package evolution

import (
	"context"
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/flux"
)

// scriptedFlux is a Reasoner that replays canned completions — a deterministic
// stand-in for a model that authors Flux, so the operational sieve can be proven
// without a live model.
type scriptedFlux struct {
	responses []string
	i         int
}

func (m *scriptedFlux) InvokeReasoning(_ context.Context, _, _ string) (string, error) {
	r := m.responses[min(m.i, len(m.responses)-1)]
	m.i++
	return r, nil
}

func physicsScenarios() []Scenario {
	return []Scenario{
		{Name: "moves", Entry: "run-tick", Steps: 1,
			Seed: []SeedWrite{
				{At: "0xB0000", U32: []uint32{100}}, {At: "0xB0008", U32: []uint32{5}},
				{At: "0xB0010", U32: []uint32{320}}, {At: "0xB0014", U32: []uint32{240}}},
			Expect: ScenarioExpect{Reads: []SeedWrite{{At: "0xB0000", U32: []uint32{105}}}}},
		{Name: "bounces", Entry: "run-tick", Steps: 1,
			Seed: []SeedWrite{
				{At: "0xB0000", U32: []uint32{318}}, {At: "0xB0008", U32: []uint32{5}},
				{At: "0xB0010", U32: []uint32{320}}, {At: "0xB0014", U32: []uint32{240}}},
			Expect: ScenarioExpect{Reads: []SeedWrite{
				{At: "0xB0008", Cmp: "decreased", U32: []uint32{5}},
				{At: "0xB0000", Cmp: "le", U32: []uint32{319}}}}},
	}
}

// The operational proof: a model that authors a Flux (cell …) program is driven
// through the REAL sieve (RunSieveWithLayout), and the cell it produces is
// verified — assembled AND behaviorally correct through ScenarioScore. This is
// the synthesis path a live grow uses, minus a live model.
func TestSieveLowersFluxToVerifiedCell(t *testing.T) {
	t.Setenv("HDM_FLUX_SURFACE", "sexpr")
	model := &scriptedFlux{responses: []string{
		"Here is the cell:\n```lisp\n" + fluxPhysics + "\n```",
	}}
	out, err := RunSieveWithLayout(context.Background(), model, "sys", "seed", 5, fluxBallLayout(), RunTickContract)
	if err != nil {
		t.Fatalf("sieve did not converge on Flux: %v", err)
	}
	if out.Artifact == nil || !out.Artifact.SyntaxPassed {
		t.Fatal("sieve produced no verified artifact")
	}
	if !strings.Contains(out.WAT, "run-tick") || !strings.Contains(out.WAT, "i32.store") {
		t.Fatalf("stored genotype is not lowered WAT:\n%s", out.WAT)
	}
	pass, total := ScenarioScore(context.Background(), out.Artifact.Bytecode, physicsScenarios(), DefaultPayloadOffset, DefaultStateWindow, nil)
	if pass != total {
		t.Fatalf("Flux cell from the sieve passed only %d/%d operational scenarios", pass, total)
	}
}

// The sieve must repair Flux like it repairs WAT: a first draft with a semantic
// error (an unknown name) is fed the message back and the model's next draft
// converges.
func TestSieveRepairsBadFlux(t *testing.T) {
	t.Setenv("HDM_FLUX_SURFACE", "sexpr")
	bad := `(cell physics (reads ball_x) (writes ball_x)
	  (write (ball_x (+ ball_x nonexistent))))`
	model := &scriptedFlux{responses: []string{bad, fluxPhysics}}
	out, err := RunSieveWithLayout(context.Background(), model, "sys", "seed", 5, fluxBallLayout(), RunTickContract)
	if err != nil {
		t.Fatalf("sieve did not recover from bad Flux: %v", err)
	}
	if out.Artifact == nil || out.Iterations < 2 {
		t.Fatalf("expected convergence on the repair iteration, got iters=%d artifact=%v", out.Iterations, out.Artifact != nil)
	}
}

// A model that still emits raw WAT is unaffected by a non-nil layout — the WAT
// path runs exactly as before.
func TestSieveWithLayoutStillAcceptsRawWAT(t *testing.T) {
	wat := `(module
	  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
	  (func (export "run-tick") (param $base i32) (param $cap i32) (result i32)
	    (i32.const 0)))`
	model := &scriptedFlux{responses: []string{wat}}
	out, err := RunSieveWithLayout(context.Background(), model, "sys", "seed", 3, fluxBallLayout(), RunTickContract)
	if err != nil || out.Artifact == nil || !out.Artifact.SyntaxPassed {
		t.Fatalf("raw WAT rejected under a Flux layout: %v", err)
	}
}

func TestLayoutFromContract(t *testing.T) {
	c := &AppContract{Fields: []ContractField{
		{Name: "ball_x", Offset: 0xB0000, Type: "i32"},
		{Name: "speed", Offset: 0xB0004, Type: "f32"},
		{Name: "grid", Offset: 0xB0100, Type: "i32[40]"}, // i32 array → a Flux buffer
	}}
	l := LayoutFromContract(c)
	if l["ball_x"].Type.String() != "Int" || l["ball_x"].Offset != 0xB0000 {
		t.Fatalf("ball_x mapping wrong: %+v", l["ball_x"])
	}
	if l["speed"].Type.String() != "Float" {
		t.Fatalf("speed should be Float, got %s", l["speed"].Type)
	}
	// An i32 array is now addressable as a bounded buffer (at/store), not omitted.
	if g, ok := l["grid"]; !ok || g.Type != flux.TBuffer || g.Offset != 0xB0100 || g.Len != 40 {
		t.Fatalf("grid must map to a TBuffer{off:0xB0100,len:40}, got %+v (ok=%v)", g, ok)
	}
}
