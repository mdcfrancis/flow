package flux

import (
	"sort"
	"strings"
	"testing"
)

func ifaceNames(is []Interface) string {
	ns := make([]string, len(is))
	for i, x := range is {
		ns[i] = x.Name
	}
	sort.Strings(ns)
	return strings.Join(ns, ",")
}

// A cell implicitly satisfies the interfaces its shape implies — no declaration.
func TestDeriveInterfaces(t *testing.T) {
	layout := Layout{
		"ball_x":      {Type: TInt, Offset: 0xB0000},
		"ball_speed":  {Type: TInt, Offset: 0xB0010},
		"hmi_slider0": {Type: TInt, Offset: 0x50024, ReadOnly: true},
	}
	cases := []struct {
		name string
		cell *Cell
		want string // sorted interface names
	}{
		{"compute writer", &Cell{Kind: KindCompute, Reads: []string{"ball_x"}, Writes: []string{"ball_x"}}, "stateful,tick"},
		{"view", &Cell{Kind: KindView, Reads: []string{"ball_x"}}, "view"},
		{"input adapter", &Cell{Kind: KindCompute, Reads: []string{"hmi_slider0", "ball_speed"}, Writes: []string{"ball_speed"}}, "input-source,stateful,tick"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ifaceNames(DeriveInterfaces(tc.cell, layout)); got != tc.want {
				t.Fatalf("interfaces = %q, want %q", got, tc.want)
			}
		})
	}

	// The input adapter needs the "HMI input" capability; a plain compute cell does not.
	if !NeedsHMIInput(cases[2].cell, layout) {
		t.Error("a cell reading hmi_slider0 must need HMI input")
	}
	if NeedsHMIInput(cases[0].cell, layout) {
		t.Error("a cell reading no read-only field must not need HMI input")
	}
	// Primary entry is the sole entry-bearing interface.
	if e := PrimaryEntry(cases[0].cell, layout); e != "run-tick" {
		t.Errorf("compute primary entry = %q, want run-tick", e)
	}
	if e := PrimaryEntry(cases[1].cell, layout); e != "render-frame" {
		t.Errorf("view primary entry = %q, want render-frame", e)
	}
}

// EntryOf derives the entry from source alone (no layout), structurally.
func TestEntryOf(t *testing.T) {
	if e, err := EntryOf(`(cell p (reads ball_x) (writes ball_x) (write (ball_x (+ ball_x 1))))`); err != nil || e != "run-tick" {
		t.Fatalf("compute EntryOf = %q, %v; want run-tick", e, err)
	}
	if e, err := EntryOf(`(cell r (reads ball_x ball_y) (draw (circle ball_x ball_y 6 #xFFFFFFFF)))`); err != nil || e != "render-frame" {
		t.Fatalf("view EntryOf = %q, %v; want render-frame", e, err)
	}
	// A draw nested under a let is still a view.
	if e, _ := EntryOf(`(cell r (reads a) (let ([b a]) (draw (circle b b 6 #xFFFFFFFF))))`); e != "render-frame" {
		t.Fatalf("let-wrapped draw EntryOf = %q, want render-frame", e)
	}
}
