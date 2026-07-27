package evolution

import "strings"

// FieldMask is a cell's enforced shared-state boundary. The SAME ranges are fed to
// the live runtime (main.buildMasks → SetCellMask) and to acceptance replay
// (execScenario), so a cell is GRADED against the exact boundary it will RUN under.
//
// Poison = byte ranges hidden from reads (zeroed for the step). Revert = ranges the
// cell may not persist a write to (restored after the step). Poison ⊆ Revert by
// construction, so restoring Revert also restores every poisoned range.
//
// Grading under the mask closes the "static ball / static renderer" gap: a cell
// whose DECLARED ports omit a field it must write (or read) commits green when
// graded UNMASKED, then has that write reverted (or that read zeroed) every frame
// live and renders static. Under the mask the reverted write / zeroed read fails the
// scenario's trajectory/read/draw assertion, so the cell STALLS instead — and a
// stall is what triggers EvolveBoundary to widen the too-tight ports (grounded on
// the declared-vs-verified gap), which then lets the cell pass and move.
type FieldMask struct {
	Poison [][2]uint32
	Revert [][2]uint32
}

// BuildFieldMask derives a cell's enforced mask from its component's DECLARED ports
// and the app contract: every contract field the cell did not declare-write is
// reverted; a field it can neither declare-read nor declare-write is also poisoned
// (poison ⊆ revert, so a poisoned range is always restored and can never leak a 0).
// Returns nil when there is nothing to enforce — no component, no contract, or the
// cell declared every field — so callers treat nil as "unmasked".
func BuildFieldMask(comp *ComponentMap, ct *AppContract) *FieldMask {
	if comp == nil || ct == nil {
		return nil
	}
	readable := map[string]bool{}
	for _, f := range comp.DeclaredReads {
		readable[strings.ToLower(f)] = true
	}
	writable := map[string]bool{}
	for _, f := range comp.DeclaredWrites {
		writable[strings.ToLower(f)] = true
	}
	var m FieldMask
	for _, f := range ct.Fields {
		off, n, ok := ct.FieldRange(f.Name)
		if !ok || n <= 0 {
			continue
		}
		name := strings.ToLower(f.Name)
		r := [2]uint32{uint32(off), uint32(n)}
		if writable[name] {
			continue
		}
		m.Revert = append(m.Revert, r)
		// Poison ONLY a field the cell can neither read nor write. A field it may
		// read (but not write) is left readable; it is still reverted so a stray
		// write cannot persist.
		if !readable[name] {
			m.Poison = append(m.Poison, r)
		}
	}
	if len(m.Revert) == 0 && len(m.Poison) == 0 {
		return nil
	}
	return &m
}

// revertRanges is the nil-safe revert set, so the post-step restore loop reads
// cleanly whether or not a mask is in force.
func (m *FieldMask) revertRanges() [][2]uint32 {
	if m == nil {
		return nil
	}
	return m.Revert
}

// firstMask extracts the optional mask threaded through the (variadic) scoring
// functions: nil means "grade unmasked", preserving every caller that passes none.
func firstMask(mask []*FieldMask) *FieldMask {
	if len(mask) > 0 {
		return mask[0]
	}
	return nil
}
