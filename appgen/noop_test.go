package appgen

import (
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/flux"
)

// The no-op Flux seeds must be REAL Flux: parse, type-check, and lower to WAT.
// If any doesn't compile, genesis would fall back to WAT and the "Flux from birth"
// property is lost — so this guards the exact programs seedNoopFlux emits.
func TestNoopFluxCompiles(t *testing.T) {
	layout := flux.Layout{
		"ball_x":  {Type: flux.TInt, Offset: 0},
		"ball_y":  {Type: flux.TInt, Offset: 4},
		"ball_vx": {Type: flux.TInt, Offset: 8},
		"ball_vy": {Type: flux.TInt, Offset: 12},
	}
	cases := []struct {
		name string
		sub  Subsystem
		want string // a substring the emitted program must contain
	}{
		{
			name: "compute writes fields back unchanged",
			sub: Subsystem{
				Identity: "urn:hdm:apps:bounce:physics", Kind: KindCompute,
				Reads:  []string{"ball_x", "ball_y", "ball_vx", "ball_vy"},
				Writes: []string{"ball_x", "ball_y", "ball_vx", "ball_vy"},
			},
			want: "(write (ball_x ball_x)",
		},
		{
			name: "view draws a circle at its read ports",
			sub: Subsystem{
				Identity: "urn:hdm:apps:bounce:renderer", Kind: KindRender,
				Reads: []string{"ball_x", "ball_y"},
			},
			want: "(draw (circle ball_x ball_y 6 #xFFFFFFFF))",
		},
		{
			name: "view with no addressable reads draws at constants",
			sub: Subsystem{
				Identity: "urn:hdm:apps:bounce:renderer", Kind: KindRender,
				Reads: []string{"HMI input"}, // not in layout
			},
			want: "(circle 160 120 6",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, ok := noopFlux(tc.sub, layout)
			if !ok {
				t.Fatalf("noopFlux returned ok=false for %s", tc.sub.Identity)
			}
			if !strings.Contains(src, tc.want) {
				t.Fatalf("program %q missing %q", src, tc.want)
			}
			if _, err := flux.Compile("cell", src, layout); err != nil {
				t.Fatalf("no-op Flux did not compile: %v\nsrc: %s", err, src)
			}
		})
	}
}

// A compute cell with no Flux-addressable write ports has no valid no-op Flux —
// genesis must fall back to WAT, so ok=false.
func TestNoopFluxUnaddressableFallsBack(t *testing.T) {
	layout := flux.Layout{"ball_x": {Type: flux.TInt, Offset: 0}}
	sub := Subsystem{
		Identity: "urn:hdm:apps:x:leaf", Kind: KindCompute,
		Writes: []string{"grid"}, // array field, not in the i32 layout
	}
	if _, ok := noopFlux(sub, layout); ok {
		t.Fatal("expected ok=false when no write port is addressable")
	}
}
