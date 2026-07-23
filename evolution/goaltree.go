package evolution

// The GOAL TREE is HDM's execution-model control structure: the recursive,
// lazily-unrolled form of the AppPlan that the scheduler WALKS depth-first.
//
// Until now the scheduler was breadth-first: every subsystem was scaffolded up
// front and chooseTarget worked whichever incomplete cell came first in
// registry-insertion order. Because a fractured parent's children are appended at
// the END of the registry, they were built LAST — after every top-level sibling —
// so effort spread across N half-built cells and nothing finished. A developer
// instead walks the tree depth-first: build one path to something concrete before
// starting the next, and when a piece is decomposed, finish its children before
// moving on.
//
// The goal tree records the LINEAGE that makes that walk possible — which children
// came from decomposing which parent — and emits a depth-first pre-order the
// scheduler uses to order its candidate set. Because chooseTarget already locks
// onto the first incomplete candidate until it converges, DFS ordering alone turns
// the breadth-first sweep into a depth-first walk: a parent's fracture children are
// worked immediately, before the next sibling subsystem.
//
// Node IDs ARE cell URNs (one buildable goal == one cell), so lineage is stable
// across restarts without any counter or clock. The root is a synthetic "#root"
// node holding the top-level subsystems.
//
// Phase 1 uses the tree purely for ORDERING (structural DFS). Status/Attempts are
// recorded for the UI and for the later phases (narrow-beam ranking, reason-carried
// backtracking, archive-for-reuse) that build on this same structure.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/mdcfrancis/flow/storage"
)

// GoalStatus is a node's lifecycle state. Phase 1 sets open/done opportunistically;
// the ordering does not depend on it (DFS is structural). Later phases drive
// building/dead + backtracking off these.
type GoalStatus string

const (
	GoalOpen     GoalStatus = "open"     // not yet realized
	GoalBuilding GoalStatus = "building" // a cell exists but its checks aren't all passing
	GoalDone     GoalStatus = "done"     // realized and verified
	GoalDead     GoalStatus = "dead"     // abandoned (backtracked past); reserved for Phase 2
)

// Attempt records something tried at a node, so backtracking (Phase 2) can carry
// the reason a path failed rather than blindly retrying.
type Attempt struct {
	Kind     string   `json:"kind"`               // "build" | "decompose"
	Reason   string   `json:"reason,omitempty"`   // why (e.g. the stall that forced a decompose)
	Outcome  string   `json:"outcome,omitempty"`  // what happened
	ChildIDs []string `json:"childIds,omitempty"` // children produced by a decompose
}

// GoalNode is one goal: an objective, the cell realizing it (for leaves), and the
// children it decomposed into (for branches).
type GoalNode struct {
	ID        string     `json:"id"` // == cell URN for buildable goals; "<ns>#root" for the root
	Parent    string     `json:"parent,omitempty"`
	Objective string     `json:"objective,omitempty"`
	Cell      string     `json:"cell,omitempty"` // the cell URN this goal builds ("" for root)
	Children  []string   `json:"children,omitempty"`
	Status    GoalStatus `json:"status,omitempty"`
	Kind      string     `json:"kind,omitempty"` // "root" | "leaf" | "branch"
	Attempts  []Attempt  `json:"attempts,omitempty"`
	Archived  bool       `json:"archived,omitempty"` // dead subtree kept resolvable for reuse (Phase 2)
}

// GoalTree is one application's recursive goal structure.
type GoalTree struct {
	Namespace string               `json:"namespace"`
	Objective string               `json:"objective"`
	RootID    string               `json:"rootId"`
	Nodes     map[string]*GoalNode `json:"nodes"`
}

func goalTreeRefURN(namespace string) string { return namespace + ":goaltree" }

func rootID(namespace string) string { return namespace + "#root" }

// NewGoalTree creates an empty tree with just its root.
func NewGoalTree(namespace, objective string) *GoalTree {
	rid := rootID(namespace)
	return &GoalTree{
		Namespace: namespace,
		Objective: objective,
		RootID:    rid,
		Nodes: map[string]*GoalNode{
			rid: {ID: rid, Objective: objective, Kind: "root", Status: GoalOpen},
		},
	}
}

// SaveGoalTree persists an application's goal tree.
func SaveGoalTree(ledger *storage.LedgerEngine, namespace string, t *GoalTree) error {
	raw, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("encode goal tree: %w", err)
	}
	h, err := ledger.WriteBlock(raw)
	if err != nil {
		return fmt.Errorf("persist goal tree: %w", err)
	}
	return ledger.UpdateRef(goalTreeRefURN(namespace), h)
}

// LoadGoalTree returns an application's goal tree, or nil if none exists.
func LoadGoalTree(ledger *storage.LedgerEngine, namespace string) *GoalTree {
	h, err := ledger.GetRef(goalTreeRefURN(namespace))
	if err != nil {
		return nil
	}
	raw, err := ledger.ReadBlock(h)
	if err != nil {
		return nil
	}
	var t GoalTree
	if json.Unmarshal(raw, &t) != nil || t.Nodes == nil {
		return nil
	}
	return &t
}

// root returns the root node, creating it if somehow missing.
func (t *GoalTree) root() *GoalNode {
	if t.RootID == "" {
		t.RootID = rootID(t.Namespace)
	}
	if t.Nodes == nil {
		t.Nodes = map[string]*GoalNode{}
	}
	r := t.Nodes[t.RootID]
	if r == nil {
		r = &GoalNode{ID: t.RootID, Objective: t.Objective, Kind: "root", Status: GoalOpen}
		t.Nodes[t.RootID] = r
	}
	return r
}

// EnsureLeaf attaches a buildable goal (cell URN) under a parent, if not already
// present. It is idempotent: seeding the same subsystem twice is a no-op, and a
// child that already exists is not re-parented.
func (t *GoalTree) EnsureLeaf(parentID, cell, objective string) {
	if cell == "" {
		return
	}
	if t.Nodes == nil {
		t.Nodes = map[string]*GoalNode{}
	}
	parent := t.Nodes[parentID]
	if parent == nil {
		parent = t.root()
		parentID = parent.ID
	}
	if n := t.Nodes[cell]; n != nil {
		return // already known — keep its existing lineage
	}
	t.Nodes[cell] = &GoalNode{
		ID: cell, Parent: parentID, Objective: objective, Cell: cell,
		Kind: "leaf", Status: GoalOpen,
	}
	parent.Children = append(parent.Children, cell)
}

// SeedSubsystem attaches a top-level subsystem (under the root).
func (t *GoalTree) SeedSubsystem(cell, objective string) {
	t.EnsureLeaf(t.root().ID, cell, objective)
}

// RecordFracture records a decomposition: the parent cell becomes a branch, its
// children are attached beneath it (so the walk descends into them before the next
// sibling), and the reason is kept on the parent for reason-carried backtracking.
func (t *GoalTree) RecordFracture(parentCell string, children []GoalSpec, reason string) {
	parent := t.Nodes[parentCell]
	if parent == nil {
		// Parent wasn't seeded (e.g. fracture before the tree knew of it) — attach it
		// under the root so its lineage still orders the children.
		t.SeedSubsystem(parentCell, "")
		parent = t.Nodes[parentCell]
	}
	parent.Kind = "branch"
	ids := make([]string, 0, len(children))
	for _, c := range children {
		t.EnsureLeaf(parentCell, c.Cell, c.Objective)
		ids = append(ids, c.Cell)
	}
	parent.Attempts = append(parent.Attempts, Attempt{
		Kind: "decompose", Reason: reason, ChildIDs: ids,
	})
}

// GoalSpec is a minimal (cell, objective) pair for seeding/fracture, kept free of
// any appgen types so the tree stays dependency-light.
type GoalSpec struct {
	Cell      string
	Objective string
}

// MarkStatus sets a node's status (best-effort; unknown IDs are ignored).
func (t *GoalTree) MarkStatus(cell string, s GoalStatus) {
	if n := t.Nodes[cell]; n != nil {
		n.Status = s
	}
}

// DFSCells returns the buildable cell URNs in depth-first pre-order: a parent
// appears before its children, and a whole subtree before the next sibling. The
// root (no cell) is skipped; archived nodes and their subtrees are skipped.
func (t *GoalTree) DFSCells() []string {
	if t == nil {
		return nil
	}
	var out []string
	var visit func(id string)
	seen := map[string]bool{}
	visit = func(id string) {
		if seen[id] {
			return // cycle guard — lineage should be a tree, but never loop
		}
		seen[id] = true
		n := t.Nodes[id]
		if n == nil || n.Archived {
			return
		}
		if n.Cell != "" {
			out = append(out, n.Cell)
		}
		// Deterministic child order (attachment order is already deterministic; sort
		// as a belt-and-braces guard so persistence round-trips are stable).
		kids := append([]string(nil), n.Children...)
		for _, c := range kids {
			visit(c)
		}
	}
	visit(t.RootID)
	return out
}

// Order reorders a candidate list into the tree's depth-first pre-order. Candidates
// the tree doesn't know about are appended after the ordered ones, preserving their
// original relative order — so an untracked cell never gets lost, it just isn't
// prioritized by the walk.
func (t *GoalTree) Order(candidates []string) []string {
	if t == nil || len(candidates) < 2 {
		return candidates
	}
	inSet := map[string]bool{}
	for _, u := range candidates {
		inSet[u] = true
	}
	rank := map[string]int{}
	for i, cell := range t.DFSCells() {
		rank[cell] = i
	}
	ordered := make([]string, 0, len(candidates))
	var tail []string
	for _, u := range candidates {
		if _, ok := rank[u]; ok {
			ordered = append(ordered, u)
		} else {
			tail = append(tail, u)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool { return rank[ordered[i]] < rank[ordered[j]] })
	return append(ordered, tail...)
}

// ApplyLiveStatus refreshes each buildable node's Status from a live scorer (the
// gauntlet), so a rendered tree reflects reality rather than the last persisted
// state. A node with no checks or an unresolvable cell is left as-is. Intended for
// display on a loaded (unsaved) copy; the persisted tree stays structural.
func (t *GoalTree) ApplyLiveStatus(score func(cell string) (passed, total int, ok bool)) {
	if t == nil || score == nil {
		return
	}
	for _, n := range t.Nodes {
		if n.Cell == "" || n.Kind == "branch" {
			continue // root and retired-parent branches have no live cell to score
		}
		passed, total, ok := score(n.Cell)
		switch {
		case !ok || total == 0:
			// leave existing status (unknown / not yet built)
		case passed >= total:
			n.Status = GoalDone
		default:
			n.Status = GoalBuilding
		}
	}
}

// Render formats the tree as an indented outline for the console / a UI panel.
func (t *GoalTree) Render() string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "GOAL TREE — %s\n", t.Objective)
	var walk func(id string, depth int)
	seen := map[string]bool{}
	walk = func(id string, depth int) {
		if seen[id] {
			return
		}
		seen[id] = true
		n := t.Nodes[id]
		if n == nil {
			return
		}
		if n.Kind != "root" {
			label := n.Objective
			if label == "" {
				label = shortName(n.Cell)
			}
			status := string(n.Status)
			if n.Kind == "branch" {
				status = "decomposed" // retired parent whose children carry the work
			}
			if n.Archived {
				status = "archived"
			}
			fmt.Fprintf(&b, "%s- [%s] %s\n", strings.Repeat("  ", depth), status, label)
		}
		for _, c := range n.Children {
			walk(c, depth+1)
		}
	}
	walk(t.RootID, 0)
	return b.String()
}
