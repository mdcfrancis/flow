package execution

import (
	"context"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/flux"
	"github.com/mdcfrancis/flow/inference"
	"github.com/mdcfrancis/flow/storage"
)

// astCellCell parses a whole COMPUTE CELL, not just an expression. It adds WRITE
// TERMINALS to the expression parser: a write token (tok <= -200, field index =
// -tok-200) pops the top expression handle and records a (field, expr) pair in the
// wbuf write buffer — the cell body's list of field assignments. Everything else
// (numbers, fields, operators) builds the expression forest as before. Stores are
// self-preserving so a node-building tick doesn't disturb the write buffer and vice
// versa. Token ranges: >=0 number · -20..-1 operator · -199..-100 field ·
// <=-200 write-to-field.
const astCellCell = `(cell astcell (reads tokens cursor stack sp nodeCount wcount nodes wbuf) (writes nodes stack sp nodeCount wbuf wcount cursor)
  (let ([tok (at tokens cursor)]
        [isop (and (<= tok -1) (>= tok -20))]
        [isfield (and (<= tok -100) (>= tok -199))]
        [iswrite (<= tok -200)]
        [isNode (not iswrite)]
        [op (neg tok)]
        [ar (if isop (if (or (= op 16) (or (= op 17) (= op 18))) 1 (if (or (= op 19) (= op 20)) 3 2)) 0)]
        [nc nodeCount]
        [base (* nc 4)]
        [c0 (at stack (- sp ar))]
        [c1 (at stack (+ (- sp ar) 1))]
        [c2 (at stack (+ (- sp ar) 2))]
        [tag (if isop op (if isfield 99 0))]
        [leafval (if isfield (- op 100) tok)]
        [pushPos (if isop (- sp ar) sp)]
        [wtop (at stack (- sp 1))]
        [fieldIdx (- op 200)]
        [wbase (* wcount 2)])
    (write
      (store nodes base (if isNode tag (at nodes base)))
      (store nodes (+ base 1) (if isNode (if isop c0 leafval) (at nodes (+ base 1))))
      (store nodes (+ base 2) (if isNode (if isop c1 0) (at nodes (+ base 2))))
      (store nodes (+ base 3) (if isNode (if isop c2 0) (at nodes (+ base 3))))
      (store stack pushPos (if isNode nc (at stack pushPos)))
      (store wbuf wbase (if iswrite fieldIdx (at wbuf wbase)))
      (store wbuf (+ wbase 1) (if iswrite wtop (at wbuf (+ wbase 1))))
      (nodeCount (if isNode (+ nodeCount 1) nodeCount))
      (wcount (if iswrite (+ wcount 1) wcount))
      (sp (if iswrite (- sp 1) (if isop (+ (- sp ar) 1) (+ sp 1))))
      (cursor (+ cursor 1)))))`

// TestFluxParsesFullComputeCell: a Flux cell parses a whole compute cell (multiple
// field writes, each an expression over fields) into a node buffer + write buffer;
// Go reconstructs the cell source; and it compiles through the real flux compiler to
// the SAME WAT as the hand-written cell. The self-hosted parser now covers the full
// compute-cell grammar.
func TestFluxParsesFullComputeCell(t *testing.T) {
	ctx := context.Background()
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)
	cs := compiler.NewCompilerService()

	const tokBase, stkBase, nodeBase, wbufBase = 0xB2000, 0xB3000, 0xB4000, 0xB6000
	const cursorOff, spOff, ncOff, wcOff = 0xB0000, 0xB0004, 0xB0008, 0xB000C
	const cap = 48
	layout := flux.Layout{
		"tokens":    {Type: flux.TBuffer, Offset: tokBase, Len: cap},
		"stack":     {Type: flux.TBuffer, Offset: stkBase, Len: cap},
		"nodes":     {Type: flux.TBuffer, Offset: nodeBase, Len: cap * 4},
		"wbuf":      {Type: flux.TBuffer, Offset: wbufBase, Len: cap * 2},
		"cursor":    {Type: flux.TInt, Offset: cursorOff},
		"sp":        {Type: flux.TInt, Offset: spOff},
		"nodeCount": {Type: flux.TInt, Offset: ncOff},
		"wcount":    {Type: flux.TInt, Offset: wcOff},
	}
	wat, err := flux.Compile("m", astCellCell, layout)
	if err != nil {
		t.Fatalf("astcell compile: %v", err)
	}
	loadWAT(t, rm, cs, "urn:hdm:test:astcell", wat)

	w := func(off uint32, v int32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(v))
		rm.sharedMem.Write(off, b[:])
	}
	r := func(off uint32) int32 { v, _ := rm.PeekU32(off); return int32(v) }

	fields := []string{"ball_x", "ball_y", "vel_x", "vel_y", "screen_w"}
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

	// parseCell runs the parser and reconstructs a full cell source from the node +
	// write buffers.
	parseCell := func(toks []int32, reads, writes []string) string {
		for i := 0; i < cap; i++ {
			w(tokBase+uint32(i*4), 0)
			w(stkBase+uint32(i*4), 0)
			w(wbufBase+uint32(i*4), 0)
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
		w(wcOff, 0)
		for range toks {
			if _, _, _, err := rm.ExecuteTrampoline("h", "urn:hdm:test:astcell", "run-tick", 0, 0); err != nil {
				t.Fatalf("astcell: %v", err)
			}
		}
		nc := int(r(ncOff))
		nodes := make([]int32, nc*4)
		for i := range nodes {
			nodes[i] = r(nodeBase + uint32(i*4))
		}
		wc := int(r(wcOff))
		var pairs []string
		for i := 0; i < wc; i++ {
			fi := r(wbufBase + uint32((i*2)*4))
			h := r(wbufBase + uint32((i*2+1)*4))
			pairs = append(pairs, "("+fields[fi]+" "+build(nodes, h)+")")
		}
		body := "(write"
		for _, p := range pairs {
			body += " " + p
		}
		body += ")"
		return fmt.Sprintf("(cell c (reads %s) (writes %s) %s)",
			joinSp(reads), joinSp(writes), body)
	}

	cellLayout := flux.Layout{
		"ball_x": {Type: flux.TInt, Offset: 0xC0000}, "ball_y": {Type: flux.TInt, Offset: 0xC0004},
		"vel_x": {Type: flux.TInt, Offset: 0xC0008}, "vel_y": {Type: flux.TInt, Offset: 0xC000C},
		"screen_w": {Type: flux.TInt, Offset: 0xC0010, ReadOnly: true},
	}
	compileCell := func(src string) string {
		w, err := flux.Compile("m", src, cellLayout)
		if err != nil {
			t.Fatalf("flux compile %q: %v", src, err)
		}
		return w
	}

	// field-ref tokens = -100-index; write tokens = -200-index.
	f := func(i int) int32 { return int32(-100 - i) }  // ball_x=0 ball_y=1 vel_x=2 vel_y=3 screen_w=4
	wr := func(i int) int32 { return int32(-200 - i) } // write to field i
	const add, lt, ge, or, neg, iff = -1, -6, -9, -13, -16, -19

	// A real physics cell: integrate + reflect, four field writes.
	//   ball_x := ball_x + vel_x
	//   vel_x  := if (or (< ball_x 0) (>= ball_x screen_w)) (neg vel_x) vel_x
	toks := []int32{
		f(0), f(2), add, wr(0), // ball_x + vel_x -> ball_x
		f(0), 0, lt, f(0), f(4), ge, or, f(2), neg, f(2), iff, wr(2), // reflect -> vel_x
	}
	got := parseCell(toks, []string{"ball_x", "vel_x", "screen_w"}, []string{"ball_x", "vel_x"})
	want := `(cell c (reads ball_x vel_x screen_w) (writes ball_x vel_x)
	  (write (ball_x (+ ball_x vel_x))
	         (vel_x (if (or (< ball_x 0) (>= ball_x screen_w)) (neg vel_x) vel_x))))`
	if compileCell(got) != compileCell(want) {
		t.Errorf("parsed compute cell differs:\n got: %s\nwant: %s", got, want)
	}
}

func joinSp(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}
