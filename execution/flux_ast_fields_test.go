package execution

import (
	"context"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/flux"
	"github.com/mdcfrancis/flow/inference"
	"github.com/mdcfrancis/flow/storage"
)

// astFieldsCell is the self-hosted parser extended with FIELD REFERENCES, so it parses
// real expressions over shared state, not just literals. Token encoding:
//   - tok >= 0        : a number literal → leaf (tag 0, value)
//   - -20 <= tok <= -1: an operator (opcode = -tok) → inner node
//   - tok <= -100     : a field reference (index = -tok-100) → var leaf (tag 99, index)
//
// With this, the cell parses the full expression grammar OVER FIELDS — the guts of any
// Flux cell — into a serialized AST.
const astFieldsCell = `(cell astf (reads tokens cursor stack sp nodeCount) (writes nodes stack sp nodeCount cursor)
  (let ([tok (at tokens cursor)]
        [isop (and (<= tok -1) (>= tok -20))]
        [isfield (<= tok -100)]
        [op (neg tok)]
        [ar (if isop (if (or (= op 16) (or (= op 17) (= op 18))) 1 (if (or (= op 19) (= op 20)) 3 2)) 0)]
        [nc nodeCount]
        [base (* nc 4)]
        [c0 (at stack (- sp ar))]
        [c1 (at stack (+ (- sp ar) 1))]
        [c2 (at stack (+ (- sp ar) 2))]
        [tag (if isop op (if isfield 99 0))]
        [leafval (if isfield (- op 100) tok)]
        [pushPos (if isop (- sp ar) sp)])
    (write
      (store nodes base tag)
      (store nodes (+ base 1) (if isop c0 leafval))
      (store nodes (+ base 2) (if isop c1 0))
      (store nodes (+ base 3) (if isop c2 0))
      (store stack pushPos nc)
      (nodeCount (+ nodeCount 1))
      (sp (if isop (+ (- sp ar) 1) (+ sp 1)))
      (cursor (+ cursor 1)))))`

// TestFluxParserProducesRealIR is the capstone: a Flux cell parses a postfix
// expression (over fields) into a node buffer; Go reconstructs the s-expression from
// that buffer; and compiling the reconstruction through the REAL flux compiler yields
// the SAME WAT as compiling the hand-written expression. The parser produces exactly
// the language's IR.
func TestFluxParserProducesRealIR(t *testing.T) {
	ctx := context.Background()
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)
	cs := compiler.NewCompilerService()

	const tokBase, stkBase, nodeBase = 0xB2000, 0xB3000, 0xB4000
	const cursorOff, spOff, ncOff = 0xB0000, 0xB0004, 0xB0008
	const cap = 48
	layout := flux.Layout{
		"tokens":    {Type: flux.TBuffer, Offset: tokBase, Len: cap},
		"stack":     {Type: flux.TBuffer, Offset: stkBase, Len: cap},
		"nodes":     {Type: flux.TBuffer, Offset: nodeBase, Len: cap * 4},
		"cursor":    {Type: flux.TInt, Offset: cursorOff},
		"sp":        {Type: flux.TInt, Offset: spOff},
		"nodeCount": {Type: flux.TInt, Offset: ncOff},
	}
	wat, err := flux.Compile("m", astFieldsCell, layout)
	if err != nil {
		t.Fatalf("astf compile: %v", err)
	}
	loadWAT(t, rm, cs, "urn:hdm:test:astf", wat)

	w := func(off uint32, v int32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(v))
		rm.sharedMem.Write(off, b[:])
	}
	r := func(off uint32) int32 { v, _ := rm.PeekU32(off); return int32(v) }

	parse := func(toks []int32) ([]int32, int32) {
		for i := 0; i < cap; i++ {
			w(tokBase+uint32(i*4), 0)
			w(stkBase+uint32(i*4), 0)
		}
		for i := 0; i < cap*4; i++ {
			w(nodeBase+uint32(i*4), 0)
		}
		for i, tk := range toks {
			w(tokBase+uint32(i*4), tk)
		}
		w(cursorOff, 0)
		w(spOff, 0)
		w(ncOff, 0)
		for range toks {
			if _, _, _, err := rm.ExecuteTrampoline("h", "urn:hdm:test:astf", "run-tick", 0, 0); err != nil {
				t.Fatalf("astf: %v", err)
			}
		}
		nc := int(r(ncOff))
		nodes := make([]int32, nc*4)
		for i := range nodes {
			nodes[i] = r(nodeBase + uint32(i*4))
		}
		return nodes, r(stkBase)
	}

	// The field-index ↔ name table (must match the token encoding below).
	fields := []string{"nx", "vel_x", "screen_w"}
	opName := map[int32]string{1: "+", 2: "-", 3: "*", 4: "/", 5: "mod", 6: "<", 7: "<=",
		8: ">", 9: ">=", 10: "=", 11: "!=", 12: "and", 13: "or", 14: "min", 15: "max",
		16: "neg", 17: "abs", 18: "not", 19: "if", 20: "clamp"}
	arity := func(op int32) int {
		if op >= 16 && op <= 18 {
			return 1
		}
		if op == 19 || op == 20 {
			return 3
		}
		return 2
	}
	// reconstruct the s-expression from the node buffer the CELL produced.
	var build func(nodes []int32, h int32) string
	build = func(nodes []int32, h int32) string {
		tag, x, y, z := nodes[h*4], nodes[h*4+1], nodes[h*4+2], nodes[h*4+3]
		switch tag {
		case 0:
			return strconv.FormatInt(int64(x), 10)
		case 99:
			return fields[x]
		}
		name := opName[tag]
		switch arity(tag) {
		case 1:
			return "(" + name + " " + build(nodes, x) + ")"
		case 3:
			return fmt.Sprintf("(%s %s %s %s)", name, build(nodes, x), build(nodes, y), build(nodes, z))
		default:
			return fmt.Sprintf("(%s %s %s)", name, build(nodes, x), build(nodes, y))
		}
	}

	// A layout for compiling reconstructed expressions (fields + an out target).
	exprLayout := flux.Layout{
		"nx":       {Type: flux.TInt, Offset: 0xC0000},
		"vel_x":    {Type: flux.TInt, Offset: 0xC0004},
		"screen_w": {Type: flux.TInt, Offset: 0xC0008, ReadOnly: true},
		"out":      {Type: flux.TInt, Offset: 0xC000C},
	}
	compileExpr := func(expr string) string {
		src := "(cell c (reads nx vel_x screen_w) (writes out) (write (out " + expr + ")))"
		w, err := flux.Compile("m", src, exprLayout)
		if err != nil {
			t.Fatalf("flux compile %q: %v", src, err)
		}
		return w
	}

	// field-ref tokens: nx=-100, vel_x=-101, screen_w=-102.
	const nx, vx, sw = -100, -101, -102
	const add, mul, sub, lt, ge, or, neg, iff, clamp = -1, -3, -2, -6, -9, -13, -16, -19, -20

	cases := []struct {
		name     string
		toks     []int32
		expected string // the equivalent hand-written Flux expression
	}{
		{"nx+vel_x", []int32{nx, vx, add}, "(+ nx vel_x)"},
		{"clamp(nx,0,screen_w)", []int32{nx, 0, sw, clamp}, "(clamp nx 0 screen_w)"},
		{
			"physics vel reflect",
			// (if (or (< nx 0) (>= nx screen_w)) (neg vel_x) vel_x)
			[]int32{nx, 0, lt, nx, sw, ge, or, vx, neg, vx, iff},
			"(if (or (< nx 0) (>= nx screen_w)) (neg vel_x) vel_x)",
		},
		{"nx*(vel_x - 1)", []int32{nx, vx, 1, sub, mul}, "(* nx (- vel_x 1))"},
	}
	for _, c := range cases {
		nodes, root := parse(c.toks)
		got := build(nodes, root)
		// The parser produces the real IR: its reconstruction compiles to the same WAT
		// as the hand-written expression.
		if compileExpr(got) != compileExpr(c.expected) {
			t.Errorf("%s: parser reconstructed %q, want-equivalent %q (different WAT)", c.name, got, c.expected)
		}
		if strings.TrimSpace(got) == "" {
			t.Errorf("%s: empty reconstruction", c.name)
		}
	}
}
