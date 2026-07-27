package execution

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/flux"
)

// The self-hosted AST parser: the FULL compute-cell grammar parsed by a live Flux cell.
// Where flux-lexer/flux-parser handle RPN arithmetic, this cell parses the language's
// own cell bodies — the whole expression vocabulary plus field refs, let bindings, and
// write terminals — into AST records, and Go reconstructs those records into real Flux
// IR. So the AUTHORING parse path (surface → cell) is now (partly) self-hosted: the
// structure is parsed by a cell; Go supplies only identifier resolution (field names →
// indices, layout-dependent and thus naturally Go-side) and the final reconstruction.
//
// Token encoding (fed to the cell): >=0 num · -20..-1 op · -199..-100 field ·
// -299..-200 write · -399..-300 local-ref · <=-400 let-bind. The cell emits 4-wide AST
// nodes (tag,c0,c1,c2; tag 0=num 99=field 98=local 1-20=op), a wbuf of (fieldIdx,expr)
// write pairs, and an lbuf of (localIdx,expr) let pairs.
const SelfHostASTParserURN = "urn:hdm:sys:flux-ast-parser"

// SelfHostASTCap is the working-buffer capacity (i32 words) for the AST parser.
const SelfHostASTCap = 64

// Dedicated AST-parser region, above the RPN parser region (0xC0000..0xC3400) and
// below windowBase; the cell makes no host calls so nothing else touches it mid-parse.
// Exported so the acceptance suites (appgen) pin behavior at the exact same offsets.
const (
	SelfHostASTCursor = 0xD0000
	SelfHostASTSp     = 0xD0004
	SelfHostASTNC     = 0xD0008
	SelfHostASTWC     = 0xD000C
	SelfHostASTLC     = 0xD0010
	SelfHostASTTok    = 0xD1000
	SelfHostASTStk    = 0xD2000
	SelfHostASTNodes  = 0xD3000 // cap*4 words
	SelfHostASTWbuf   = 0xD5000
	SelfHostASTLbuf   = 0xD6000
)

// SelfHostASTCellFlux is the full-grammar parser (identical to the test-proven `astlet`
// cell): per tick it consumes one token, shifting expression nodes onto the stack and
// recording write/let statements, so over the token stream it builds the whole cell.
const SelfHostASTCellFlux = `(cell astlet (reads tokens cursor stack sp nodeCount wcount lcount nodes wbuf lbuf) (writes nodes stack sp nodeCount wbuf wcount lbuf lcount cursor)
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

func selfHostASTLayout() flux.Layout {
	return flux.Layout{
		"tokens":    {Type: flux.TBuffer, Offset: SelfHostASTTok, Len: SelfHostASTCap},
		"stack":     {Type: flux.TBuffer, Offset: SelfHostASTStk, Len: SelfHostASTCap},
		"nodes":     {Type: flux.TBuffer, Offset: SelfHostASTNodes, Len: SelfHostASTCap * 4},
		"wbuf":      {Type: flux.TBuffer, Offset: SelfHostASTWbuf, Len: SelfHostASTCap},
		"lbuf":      {Type: flux.TBuffer, Offset: SelfHostASTLbuf, Len: SelfHostASTCap},
		"cursor":    {Type: flux.TInt, Offset: SelfHostASTCursor},
		"sp":        {Type: flux.TInt, Offset: SelfHostASTSp},
		"nodeCount": {Type: flux.TInt, Offset: SelfHostASTNC},
		"wcount":    {Type: flux.TInt, Offset: SelfHostASTWC},
		"lcount":    {Type: flux.TInt, Offset: SelfHostASTLC},
	}
}

// CompileSelfHostASTParser lowers the AST parser cell to WAT with the Go compiler.
func CompileSelfHostASTParser() (string, error) {
	wat, err := flux.Compile("m", SelfHostASTCellFlux, selfHostASTLayout())
	if err != nil {
		return "", fmt.Errorf("self-host AST parser: %w", err)
	}
	return wat, nil
}

// SelfHostASTParserCell returns the AST parser as a seedable system cell.
func SelfHostASTParserCell() (Combinator, error) {
	wat, err := CompileSelfHostASTParser()
	if err != nil {
		return Combinator{}, err
	}
	return Combinator{URN: SelfHostASTParserURN, WAT: wat,
		Intent: "Self-hosted Flux AST parser: parse a compute-cell token stream (fields, ops, lets, writes) into AST node records — the authoring parse path as a cell."}, nil
}

// ASTResult is the parsed structure read back out of the cell's buffers: the AST node
// records (4 words each), plus the ordered write and let statement pairs.
type ASTResult struct {
	Nodes []int32 // nc*4 words: [tag,c0,c1,c2] per node
	Wbuf  []int32 // wc*2 words: [fieldIdx, exprHandle] per write
	Lbuf  []int32 // lc*2 words: [localIdx, exprHandle] per let
	NC    int
	WC    int
	LC    int
}

var (
	selfHostASTOnce      sync.Once
	selfHostASTBytecode  []byte
	selfHostASTCompileErr error
)

func compileSelfHostASTBytecode() {
	wat, err := CompileSelfHostASTParser()
	if err != nil {
		selfHostASTCompileErr = err
		return
	}
	art, err := compiler.NewCompilerService().CompileGenotype(wat)
	if err != nil || !art.SyntaxPassed {
		selfHostASTCompileErr = fmt.Errorf("self-host AST parser wasm: %v", err)
		return
	}
	selfHostASTBytecode = art.Bytecode
}

// RunSelfHostASTParse drives the live AST parser cell over a token stream and reads the
// resulting AST/write/let records back out. Holds the runtime lock across the parse
// (race-free with the frame loop); writes only its dedicated 0xD0000+ region.
func (rm *RuntimeManager) RunSelfHostASTParse(toks []int32) (*ASTResult, error) {
	selfHostASTOnce.Do(compileSelfHostASTBytecode)
	if selfHostASTCompileErr != nil {
		return nil, selfHostASTCompileErr
	}
	if len(toks) > SelfHostASTCap {
		return nil, fmt.Errorf("token stream too long: %d > cap %d", len(toks), SelfHostASTCap)
	}
	if err := rm.LoadCell(SelfHostASTParserURN, selfHostASTBytecode); err != nil {
		return nil, fmt.Errorf("load AST parser: %w", err)
	}

	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.sharedMem == nil {
		return nil, fmt.Errorf("shared memory unavailable")
	}
	wr := func(off uint32, v int32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(v))
		rm.sharedMem.Write(off, b[:])
	}
	rd := func(off uint32) int32 {
		if b, ok := rm.sharedMem.Read(off, 4); ok {
			return int32(binary.LittleEndian.Uint32(b))
		}
		return 0
	}
	// Clear working buffers.
	for i := uint32(0); i < SelfHostASTCap; i++ {
		wr(SelfHostASTTok+i*4, 0)
		wr(SelfHostASTStk+i*4, 0)
		wr(SelfHostASTWbuf+i*4, 0)
		wr(SelfHostASTLbuf+i*4, 0)
	}
	for i := uint32(0); i < SelfHostASTCap*4; i++ {
		wr(SelfHostASTNodes+i*4, 0)
	}
	for i, tk := range toks {
		wr(SelfHostASTTok+uint32(i)*4, tk)
	}
	wr(SelfHostASTCursor, 0)
	wr(SelfHostASTSp, 0)
	wr(SelfHostASTNC, 0)
	wr(SelfHostASTWC, 0)
	wr(SelfHostASTLC, 0)
	rm.reasoningNanos = 0
	for range toks {
		if _, _, _, err := rm.execTrampoline(SelfHostASTParserURN, SelfHostASTParserURN, "run-tick", 0, 0); err != nil {
			return nil, fmt.Errorf("ast tick: %w", err)
		}
	}
	nc, wc, lc := int(rd(SelfHostASTNC)), int(rd(SelfHostASTWC)), int(rd(SelfHostASTLC))
	res := &ASTResult{NC: nc, WC: wc, LC: lc,
		Nodes: make([]int32, nc*4), Wbuf: make([]int32, wc*2), Lbuf: make([]int32, lc*2)}
	for i := range res.Nodes {
		res.Nodes[i] = rd(SelfHostASTNodes + uint32(i)*4)
	}
	for i := range res.Wbuf {
		res.Wbuf[i] = rd(SelfHostASTWbuf + uint32(i)*4)
	}
	for i := range res.Lbuf {
		res.Lbuf[i] = rd(SelfHostASTLbuf + uint32(i)*4)
	}
	return res, nil
}

var astOpName = map[int32]string{1: "+", 2: "-", 3: "*", 4: "/", 5: "mod", 6: "<", 7: "<=",
	8: ">", 9: ">=", 10: "=", 11: "!=", 12: "and", 13: "or", 14: "min", 15: "max",
	16: "neg", 17: "abs", 18: "not", 19: "if", 20: "clamp"}

func astArity(op int32) int {
	if op >= 16 && op <= 18 {
		return 1
	}
	if op == 19 || op == 20 {
		return 3
	}
	return 2
}

// ReconstructCell turns the parser's AST/write/let records back into Flux cell source.
// This is the Go-side reconstruction: the cell parsed the STRUCTURE; Go names the
// leaves (field/local identifiers) and reassembles the s-expr. `fields` maps a field
// index to its name; `reads`/`writes` are the cell header's declared ports.
func ReconstructCell(res *ASTResult, name string, fields, reads, writes []string) (string, error) {
	var build func(h int32) (string, error)
	build = func(h int32) (string, error) {
		if h < 0 || int(h)*4+3 >= len(res.Nodes)+3 || int(h) >= res.NC {
			return "", fmt.Errorf("node handle %d out of range (nc=%d)", h, res.NC)
		}
		tag, x, y, z := res.Nodes[h*4], res.Nodes[h*4+1], res.Nodes[h*4+2], res.Nodes[h*4+3]
		switch tag {
		case 0:
			return strconv.FormatInt(int64(x), 10), nil
		case 99:
			if int(x) >= len(fields) {
				return "", fmt.Errorf("field index %d out of range", x)
			}
			return fields[x], nil
		case 98:
			return "t" + strconv.FormatInt(int64(x), 10), nil
		}
		nm, ok := astOpName[tag]
		if !ok {
			return "", fmt.Errorf("unknown op tag %d", tag)
		}
		a, err := build(x)
		if err != nil {
			return "", err
		}
		switch astArity(tag) {
		case 1:
			return "(" + nm + " " + a + ")", nil
		case 3:
			b, err := build(y)
			if err != nil {
				return "", err
			}
			c, err := build(z)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("(%s %s %s %s)", nm, a, b, c), nil
		default:
			b, err := build(y)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("(%s %s %s)", nm, a, b), nil
		}
	}
	body := ""
	if res.LC > 0 {
		body += "(let ("
		for i := 0; i < res.LC; i++ {
			li, h := res.Lbuf[i*2], res.Lbuf[i*2+1]
			e, err := build(h)
			if err != nil {
				return "", err
			}
			if i > 0 {
				body += " "
			}
			body += fmt.Sprintf("[t%d %s]", li, e)
		}
		body += ") "
	}
	body += "(write"
	for i := 0; i < res.WC; i++ {
		fi, h := res.Wbuf[i*2], res.Wbuf[i*2+1]
		if int(fi) >= len(fields) {
			return "", fmt.Errorf("write field index %d out of range", fi)
		}
		e, err := build(h)
		if err != nil {
			return "", err
		}
		body += " (" + fields[fi] + " " + e + ")"
	}
	body += ")"
	if res.LC > 0 {
		body += ")"
	}
	return fmt.Sprintf("(cell %s (reads %s) (writes %s) %s)", name, strings.Join(reads, " "), strings.Join(writes, " "), body), nil
}

// SelfHostASTSelfCheck parses the canonical physics cell (lets + clamp + wall-reflect,
// four writes) THROUGH the live AST parser cell, reconstructs it, and verifies it
// compiles byte-identical to the same cell compiled directly by the Go parser — a
// boot-time proof that the authoring parse path is self-hosted AND faithful. Returns
// the reconstructed source (for logging/inspection) and an error if it diverges.
func (rm *RuntimeManager) SelfHostASTSelfCheck() (string, error) {
	f := func(i int) int32 { return int32(-100 - i) }
	lref := func(i int) int32 { return int32(-300 - i) }
	wr := func(i int) int32 { return int32(-200 - i) }
	lb := func(i int) int32 { return int32(-400 - i) }
	const add, sub, lt, ge, or, neg, iff, clamp = -1, -2, -6, -9, -13, -16, -19, -20
	toks := []int32{
		f(0), f(2), add, lb(0),
		f(1), f(3), add, lb(1),
		lref(0), 0, f(4), 1, sub, clamp, wr(0),
		lref(1), 0, f(5), 1, sub, clamp, wr(1),
		lref(0), 0, lt, lref(0), f(4), ge, or, f(2), neg, f(2), iff, wr(2),
		lref(1), 0, lt, lref(1), f(5), ge, or, f(3), neg, f(3), iff, wr(3),
	}
	res, err := rm.RunSelfHostASTParse(toks)
	if err != nil {
		return "", err
	}
	fields := []string{"ball_x", "ball_y", "vel_x", "vel_y", "screen_w", "screen_h"}
	got, err := ReconstructCell(res, "c", fields, fields, []string{"ball_x", "ball_y", "vel_x", "vel_y"})
	if err != nil {
		return "", err
	}
	want := `(cell c (reads ball_x ball_y vel_x vel_y screen_w screen_h) (writes ball_x ball_y vel_x vel_y)
	  (let ([t0 (+ ball_x vel_x)] [t1 (+ ball_y vel_y)])
	    (write (ball_x (clamp t0 0 (- screen_w 1)))
	           (ball_y (clamp t1 0 (- screen_h 1)))
	           (vel_x (if (or (< t0 0) (>= t0 screen_w)) (neg vel_x) vel_x))
	           (vel_y (if (or (< t1 0) (>= t1 screen_h)) (neg vel_y) vel_y)))))`
	layout := flux.Layout{
		"ball_x": {Type: flux.TInt, Offset: 0xC0000}, "ball_y": {Type: flux.TInt, Offset: 0xC0004},
		"vel_x": {Type: flux.TInt, Offset: 0xC0008}, "vel_y": {Type: flux.TInt, Offset: 0xC000C},
		"screen_w": {Type: flux.TInt, Offset: 0xC0010, ReadOnly: true}, "screen_h": {Type: flux.TInt, Offset: 0xC0014, ReadOnly: true},
	}
	gotWAT, err := flux.Compile("m", got, layout)
	if err != nil {
		return got, fmt.Errorf("reconstructed cell did not compile: %w", err)
	}
	wantWAT, err := flux.Compile("m", want, layout)
	if err != nil {
		return got, fmt.Errorf("canonical cell did not compile: %w", err)
	}
	if gotWAT != wantWAT {
		return got, fmt.Errorf("self-hosted parse differs from the Go compiler")
	}
	return got, nil
}
