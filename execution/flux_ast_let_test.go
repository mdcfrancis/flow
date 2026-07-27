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

// astLetCell is the self-hosted parser extended with LET BINDINGS — a second
// namespace. Two new token classes:
//   - local reference (-399..-300, index = -tok-300): a var leaf (tag 98) naming a let local
//   - let bind (-499..-400, index = -tok-400): pops the top expression handle and
//     records a (local, expr) pair in the lbuf let buffer
//
// Together with field refs, write terminals, and the full expression vocabulary, the
// cell now parses a complete compute cell WITH intermediates:
//
//	(cell c (reads …) (writes …) (let ([t0 e0] …) (write (f e) …)))
//
// Token ranges: >=0 num · -20..-1 op · -199..-100 field · -299..-200 write ·
// -399..-300 local-ref · <=-400 let-bind.
const astLetCell = `(cell astlet (reads tokens cursor stack sp nodeCount wcount lcount nodes wbuf lbuf) (writes nodes stack sp nodeCount wbuf wcount lbuf lcount cursor)
  (let ([tok (at tokens cursor)]
        [isop (and (<= tok -1) (>= tok -20))]
        [isfield (and (<= tok -100) (>= tok -199))]
        [iswrite (and (<= tok -200) (>= tok -299))]
        [islocal (and (<= tok -300) (>= tok -399))]
        [islet (<= tok -400)]
        [isstmt (or iswrite islet)]
        [isNode (not isstmt)]
        [op (neg tok)]
        [ar (if isop (if (or (= op 16) (or (= op 17) (= op 18))) 1 (if (or (= op 19) (= op 20)) 3 2)) 0)]
        [nc nodeCount]
        [base (* nc 4)]
        [c0 (at stack (- sp ar))]
        [c1 (at stack (+ (- sp ar) 1))]
        [c2 (at stack (+ (- sp ar) 2))]
        [tag (if isop op (if isfield 99 (if islocal 98 0)))]
        [leafval (if isfield (- op 100) (if islocal (- op 300) tok))]
        [pushPos (if isop (- sp ar) sp)]
        [top (at stack (- sp 1))]
        [fieldIdx (- op 200)]
        [localIdx (- op 400)]
        [wbase (* wcount 2)]
        [lbase (* lcount 2)])
    (write
      (store nodes base (if isNode tag (at nodes base)))
      (store nodes (+ base 1) (if isNode (if isop c0 leafval) (at nodes (+ base 1))))
      (store nodes (+ base 2) (if isNode (if isop c1 0) (at nodes (+ base 2))))
      (store nodes (+ base 3) (if isNode (if isop c2 0) (at nodes (+ base 3))))
      (store stack pushPos (if isNode nc (at stack pushPos)))
      (store wbuf wbase (if iswrite fieldIdx (at wbuf wbase)))
      (store wbuf (+ wbase 1) (if iswrite top (at wbuf (+ wbase 1))))
      (store lbuf lbase (if islet localIdx (at lbuf lbase)))
      (store lbuf (+ lbase 1) (if islet top (at lbuf (+ lbase 1))))
      (nodeCount (if isNode (+ nodeCount 1) nodeCount))
      (wcount (if iswrite (+ wcount 1) wcount))
      (lcount (if islet (+ lcount 1) lcount))
      (sp (if isstmt (- sp 1) (if isop (+ (- sp ar) 1) (+ sp 1))))
      (cursor (+ cursor 1)))))`

// TestFluxParsesCellWithLets: the parser handles a full compute cell WITH let
// bindings, verified against the real flux compiler (identical WAT) on the canonical
// physics cell (nx/ny intermediates, clamp + wall-reflect, four writes).
func TestFluxParsesCellWithLets(t *testing.T) {
	ctx := context.Background()
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)
	cs := compiler.NewCompilerService()

	const tokBase, stkBase, nodeBase, wbufBase, lbufBase = 0xB2000, 0xB3000, 0xB4000, 0xB6000, 0xB7000
	const cursorOff, spOff, ncOff, wcOff, lcOff = 0xB0000, 0xB0004, 0xB0008, 0xB000C, 0xB0010
	const cap = 64
	layout := flux.Layout{
		"tokens":    {Type: flux.TBuffer, Offset: tokBase, Len: cap},
		"stack":     {Type: flux.TBuffer, Offset: stkBase, Len: cap},
		"nodes":     {Type: flux.TBuffer, Offset: nodeBase, Len: cap * 4},
		"wbuf":      {Type: flux.TBuffer, Offset: wbufBase, Len: cap},
		"lbuf":      {Type: flux.TBuffer, Offset: lbufBase, Len: cap},
		"cursor":    {Type: flux.TInt, Offset: cursorOff},
		"sp":        {Type: flux.TInt, Offset: spOff},
		"nodeCount": {Type: flux.TInt, Offset: ncOff},
		"wcount":    {Type: flux.TInt, Offset: wcOff},
		"lcount":    {Type: flux.TInt, Offset: lcOff},
	}
	wat, err := flux.Compile("m", astLetCell, layout)
	if err != nil {
		t.Fatalf("astlet compile: %v", err)
	}
	loadWAT(t, rm, cs, "urn:hdm:test:astlet", wat)

	w := func(off uint32, v int32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(v))
		rm.sharedMem.Write(off, b[:])
	}
	r := func(off uint32) int32 { v, _ := rm.PeekU32(off); return int32(v) }

	fields := []string{"ball_x", "ball_y", "vel_x", "vel_y", "screen_w", "screen_h"}
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
		case 98:
			return "t" + strconv.FormatInt(int64(x), 10) // a let local
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

	run := func(toks []int32) string {
		for i := 0; i < cap; i++ {
			w(tokBase+uint32(i*4), 0)
			w(stkBase+uint32(i*4), 0)
			w(wbufBase+uint32(i*4), 0)
			w(lbufBase+uint32(i*4), 0)
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
		w(lcOff, 0)
		for range toks {
			if _, _, _, err := rm.ExecuteTrampoline("h", "urn:hdm:test:astlet", "run-tick", 0, 0); err != nil {
				t.Fatalf("astlet: %v", err)
			}
		}
		nc := int(r(ncOff))
		nodes := make([]int32, nc*4)
		for i := range nodes {
			nodes[i] = r(nodeBase + uint32(i*4))
		}
		// lets in order
		lc := int(r(lcOff))
		body := ""
		if lc > 0 {
			body += "(let ("
			for i := 0; i < lc; i++ {
				li := r(lbufBase + uint32((i*2)*4))
				h := r(lbufBase + uint32((i*2+1)*4))
				if i > 0 {
					body += " "
				}
				body += fmt.Sprintf("[t%d %s]", li, build(nodes, h))
			}
			body += ") "
		}
		wc := int(r(wcOff))
		body += "(write"
		for i := 0; i < wc; i++ {
			fi := r(wbufBase + uint32((i*2)*4))
			h := r(wbufBase + uint32((i*2+1)*4))
			body += " (" + fields[fi] + " " + build(nodes, h) + ")"
		}
		body += ")"
		if lc > 0 {
			body += ")"
		}
		return "(cell c (reads ball_x ball_y vel_x vel_y screen_w screen_h) (writes ball_x ball_y vel_x vel_y) " + body + ")"
	}

	cellLayout := flux.Layout{
		"ball_x": {Type: flux.TInt, Offset: 0xC0000}, "ball_y": {Type: flux.TInt, Offset: 0xC0004},
		"vel_x": {Type: flux.TInt, Offset: 0xC0008}, "vel_y": {Type: flux.TInt, Offset: 0xC000C},
		"screen_w": {Type: flux.TInt, Offset: 0xC0010, ReadOnly: true}, "screen_h": {Type: flux.TInt, Offset: 0xC0014, ReadOnly: true},
	}
	compileCell := func(src string) string {
		w, err := flux.Compile("m", src, cellLayout)
		if err != nil {
			t.Fatalf("flux compile:\n%s\nerr: %v", src, err)
		}
		return w
	}

	f := func(i int) int32 { return int32(-100 - i) }
	lref := func(i int) int32 { return int32(-300 - i) }
	wr := func(i int) int32 { return int32(-200 - i) }
	lb := func(i int) int32 { return int32(-400 - i) }
	const add, sub, lt, ge, or, neg, iff, clamp = -1, -2, -6, -9, -13, -16, -19, -20
	// field idx: ball_x=0 ball_y=1 vel_x=2 vel_y=3 screen_w=4 screen_h=5
	toks := []int32{
		f(0), f(2), add, lb(0), // t0 = ball_x + vel_x
		f(1), f(3), add, lb(1), // t1 = ball_y + vel_y
		lref(0), 0, f(4), 1, sub, clamp, wr(0), // ball_x = clamp(t0, 0, screen_w-1)
		lref(1), 0, f(5), 1, sub, clamp, wr(1), // ball_y = clamp(t1, 0, screen_h-1)
		lref(0), 0, lt, lref(0), f(4), ge, or, f(2), neg, f(2), iff, wr(2), // vel_x reflect
		lref(1), 0, lt, lref(1), f(5), ge, or, f(3), neg, f(3), iff, wr(3), // vel_y reflect
	}
	got := run(toks)
	want := `(cell c (reads ball_x ball_y vel_x vel_y screen_w screen_h) (writes ball_x ball_y vel_x vel_y)
	  (let ([t0 (+ ball_x vel_x)] [t1 (+ ball_y vel_y)])
	    (write (ball_x (clamp t0 0 (- screen_w 1)))
	           (ball_y (clamp t1 0 (- screen_h 1)))
	           (vel_x (if (or (< t0 0) (>= t0 screen_w)) (neg vel_x) vel_x))
	           (vel_y (if (or (< t1 0) (>= t1 screen_h)) (neg vel_y) vel_y)))))`
	if compileCell(got) != compileCell(want) {
		t.Errorf("parsed cell-with-lets differs:\n got: %s\nwant: %s", got, want)
	}
}
