package flux

import (
	"strings"
	"testing"
)

func TestGBNFCompute(t *testing.T) {
	layout := Layout{
		"ball_x":  {Type: TInt, Offset: 0xB0000},
		"ball_vx": {Type: TInt, Offset: 0xB0008},
		"hmi_key": {Type: TInt, Offset: 0x50020, ReadOnly: true},
	}
	g := GBNF(layout, KindCompute)
	// the reads clause is fixed to every field (incl. the read-only input); the
	// writes enumeration excludes it.
	if !strings.Contains(g, `(reads ball_vx ball_x hmi_key)`) {
		t.Errorf("reads clause must fix all fields:\n%s", g)
	}
	if !strings.Contains(g, `writefield ::= "ball_vx" | "ball_x"`) || strings.Contains(g, `writefield ::= "ball_vx" | "ball_x" | "hmi_key"`) {
		t.Errorf("writefield must exclude the read-only field:\n%s", g)
	}
	if !strings.Contains(g, `(writes " writes ")`) || !strings.Contains(g, "write ::= ") {
		t.Errorf("compute root must end in a write body:\n%s", g)
	}
}

func TestGBNFView(t *testing.T) {
	layout := Layout{"ball_x": {Type: TInt, Offset: 0xB0000}, "ball_y": {Type: TInt, Offset: 0xB0004}}
	g := GBNF(layout, KindView)
	if !strings.Contains(g, "draw ::= ") || !strings.Contains(g, "circle") {
		t.Errorf("view root must produce a draw body:\n%s", g)
	}
	if strings.Contains(g, "writefield") || strings.Contains(g, "(writes ") {
		t.Errorf("a view has no writes clause:\n%s", g)
	}
}

// A compute cell whose only fields are read-only has nothing to write — no grammar.
func TestGBNFEmptyWhenNothingWritable(t *testing.T) {
	if g := GBNF(Layout{"hmi_key": {Type: TInt, Offset: 0x50020, ReadOnly: true}}, KindCompute); g != "" {
		t.Errorf("expected empty grammar, got:\n%s", g)
	}
}
