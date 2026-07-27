package execution

import (
	"context"
	"encoding/binary"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/flux"
	"github.com/mdcfrancis/flow/inference"
	"github.com/mdcfrancis/flow/storage"
)

// astFullCell extends the self-hosted parser to the FULL EXPRESSION vocabulary of the
// language: every operator, with its arity, built into a 4-wide node (tag, c0, c1,
// c2) so ternary forms (if, clamp) fit. Operand tokens (>= 0) become leaves; operator
// tokens (< 0, code = -opcode) pop `arity` handles and become an inner node. The
// stack gives arbitrary nesting for free (postfix). This is the whole expression
// grammar parsed by a Flux cell.
//
// opcodes: 1 + 2 - 3 * 4 / 5 mod  6 < 7 <= 8 > 9 >= 10 = 11 !=  12 and 13 or
//
//	14 min 15 max  16 neg 17 abs 18 not  19 if 20 clamp
const astFullCell = `(cell astfull (reads tokens cursor stack sp nodeCount) (writes nodes stack sp nodeCount cursor)
  (let ([tok (at tokens cursor)]
        [isop (< tok 0)]
        [op (neg tok)]
        [ar (if (or (= op 16) (or (= op 17) (= op 18))) 1 (if (or (= op 19) (= op 20)) 3 2))]
        [nc nodeCount]
        [base (* nc 4)]
        [c0 (at stack (- sp ar))]
        [c1 (at stack (+ (- sp ar) 1))]
        [c2 (at stack (+ (- sp ar) 2))]
        [tag (if isop op 0)]
        [pushPos (if isop (- sp ar) sp)])
    (write
      (store nodes base tag)
      (store nodes (+ base 1) (if isop c0 tok))
      (store nodes (+ base 2) (if isop c1 0))
      (store nodes (+ base 3) (if isop c2 0))
      (store stack pushPos nc)
      (nodeCount (+ nodeCount 1))
      (sp (if isop (+ (- sp ar) 1) (+ sp 1)))
      (cursor (+ cursor 1)))))`

func TestFluxFullExpressionParser(t *testing.T) {
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
	const cap = 32
	layout := flux.Layout{
		"tokens":    {Type: flux.TBuffer, Offset: tokBase, Len: cap},
		"stack":     {Type: flux.TBuffer, Offset: stkBase, Len: cap},
		"nodes":     {Type: flux.TBuffer, Offset: nodeBase, Len: cap * 4},
		"cursor":    {Type: flux.TInt, Offset: cursorOff},
		"sp":        {Type: flux.TInt, Offset: spOff},
		"nodeCount": {Type: flux.TInt, Offset: ncOff},
	}
	wat, err := flux.Compile("m", astFullCell, layout)
	if err != nil {
		t.Fatalf("astfull compile: %v", err)
	}
	loadWAT(t, rm, cs, "urn:hdm:test:astfull", wat)

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
			if _, _, _, err := rm.ExecuteTrampoline("h", "urn:hdm:test:astfull", "run-tick", 0, 0); err != nil {
				t.Fatalf("astfull: %v", err)
			}
		}
		nc := int(r(ncOff))
		nodes := make([]int32, nc*4)
		for i := range nodes {
			nodes[i] = r(nodeBase + uint32(i*4))
		}
		return nodes, r(stkBase)
	}

	// evalAST reconstructs the tree the CELL built and evaluates the full vocabulary.
	b2i := func(b bool) int32 {
		if b {
			return 1
		}
		return 0
	}
	var eval func(nodes []int32, h int32) int32
	eval = func(nodes []int32, h int32) int32 {
		tag, x, y, z := nodes[h*4], nodes[h*4+1], nodes[h*4+2], nodes[h*4+3]
		if tag == 0 {
			return x
		}
		a := eval(nodes, x)
		switch tag {
		case 16:
			return -a
		case 17:
			if a < 0 {
				return -a
			}
			return a
		case 18:
			return b2i(a == 0)
		}
		bb := eval(nodes, y)
		switch tag {
		case 1:
			return a + bb
		case 2:
			return a - bb
		case 3:
			return a * bb
		case 4:
			return a / bb
		case 5:
			return a % bb
		case 6:
			return b2i(a < bb)
		case 7:
			return b2i(a <= bb)
		case 8:
			return b2i(a > bb)
		case 9:
			return b2i(a >= bb)
		case 10:
			return b2i(a == bb)
		case 11:
			return b2i(a != bb)
		case 12:
			return b2i(a != 0 && bb != 0)
		case 13:
			return b2i(a != 0 || bb != 0)
		case 14:
			if a < bb {
				return a
			}
			return bb
		case 15:
			if a > bb {
				return a
			}
			return bb
		}
		c := eval(nodes, z)
		switch tag {
		case 19: // if
			if a != 0 {
				return bb
			}
			return c
		case 20: // clamp(x,lo,hi)
			if a < bb {
				return bb
			}
			if a > c {
				return c
			}
			return a
		}
		return 0
	}

	const (
		add, mul, mod        = -1, -3, -5
		lt, mn, mx           = -6, -14, -15
		and, or              = -12, -13
		neg, abs, iff, clamp = -16, -17, -19, -20
	)
	cases := []struct {
		name string
		toks []int32
		want int32
	}{
		{"3 4 + 2 *", []int32{3, 4, add, 2, mul}, 14},
		{"5 3 min", []int32{5, 3, mn}, 3},
		{"5 3 max", []int32{5, 3, mx}, 5},
		{"5 3 <", []int32{5, 3, lt}, 0},
		{"3 5 <", []int32{3, 5, lt}, 1},
		{"5 neg", []int32{5, neg}, -5},
		{"5 neg abs", []int32{5, neg, abs}, 5},
		{"1 0 or", []int32{1, 0, or}, 1},
		{"1 0 and", []int32{1, 0, and}, 0},
		{"7 2 3 clamp", []int32{7, 2, 3, clamp}, 3}, // 7 clamped to [2,3] = 3
		{"1 10 20 if", []int32{1, 10, 20, iff}, 10}, // cond true
		{"0 10 20 if", []int32{0, 10, 20, iff}, 20}, // cond false
		{"9 3 mod", []int32{9, 3, mod}, 0},          // 9 % 3
		{"nested if(3<5, min(8,4), 99)", []int32{3, 5, lt, 8, 4, mn, 99, iff}, 4},
	}
	for _, c := range cases {
		nodes, root := parse(c.toks)
		if got := eval(nodes, root); got != c.want {
			t.Errorf("%s = %d, want %d (nodes=%v root=%d)", c.name, got, c.want, nodes, root)
		}
	}
}
