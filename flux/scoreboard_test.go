package flux

import (
	"strings"
	"testing"
)

// fakeGen replays a fixed sequence of (src, tokens) outputs, cycling — so a test can
// script exactly what the "model" produces.
type fakeGen struct {
	outs []struct {
		src string
		tok int
	}
	i int
}

func (f *fakeGen) Generate(_, _ string) (string, int, error) {
	o := f.outs[f.i%len(f.outs)]
	f.i++
	return o.src, o.tok, nil
}

// startsWithCell is a validity stub that decouples the scoring test from the real
// checker: a "valid" program is any that begins with "(cell".
func startsWithCell(src string, _ Layout) bool { return strings.HasPrefix(src, "(cell") }

func TestRunScoreboardScoresAxes(t *testing.T) {
	gen := &fakeGen{outs: []struct {
		src string
		tok int
	}{
		{"(cell A)", 10}, {"(cell A)", 10}, {"(cell B)", 12}, {"junk", 5},
	}}
	tasks := []Task{{Name: "t", Kind: KindView, Layout: Layout{"x": {Type: TInt}}}}
	variants := []Variant{{Name: "v", Grammar: func(Layout, CellKind) string { return "" }, Valid: startsWithCell}}

	board := RunScoreboard(gen, tasks, variants, 4)
	if len(board) != 1 {
		t.Fatalf("expected one score, got %d", len(board))
	}
	s := board[0]
	if s.Samples != 4 {
		t.Fatalf("samples = %d, want 4", s.Samples)
	}
	if s.ValidRate != 0.75 { // 3 of 4 start with (cell
		t.Fatalf("valid rate = %v, want 0.75", s.ValidRate)
	}
	// canonicality: of the 3 valid, "(cell A)" appears twice → 2/3
	if got := s.Canonicality; got < 0.66 || got > 0.67 {
		t.Fatalf("canonicality = %v, want ~0.667", got)
	}
	// mean tokens over valid samples: (10+10+12)/3
	if got := s.MeanTokens; got < 10.66 || got > 10.67 {
		t.Fatalf("mean tokens = %v, want ~10.667", got)
	}
}

func TestScoreboardRanksByFitness(t *testing.T) {
	// A: always the same short valid program. B: always a longer valid program.
	// A should rank first (equal validity + canonicality, fewer tokens).
	genA := &fakeGen{outs: []struct {
		src string
		tok int
	}{{"(cell A)", 10}}}
	genB := &fakeGen{outs: []struct {
		src string
		tok int
	}{{"(cell BBBBBB)", 60}}}
	tasks := []Task{{Name: "t", Kind: KindView, Layout: Layout{"x": {Type: TInt}}}}
	grammar := func(Layout, CellKind) string { return "" }

	a := RunScoreboard(genA, tasks, []Variant{{Name: "A", Grammar: grammar, Valid: startsWithCell}}, 3)[0]
	b := RunScoreboard(genB, tasks, []Variant{{Name: "B", Grammar: grammar, Valid: startsWithCell}}, 3)[0]
	if a.ValidRate != 1 || b.ValidRate != 1 || a.Canonicality != 1 || b.Canonicality != 1 {
		t.Fatalf("both should be fully valid + canonical: %+v %+v", a, b)
	}
	if a.Fitness() <= b.Fitness() {
		t.Fatalf("fewer tokens must win at equal validity/canonicality: A=%v B=%v", a.Fitness(), b.Fitness())
	}
}

func TestModalFractionEdges(t *testing.T) {
	if got := modalFraction([]string{"(cell a)", "(cell   a)", "(cell\na)"}); got != 1.0 {
		t.Fatalf("whitespace-only differences must be one canonical form, got %v", got)
	}
	if got := modalFraction([]string{"a", "b", "c"}); got < 0.33 || got > 0.34 {
		t.Fatalf("all-distinct → 1/N, got %v", got)
	}
	if got := modalFraction(nil); got != 0 {
		t.Fatalf("empty → 0, got %v", got)
	}
}

// The default validator is the real Flux checker: a well-typed cell passes, garbage
// and an ill-typed cell fail.
func TestDefaultValidUsesChecker(t *testing.T) {
	layout := Layout{"ball_x": {Type: TInt, Offset: 0xB0000}, "ball_y": {Type: TInt, Offset: 0xB0004}}
	ok := `(cell c (reads ball_x ball_y) (draw (circle ball_x ball_y 8 #xFFCC33FF)))`
	if !defaultValid(ok, layout) {
		t.Fatal("a well-formed view cell must validate")
	}
	if defaultValid("not flux at all", layout) {
		t.Fatal("garbage must not validate")
	}
}
