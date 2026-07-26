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

// astParseCell is the SELF-HOSTED SURFACE PARSER, authored in Flux: it parses a
// postfix token stream into a serialized ABSTRACT SYNTAX TREE. Same shift-reduce
// shape as the evaluator, but instead of computing a value it ALLOCATES a node and
// pushes its HANDLE onto the stack: a number token becomes a leaf node (tag 0, value);
// an operator token pops two handles and becomes an inner node (tag = op code, left,
// right). Nodes are (tag, a, b) records in the `nodes` buffer; `nodeCount` is the bump
// allocator; the stack holds node handles. At the end stack[0] is the root handle and
// `nodes` is the serialized tree — the IR, produced by a cell, not by Go.
const astParseCell = `(cell astparse (reads tokens cursor stack sp nodeCount) (writes nodes stack sp nodeCount cursor)
  (let ([tok (at tokens cursor)]
        [isop (< tok 0)]
        [op (neg tok)]
        [nc nodeCount]
        [base (* nc 3)]
        [l (at stack (- sp 2))]
        [r (at stack (- sp 1))]
        [tag (if isop op 0)]
        [a (if isop l tok)]
        [b (if isop r 0)]
        [pushPos (if isop (- sp 2) sp)])
    (write
      (store nodes base tag)
      (store nodes (+ base 1) a)
      (store nodes (+ base 2) b)
      (store stack pushPos nc)
      (nodeCount (+ nodeCount 1))
      (sp (if isop (- sp 1) (+ sp 1)))
      (cursor (+ cursor 1)))))`

func TestFluxSelfHostedSurfaceParser(t *testing.T) {
	ctx := context.Background()
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)
	cs := compiler.NewCompilerService()

	const srcBase, tokBase, stkBase, nodeBase = 0xB1000, 0xB2000, 0xB3000, 0xB4000
	const mcur, outp, acc, innum = 0xB0000, 0xB0004, 0xB0008, 0xB000C
	const acur, sp, ncount = 0xB0010, 0xB0014, 0xB0018
	const cap = 32

	lexLayout := flux.Layout{
		"src":    {Type: flux.TBuffer, Offset: srcBase, Len: cap},
		"tokens": {Type: flux.TBuffer, Offset: tokBase, Len: cap},
		"cursor": {Type: flux.TInt, Offset: mcur},
		"outp":   {Type: flux.TInt, Offset: outp},
		"acc":    {Type: flux.TInt, Offset: acc},
		"innum":  {Type: flux.TInt, Offset: innum},
	}
	astLayout := flux.Layout{
		"tokens":    {Type: flux.TBuffer, Offset: tokBase, Len: cap},
		"stack":     {Type: flux.TBuffer, Offset: stkBase, Len: cap},
		"nodes":     {Type: flux.TBuffer, Offset: nodeBase, Len: cap * 3},
		"cursor":    {Type: flux.TInt, Offset: acur},
		"sp":        {Type: flux.TInt, Offset: sp},
		"nodeCount": {Type: flux.TInt, Offset: ncount},
	}
	lexWAT, err := flux.Compile("m", mlexCell, lexLayout) // from flux_multidigit_parser_test.go
	if err != nil {
		t.Fatalf("mlex compile: %v", err)
	}
	astWAT, err := flux.Compile("m", astParseCell, astLayout)
	if err != nil {
		t.Fatalf("astparse compile: %v", err)
	}
	loadWAT(t, rm, cs, "urn:hdm:test:mlex2", lexWAT)
	loadWAT(t, rm, cs, "urn:hdm:test:astparse", astWAT)

	w := func(off uint32, v int32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(v))
		rm.sharedMem.Write(off, b[:])
	}
	r := func(off uint32) int32 { v, _ := rm.PeekU32(off); return int32(v) }

	// parseToAST runs the full self-hosted pipeline (text → tokens → AST node buffer)
	// and returns (nodes, rootHandle).
	parseToAST := func(text string) ([]int32, int32) {
		for i := 0; i < cap; i++ {
			w(srcBase+uint32(i*4), 0)
			w(tokBase+uint32(i*4), 0)
			w(stkBase+uint32(i*4), 0)
		}
		for i := 0; i < cap*3; i++ {
			w(nodeBase+uint32(i*4), 0)
		}
		for i, ch := range []byte(text) {
			w(srcBase+uint32(i*4), int32(ch))
		}
		// tokenize
		w(mcur, 0)
		w(outp, 0)
		w(acc, 0)
		w(innum, 0)
		for i := 0; i <= len(text); i++ {
			if _, _, _, err := rm.ExecuteTrampoline("h", "urn:hdm:test:mlex2", "run-tick", 0, 0); err != nil {
				t.Fatalf("mlex: %v", err)
			}
		}
		nTok := int(r(outp))
		// parse tokens into the AST node buffer
		w(acur, 0)
		w(sp, 0)
		w(ncount, 0)
		for i := 0; i < nTok; i++ {
			if _, _, _, err := rm.ExecuteTrampoline("h", "urn:hdm:test:astparse", "run-tick", 0, 0); err != nil {
				t.Fatalf("astparse: %v", err)
			}
		}
		nc := int(r(ncount))
		nodes := make([]int32, nc*3)
		for i := range nodes {
			nodes[i] = r(nodeBase + uint32(i*4))
		}
		return nodes, r(stkBase) // root handle is the last thing on the stack
	}

	// evalAST reconstructs the tree from the serialized node buffer (proving the Flux
	// cell produced a correct structure) and evaluates it — the Go side only walks a
	// tree the CELL built.
	var evalAST func(nodes []int32, h int32) int32
	evalAST = func(nodes []int32, h int32) int32 {
		tag, a, b := nodes[h*3], nodes[h*3+1], nodes[h*3+2]
		if tag == 0 {
			return a // leaf value
		}
		l, rr := evalAST(nodes, a), evalAST(nodes, b)
		switch tag {
		case 1:
			return l + rr
		case 2:
			return l - rr
		case 3:
			return l * rr
		default:
			return l / rr
		}
	}

	cases := []struct {
		text string
		want int32
	}{
		{"3 4 +", 7},
		{"3 4 + 2 *", 14},
		{"12 34 +", 46},
		{"200 50 - 3 *", 450},
		{"100 5 /", 20},
	}
	for _, c := range cases {
		nodes, root := parseToAST(c.text)
		if got := evalAST(nodes, root); got != c.want {
			t.Errorf("parse+eval(%q) = %d, want %d  (nodes=%v root=%d)", c.text, got, c.want, nodes, root)
		}
	}
}
