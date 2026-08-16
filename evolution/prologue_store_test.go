package evolution

import (
	"context"
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/flux"
)

// A cell that CALLS a prologue macro (integ) without defining it must still converge:
// the sieve prepends the effective prologue before expansion, and the stored genome is
// the model's own program (no prologue bloat).
func TestSievePrependsPrologue(t *testing.T) {
	model := &scriptedModel{responses: []string{
		"(cell run-tick (integ ball_x vel_x) (i32.const 0))",
	}}
	pro := PrologueText(DefaultPrologue())
	out, err := RunSieveWithPrologue(context.Background(), model, "sys", "seed", 5, macroBallLayout(), pro, RunTickContract)
	if err != nil {
		t.Fatalf("sieve with prologue: %v", err)
	}
	if out.Artifact == nil || !out.Artifact.SyntaxPassed {
		t.Fatal("a cell calling a prologue macro must assemble")
	}
	if strings.Contains(out.Flux, "defmacro") {
		t.Fatalf("stored genome must be the model's program, not the prologue: %q", out.Flux)
	}
	if !strings.Contains(out.Flux, "(integ ball_x vel_x)") {
		t.Fatalf("stored genome should keep the macro CALL: %q", out.Flux)
	}
}

// Every default-prologue macro must actually expand + assemble when used — a starter
// kit that doesn't compile would poison every cell that calls it.
func TestDefaultPrologueAssembles(t *testing.T) {
	layout := flux.Layout{
		"ball_x":   {Type: flux.TInt, Offset: 0xB0000},
		"vel_x":    {Type: flux.TInt, Offset: 0xB0008},
		"screen_w": {Type: flux.TInt, Offset: 0xB0010},
	}
	// A cell exercising each default macro.
	cell := `(cell run-tick
  (integ ball_x vel_x)
  (reflect vel_x ball_x (i32.const 0) (get screen_w))
  (set ball_x (clampi (get ball_x) (i32.const 0) (i32.sub (get screen_w) (i32.const 1))))
  (i32.const 0))`
	src := PrologueText(DefaultPrologue()) + cell
	wat, err := flux.Expand(src, layout)
	if err != nil {
		t.Fatalf("expand with default prologue: %v", err)
	}
	if strings.Contains(wat, "(integ") || strings.Contains(wat, "(reflect") || strings.Contains(wat, "(clampi") || strings.Contains(wat, "(mini") || strings.Contains(wat, "(maxi") {
		t.Fatalf("a prologue macro was left unexpanded:\n%s", wat)
	}
	art, cerr := compiler.NewCompilerService().CompileGenotype(wat)
	if cerr != nil || art == nil || !art.SyntaxPassed {
		t.Fatalf("default-prologue cell must assemble: %v\n---\n%s", cerr, wat)
	}
}

// Harvest promotes a NEW inline macro from a committed genome into the app prologue,
// and declines names that already exist (a default or a prior app macro).
func TestHarvestProloguePromotesNewOnly(t *testing.T) {
	le := newLedger(t)
	ns := "urn:hdm:apps:demo"
	genome := `(defmacro (approach cur target step)
  (if (i32.lt_s (get cur) target) (then (set cur (i32.add (get cur) step)))))
(defmacro (reflect v p lo hi) (set v (i32.const 0)))
(cell run-tick (approach ball_x (i32.const 100) (i32.const 2)) (i32.const 0))`
	// "approach" is new → promoted; "reflect" is a default → declined (kept inline).
	if n := HarvestPrologue(le, ns, genome); n != 1 {
		t.Fatalf("expected 1 new macro harvested, got %d", n)
	}
	app := LoadPrologue(le, ns)
	if len(app) != 1 || app[0].Name != "approach" {
		t.Fatalf("only the new macro should be stored, got %+v", app)
	}
	// A second commit that redefines "approach" does not duplicate or overwrite it.
	if n := HarvestPrologue(le, ns, `(defmacro (approach a b c) (i32.const 9)) (cell run-tick (i32.const 0))`); n != 0 {
		t.Fatalf("a name already stored must not be re-harvested, got %d", n)
	}
	if app := LoadPrologue(le, ns); len(app) != 1 || !strings.Contains(app[0].Src, "i32.lt_s") {
		t.Fatalf("existing app macro must be preserved unchanged, got %+v", app)
	}
}

// The effective prologue overlays app-stored macros on the defaults, with the app
// version winning on a name clash.
func TestEffectivePrologueOverlaysApp(t *testing.T) {
	le := newLedger(t)
	ns := "urn:hdm:apps:demo"
	if base := EffectivePrologue(le, ns); len(base) != len(DefaultPrologue()) {
		t.Fatalf("empty app prologue must equal the defaults, got %d", len(base))
	}
	// Store an app macro that overrides "integ" plus a brand-new one.
	app := []PrologueMacro{
		{Name: "integ", Params: []string{"p", "v"}, Src: "(defmacro (integ p v) (set p (i32.add (get p) (i32.mul (get v) (i32.const 2)))))"},
		{Name: "half", Params: []string{"x"}, Src: "(defmacro (half x) (i32.div_s x (i32.const 2)))"},
	}
	if err := SavePrologue(le, ns, app); err != nil {
		t.Fatalf("save: %v", err)
	}
	eff := EffectivePrologue(le, ns)
	if len(eff) != len(DefaultPrologue())+1 {
		t.Fatalf("overlay size wrong: %d", len(eff))
	}
	var integ PrologueMacro
	for _, m := range eff {
		if m.Name == "integ" {
			integ = m
		}
	}
	if !strings.Contains(integ.Src, "i32.mul") {
		t.Fatalf("app override of integ did not win: %q", integ.Src)
	}
}
