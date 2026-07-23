package execution

import (
	"context"
	"fmt"

	"github.com/tetratelabs/wazero/api"
)

// Software paging: a simple, location-independent, protected private-memory ABI.
//
// The runtime gives each cell a fixed VIRTUAL WINDOW (windowBase..windowEnd) that its WAT
// treats as a plain flat block of memory — no handles, no base arithmetic, no page table in
// the guest. Behind that window the runtime maps a PHYSICAL page from a host-managed page
// zone: before a cell runs it copies the cell's mapped page INTO the window (page-in), and
// after it copies the window back OUT to the page (page-out).
//
// Two properties fall out, and they are the pillars of a real OS memory ABI:
//   - PROTECTION. A cell only ever sees its OWN mapped page in the window; a non-owner is
//     never mapped to another cell's page, so it cannot read or corrupt it. This isolation
//     is STRUCTURAL — it holds even with mask enforcement disabled. (Mask enforcement adds a
//     second layer: the physical page zone is hidden from any guest that tries to address it
//     directly, so the window is the ONLY guest-visible surface.)
//   - RELOCATION / LOCATION INDEPENDENCE. Every cell uses the same windowBase, so guest code
//     is position-independent and the runtime may place or move the physical page freely.
//
// Page selection is either STANDING (host/boundary sets rm.mapped[urn] via MapPage — the
// cell's own page every tick) or PER-DISPATCH (a guest calls paging.select(handle) to choose
// which instance its NEXT dispatched op operates on — how one cell drives several dicts).
//
// The mapping (urn/handle -> page) is also the seam for later moving a (page + owner ops)
// unit to another runtime instance: page-in/page-out is where a local page can become remote.

const pageSize = 0x1000 // 4 KiB allocation + mapping granule

// pageRec is one physical page in the host-managed page zone. Guests never see base.
type pageRec struct {
	handle uint32
	base   uint32
	length uint32
	free   bool
	// owners, when non-nil, is the set of cell URNs allowed to SELECT this page (guest path).
	// nil = public (any cell may select). Ownership beats handle-possession: leaking the
	// handle bits does not grant access. Host-driven MapPage is trusted and unenforced.
	owners map[string]bool
}

// --- public API (locks mu) -----------------------------------------------------------------

// AllocPage reserves a page-aligned physical region (at most one window's worth) and returns
// an opaque handle. The guest never learns the base — the runtime maps the page into the
// window.
func (rm *RuntimeManager) AllocPage(size uint32) (uint32, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return rm.allocPageLocked(size)
}

// AllocPageOwned reserves a page owned by the given cell URNs — only they may SELECT it from
// a guest. This is the boundary/growth-facing constructor (a cell that "owns a page" gets one
// bound to it); the plain AllocPage leaves the page public.
func (rm *RuntimeManager) AllocPageOwned(owners []string, size uint32) (uint32, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	h, err := rm.allocPageLocked(size)
	if err != nil {
		return 0, err
	}
	if p := rm.pageRecByHandle(h); p != nil && len(owners) > 0 {
		set := map[string]bool{}
		for _, o := range owners {
			if o != "" {
				set[o] = true
			}
		}
		p.owners = set
	}
	return h, nil
}

// FreePage releases a page's handle for reuse.
func (rm *RuntimeManager) FreePage(handle uint32) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.freePageLocked(handle)
}

// MapPage sets the STANDING page mapped into a cell's window on its runs. A handle of 0
// unmaps the cell, so it sees a zeroed window.
func (rm *RuntimeManager) MapPage(urn string, handle uint32) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.mapped == nil {
		rm.mapped = map[string]uint32{}
	}
	if handle == 0 {
		delete(rm.mapped, urn)
	} else {
		rm.mapped[urn] = handle
	}
}

// PageBase reports a page's physical base (for host/test inspection only — guests never use
// it). Also proves the runtime placed the page somewhere other than the window.
func (rm *RuntimeManager) PageBase(handle uint32) (uint32, bool) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if p := rm.pageRecByHandle(handle); p != nil {
		return p.base, true
	}
	return 0, false
}

// --- locked internals (caller holds mu; host functions run under it) ------------------------

func (rm *RuntimeManager) allocPageLocked(size uint32) (uint32, error) {
	if size == 0 {
		size = pageSize
	}
	n := (size + pageSize - 1) &^ uint32(pageSize-1)
	if n > windowEnd-windowBase {
		return 0, fmt.Errorf("page of %d bytes exceeds the %d-byte window", n, windowEnd-windowBase)
	}
	for i := range rm.pages {
		if p := &rm.pages[i]; p.free && p.length >= n {
			p.free = false
			p.owners = nil // reused slot starts public; caller re-owns if needed
			return p.handle, nil
		}
	}
	if rm.pageNext == 0 {
		rm.pageNext = pageZoneBase
	}
	if rm.pageNext+n > pageZoneEnd {
		return 0, fmt.Errorf("page zone exhausted (need %d bytes)", n)
	}
	base := rm.pageNext
	rm.pageNext += n
	h := uint32(len(rm.pages)) + 1
	rm.pages = append(rm.pages, pageRec{handle: h, base: base, length: n})
	return h, nil
}

func (rm *RuntimeManager) freePageLocked(handle uint32) {
	if p := rm.pageRecByHandle(handle); p != nil {
		p.free = true
	}
}

// pageRecByHandle returns the live physical page for a handle, or nil. Caller holds mu.
func (rm *RuntimeManager) pageRecByHandle(handle uint32) *pageRec {
	if handle == 0 || int(handle) > len(rm.pages) {
		return nil
	}
	if p := &rm.pages[handle-1]; !p.free {
		return p
	}
	return nil
}

// pageForLocked returns the live page STANDING-mapped to urn, or nil. Caller holds mu.
func (rm *RuntimeManager) pageForLocked(urn string) *pageRec {
	return rm.pageRecByHandle(rm.mapped[urn])
}

// pageInRec copies a page into the window (or zeroes the window if p is nil), so the guest
// always sees a simple flat block determined only by ITS page — never residue from a previous
// cell. Caller holds mu.
func (rm *RuntimeManager) pageInRec(p *pageRec) {
	if rm.sharedMem == nil {
		return
	}
	rm.zeroRange(windowBase, windowEnd-windowBase)
	if p != nil {
		if src, ok := rm.read(p.base, p.length); ok {
			cp := make([]byte, len(src))
			copy(cp, src)
			rm.sharedMem.Write(windowBase, cp)
		}
	}
}

// pageOutRec copies the window back to a page, persisting the tick's work. No-op for nil.
// Caller holds mu.
func (rm *RuntimeManager) pageOutRec(p *pageRec) {
	if rm.sharedMem == nil || p == nil {
		return
	}
	if win, ok := rm.read(windowBase, p.length); ok {
		cp := make([]byte, len(win))
		copy(cp, win)
		rm.sharedMem.Write(p.base, cp)
	}
}

// pageIn / pageOut map a cell's STANDING page around a top-level tick. Caller holds mu.
func (rm *RuntimeManager) pageIn(urn string)  { rm.pageInRec(rm.pageForLocked(urn)) }
func (rm *RuntimeManager) pageOut(urn string) { rm.pageOutRec(rm.pageForLocked(urn)) }

// zeroRange clears a region of shared memory in place. Caller holds mu.
func (rm *RuntimeManager) zeroRange(off, length uint32) {
	if b, ok := rm.read(off, length); ok {
		for i := range b {
			b[i] = 0
		}
	}
}

// allocatedPages returns [base,len] of every live physical page — the host-only page zone
// that no guest may read or write directly (it is reached only through the window). Caller
// holds mu. Used to hide the zone under mask enforcement.
func (rm *RuntimeManager) allocatedPages() [][2]uint32 {
	var out [][2]uint32
	for i := range rm.pages {
		if p := &rm.pages[i]; !p.free {
			out = append(out, [2]uint32{p.base, p.length})
		}
	}
	return out
}

// --- guest-facing host functions (hdm:kernel/paging) ---------------------------------------
// These run during guest execution, so mu is already held: they use the *Locked internals.

// hostPageAlloc: alloc(size) -> handle (0 on failure). Lets a guest create its own private
// structure (e.g. a fresh dict instance); the calling cell becomes its sole owner.
func (rm *RuntimeManager) hostPageAlloc(_ context.Context, caller api.Module, stack []uint64) {
	h, err := rm.allocPageLocked(uint32(stack[0]))
	if err != nil {
		stack[0] = 0
		return
	}
	if p := rm.pageRecByHandle(h); p != nil && caller != nil {
		p.owners = map[string]bool{caller.Name(): true}
	}
	stack[0] = uint64(h)
}

// mayAccess reports whether cell `name` may select/free page p — true if p is public (nil
// owners) or name is an owner. Ownership beats handle-possession.
func (p *pageRec) mayAccess(name string) bool {
	return p.owners == nil || p.owners[name]
}

// hostPageSelect: select(handle) -> ok. Sets the page the NEXT dispatched op operates on —
// how a caller drives several instances of the same primitive. Consumed by one invoke-cell.
// Denied (0, no effect) if the caller is not an owner of a private page.
func (rm *RuntimeManager) hostPageSelect(_ context.Context, caller api.Module, stack []uint64) {
	p := rm.pageRecByHandle(uint32(stack[0]))
	if p == nil || (caller != nil && !p.mayAccess(caller.Name())) {
		stack[0] = 0
		return
	}
	rm.pendingPage = p.handle
	stack[0] = 1
}

// hostPageFree: free(handle). Denied for a non-owner of a private page.
func (rm *RuntimeManager) hostPageFree(_ context.Context, caller api.Module, stack []uint64) {
	p := rm.pageRecByHandle(uint32(stack[0]))
	if p == nil || (caller != nil && !p.mayAccess(caller.Name())) {
		return
	}
	rm.freePageLocked(p.handle)
}
