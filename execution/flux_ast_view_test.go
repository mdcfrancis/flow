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

// astDrawCell parses a VIEW CELL — the draw terminal. New token classes:
//   - color literal (-699..-600, index = -tok-600): a COLOR leaf (tag 97) whose value
//     is looked up in the seeded `colors` table (colors are arbitrary 32-bit RGBA, so
//     they can't be encoded inline — they live in a table, referenced by index).
//   - draw prim (<=-700, kind = -tok-700: 0 circle, 1 rect, 2 line): pops its args
//     (4 for circle, 5 for rect/line — geometry ints then the color) and records them
//     in the `dbuf` draw buffer (6-wide: kind + up to five arg handles).
//
// With this, plus expressions/fields, the parser covers view cells too — the whole
// surface. Ranges: >=0 num · -20..-1 op · -199..-100 field · -699..-600 color ·
// <=-700 draw-prim.
const astDrawCell = `(cell astdraw (reads tokens cursor stack sp nodeCount dcount nodes dbuf colors) (writes nodes stack sp nodeCount dbuf dcount cursor)
  (let ([tok (at tokens cursor)]
        [isop (and (<= tok -1) (>= tok -20))]
        [isfield (and (<= tok -100) (>= tok -199))]
        [iscolor (and (<= tok -600) (>= tok -699))]
        [isdraw (<= tok -700)]
        [isNode (not isdraw)]
        [op (neg tok)]
        [ar (if isop (if (or (= op 16) (or (= op 17) (= op 18))) 1 (if (or (= op 19) (= op 20)) 3 2)) 0)]
        [nc nodeCount]
        [base (* nc 4)]
        [c0 (at stack (- sp ar))]
        [c1 (at stack (+ (- sp ar) 1))]
        [c2 (at stack (+ (- sp ar) 2))]
        [colorVal (at colors (- op 600))]
        [tag (if isop op (if isfield 99 (if iscolor 97 0)))]
        [leafval (if isfield (- op 100) (if iscolor colorVal tok))]
        [pushPos (if isop (- sp ar) sp)]
        [primKind (- op 700)]
        [darity (if (= primKind 0) 4 5)]
        [d0 (at stack (- sp darity))]
        [d1 (at stack (+ (- sp darity) 1))]
        [d2 (at stack (+ (- sp darity) 2))]
        [d3 (at stack (+ (- sp darity) 3))]
        [d4 (at stack (+ (- sp darity) 4))]
        [dbase (* dcount 6)])
    (write
      (store nodes base (if isNode tag (at nodes base)))
      (store nodes (+ base 1) (if isNode (if isop c0 leafval) (at nodes (+ base 1))))
      (store nodes (+ base 2) (if isNode (if isop c1 0) (at nodes (+ base 2))))
      (store nodes (+ base 3) (if isNode (if isop c2 0) (at nodes (+ base 3))))
      (store stack pushPos (if isNode nc (at stack pushPos)))
      (store dbuf dbase (if isdraw primKind (at dbuf dbase)))
      (store dbuf (+ dbase 1) (if isdraw d0 (at dbuf (+ dbase 1))))
      (store dbuf (+ dbase 2) (if isdraw d1 (at dbuf (+ dbase 2))))
      (store dbuf (+ dbase 3) (if isdraw d2 (at dbuf (+ dbase 3))))
      (store dbuf (+ dbase 4) (if isdraw d3 (at dbuf (+ dbase 4))))
      (store dbuf (+ dbase 5) (if isdraw d4 (at dbuf (+ dbase 5))))
      (nodeCount (if isNode (+ nodeCount 1) nodeCount))
      (dcount (if isdraw (+ dcount 1) dcount))
      (sp (if isdraw (- sp darity) (if isop (+ (- sp ar) 1) (+ sp 1))))
      (cursor (+ cursor 1)))))`

// TestFluxParsesViewCell: the parser handles a view cell (draw terminal + color
// literals), verified against the real flux compiler (identical WAT) on a renderer
// that draws a circle and a rect at computed positions.
func TestFluxParsesViewCell(t *testing.T) {
	ctx := context.Background()
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)
	cs := compiler.NewCompilerService()

	const tokBase, stkBase, nodeBase, dbufBase, colBase = 0xB2000, 0xB3000, 0xB4000, 0xB6000, 0xB8000
	const cursorOff, spOff, ncOff, dcOff = 0xB0000, 0xB0004, 0xB0008, 0xB000C
	const cap = 48
	layout := flux.Layout{
		"tokens":    {Type: flux.TBuffer, Offset: tokBase, Len: cap},
		"stack":     {Type: flux.TBuffer, Offset: stkBase, Len: cap},
		"nodes":     {Type: flux.TBuffer, Offset: nodeBase, Len: cap * 4},
		"dbuf":      {Type: flux.TBuffer, Offset: dbufBase, Len: cap * 6},
		"colors":    {Type: flux.TBuffer, Offset: colBase, Len: cap},
		"cursor":    {Type: flux.TInt, Offset: cursorOff},
		"sp":        {Type: flux.TInt, Offset: spOff},
		"nodeCount": {Type: flux.TInt, Offset: ncOff},
		"dcount":    {Type: flux.TInt, Offset: dcOff},
	}
	wat, err := flux.Compile("m", astDrawCell, layout)
	if err != nil {
		t.Fatalf("astdraw compile: %v", err)
	}
	loadWAT(t, rm, cs, "urn:hdm:test:astdraw", wat)

	w := func(off uint32, v int32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(v))
		rm.sharedMem.Write(off, b[:])
	}
	r := func(off uint32) int32 { v, _ := rm.PeekU32(off); return int32(v) }

	fields := []string{"ball_x", "ball_y"}
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
		case 97:
			return fmt.Sprintf("#x%08X", uint32(x)) // a color literal
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
	primName := []string{"circle", "rect", "line"}
	primArity := []int{4, 5, 5}

	run := func(toks []int32, colors []uint32) string {
		for i := 0; i < cap; i++ {
			w(tokBase+uint32(i*4), 0)
			w(stkBase+uint32(i*4), 0)
			w(dbufBase+uint32(i*4), 0)
			w(colBase+uint32(i*4), 0)
		}
		for i := 0; i < cap*4; i++ {
			w(nodeBase+uint32(i*4), 0)
		}
		for i := 0; i < cap*6; i++ {
			w(dbufBase+uint32(i*4), 0)
		}
		for i, tk := range toks {
			w(tokBase+uint32(i*4), tk)
		}
		for i, c := range colors {
			w(colBase+uint32(i*4), int32(c))
		}
		w(cursorOff, 0)
		w(spOff, 0)
		w(ncOff, 0)
		w(dcOff, 0)
		for range toks {
			if _, _, _, err := rm.ExecuteTrampoline("h", "urn:hdm:test:astdraw", "run-tick", 0, 0); err != nil {
				t.Fatalf("astdraw: %v", err)
			}
		}
		nc := int(r(ncOff))
		nodes := make([]int32, nc*4)
		for i := range nodes {
			nodes[i] = r(nodeBase + uint32(i*4))
		}
		dc := int(r(dcOff))
		body := "(draw"
		for d := 0; d < dc; d++ {
			dbase := d * 6
			kind := r(dbufBase + uint32(dbase*4))
			body += " (" + primName[kind]
			for k := 0; k < primArity[kind]; k++ {
				h := r(dbufBase + uint32((dbase+1+k)*4))
				body += " " + build(nodes, h)
			}
			body += ")"
		}
		body += ")"
		return "(cell c (reads ball_x ball_y) " + body + ")"
	}

	cellLayout := flux.Layout{
		"ball_x": {Type: flux.TInt, Offset: 0xC0000},
		"ball_y": {Type: flux.TInt, Offset: 0xC0004},
	}
	compileCell := func(src string) string {
		w, err := flux.Compile("m", src, cellLayout)
		if err != nil {
			t.Fatalf("flux compile:\n%s\nerr: %v", src, err)
		}
		return w
	}

	f := func(i int) int32 { return int32(-100 - i) }
	col := func(i int) int32 { return int32(-600 - i) }
	const circle, rect, add = -700, -701, -1
	// (draw (circle ball_x ball_y 8 #xFFCC33FF)
	//       (rect 0 0 (+ ball_x 4) 20 #x00FF00FF))
	toks := []int32{
		f(0), f(1), 8, col(0), circle,
		0, 0, f(0), 4, add, 20, col(1), rect,
	}
	colors := []uint32{0xFFCC33FF, 0x00FF00FF}
	got := run(toks, colors)
	want := `(cell c (reads ball_x ball_y)
	  (draw (circle ball_x ball_y 8 #xFFCC33FF)
	        (rect 0 0 (+ ball_x 4) 20 #x00FF00FF)))`
	if compileCell(got) != compileCell(want) {
		t.Errorf("parsed view cell differs:\n got: %s\nwant: %s", got, want)
	}
}
