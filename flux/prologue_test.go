package flux

import "testing"

// collectOps gathers every primitive operator used in an expression tree.
func collectOps(e Expr, into map[string]bool) {
	switch x := e.(type) {
	case *Let:
		for _, v := range x.Vals {
			collectOps(v, into)
		}
		collectOps(x.Body, into)
	case *Write:
		for _, v := range x.Vals {
			collectOps(v, into)
		}
	case *Draw:
		for _, p := range x.Prims {
			for _, a := range p.Args {
				collectOps(a, into)
			}
		}
	case *Prim:
		into[x.Op] = true
		for _, a := range x.Args {
			collectOps(a, into)
		}
	}
}

// After expansion, no derivation op (neg/abs/min/max/clamp) survives — the cell is
// pure core, which is all the backend lowers.
func TestPrologueExpandsToCore(t *testing.T) {
	p, err := dfltPrologue()
	if err != nil {
		t.Fatalf("prologue compile: %v", err)
	}
	cell, err := SExpr{}.Read("m",
		`(cell c (reads ball_x ball_y vel_x screen_w) (writes ball_x)
		   (let ([nx (+ ball_x vel_x)])
		     (write (ball_x (clamp (abs (neg nx)) (min 0 vel_x) (max ball_y screen_w))))))`,
		Layout{
			"ball_x": {Type: TInt, Offset: 0xB0000}, "ball_y": {Type: TInt, Offset: 0xB0004},
			"vel_x": {Type: TInt, Offset: 0xB0008}, "screen_w": {Type: TInt, Offset: 0xB000C, ReadOnly: true},
		})
	if err != nil {
		t.Fatal(err)
	}
	core, err := p.expand(cell)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	ops := map[string]bool{}
	collectOps(core.Body, ops)
	for _, deriv := range []string{"neg", "abs", "min", "max", "clamp"} {
		if ops[deriv] {
			t.Fatalf("derivation %q survived expansion; ops=%v", deriv, ops)
		}
	}
	// It still lowers (to core WAT) without hitting the "must be expanded" error.
	if _, err := Lower(cell, Layout{
		"ball_x": {Type: TInt, Offset: 0xB0000}, "ball_y": {Type: TInt, Offset: 0xB0004},
		"vel_x": {Type: TInt, Offset: 0xB0008}, "screen_w": {Type: TInt, Offset: 0xB000C, ReadOnly: true},
	}); err != nil {
		t.Fatalf("lower of a composite-using cell: %v", err)
	}
}

// The default prologue compiles (every derivation body type-checks against the core).
func TestDefaultPrologueCompiles(t *testing.T) {
	if _, err := dfltPrologue(); err != nil {
		t.Fatalf("default prologue must compile: %v", err)
	}
}
