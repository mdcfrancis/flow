package evolution

import "sync"

// CellRegistry is the thread-safe set of cell URNs the scheduler anneals. The
// evolution loop reads it each frame; the interactive build endpoint appends to
// it as new applications are grown at runtime.
type CellRegistry struct {
	mu   sync.Mutex
	urns []string
	seen map[string]bool
}

// NewCellRegistry seeds a registry with initial cell URNs.
func NewCellRegistry(initial ...string) *CellRegistry {
	r := &CellRegistry{seen: map[string]bool{}}
	r.Add(initial...)
	return r
}

// Add appends URNs, ignoring blanks and duplicates.
func (r *CellRegistry) Add(urns ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, u := range urns {
		if u != "" && !r.seen[u] {
			r.seen[u] = true
			r.urns = append(r.urns, u)
		}
	}
}

// Remove drops URNs from the annealing set (e.g. a parent cell retired after it
// is fractured into sub-cells). Unknown URNs are ignored. A removed URN can be
// re-Added later.
func (r *CellRegistry) Remove(urns ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	drop := map[string]bool{}
	for _, u := range urns {
		if u != "" {
			drop[u] = true
			delete(r.seen, u)
		}
	}
	kept := r.urns[:0]
	for _, u := range r.urns {
		if !drop[u] {
			kept = append(kept, u)
		}
	}
	r.urns = kept
}

// List returns a copy of the current URN set.
func (r *CellRegistry) List() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.urns...)
}
