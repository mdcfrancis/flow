package compiler

// Package compiler implements the HDM recursive syntax sieve: a native,
// zero-dependency WebAssembly Text (WAT) assembler. It parses the standard
// WAT grammar (both folded S-expression and linear stack-machine forms),
// resolves symbolic names, and emits a spec-compliant WebAssembly binary
// module that the wazero JIT can compile directly.
//
// Supported surface: WebAssembly MVP + multi-value + sign-extension. Tables,
// call_indirect, SIMD, reference types beyond funcref/externref, and the GC/
// bulk-memory proposals are intentionally unsupported and reported as precise
// diagnostic errors rather than mis-encoded.

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// CompilationArtifact holds the output of a compile pass. The diagnostic
// fields feed the recursive sieve feedback protocol.
type CompilationArtifact struct {
	Bytecode     []byte
	SizeInBytes  uint32
	SyntaxPassed bool
	ErrorContext string
	ErrorLine    int
}

// MemoryGuard bounds the linear-memory region a cell may write to. When set on
// a CompilerService, the assembler injects a clamp-or-trap check before every
// store instruction, isolating a tenant's memory without a VM boundary.
type MemoryGuard struct {
	Lower uint32
	Upper uint32
}

// CompilerService provides structural validation and WAT-to-WASM assembly.
type CompilerService struct {
	Guard *MemoryGuard // when non-nil, injects store bounds checks
}

// NewCompilerService returns a stateless compiler service.
func NewCompilerService() *CompilerService {
	return &CompilerService{}
}

// NewGuardedCompilerService returns a compiler that injects pointer-bounds
// checks on every store, trapping writes outside [lower, upper).
func NewGuardedCompilerService(lower, upper uint32) *CompilerService {
	return &CompilerService{Guard: &MemoryGuard{Lower: lower, Upper: upper}}
}

// CompileGenotype assembles WAT source text into a validated WASM binary.
// On failure it returns a populated artifact (SyntaxPassed=false, with the
// diagnostic line/context set) alongside the error, so callers can either
// check err or drive the recursive sieve loop off the artifact.
func (cs *CompilerService) CompileGenotype(watSource string) (*CompilationArtifact, error) {
	if len(strings.TrimSpace(watSource)) == 0 {
		return &CompilationArtifact{
			SyntaxPassed: false,
			ErrorContext: "empty source stream detected",
			ErrorLine:    0,
		}, fmt.Errorf("compilation failed: empty source stream")
	}

	binary, err := assemble(watSource, cs.Guard)
	if err != nil {
		line := 0
		if ce, ok := err.(*compileError); ok {
			line = ce.line
		}
		return &CompilationArtifact{
			SyntaxPassed: false,
			ErrorContext: err.Error(),
			ErrorLine:    line,
		}, fmt.Errorf("compilation failed: %w", err)
	}

	return &CompilationArtifact{
		Bytecode:     binary,
		SizeInBytes:  uint32(len(binary)),
		SyntaxPassed: true,
	}, nil
}

// compileError carries a source line for diagnostic feedback.
type compileError struct {
	line int
	msg  string
}

func (e *compileError) Error() string { return e.msg }

func errf(line int, format string, args ...interface{}) *compileError {
	return &compileError{line: line, msg: fmt.Sprintf(format, args...)}
}

// CountImportCalls reports how many times the imported function (module, name)
// is invoked across all function bodies in the WAT source, matching both the
// symbolic `$name` and the numeric func index, in folded and linear forms. It
// is used to enforce structural invariants — e.g. that an optimized candidate
// does not drop an effectful host call it is required to preserve.
func CountImportCalls(watSource, module, name string) (int, error) {
	toks, err := tokenize(watSource)
	if err != nil {
		return 0, err
	}
	// Locate the target func import: its internal $name and func-space index
	// (only func imports occupy the func index space, in source order).
	internal, funcIdx, fiCounter := "", -1, 0
	for i := 0; i+5 < len(toks); i++ {
		if toks[i].kind == tLParen && toks[i+1].kind == tAtom && toks[i+1].text == "import" &&
			toks[i+2].kind == tString && toks[i+3].kind == tString &&
			toks[i+4].kind == tLParen && toks[i+5].kind == tAtom && toks[i+5].text == "func" {
			this := ""
			if i+6 < len(toks) && toks[i+6].kind == tAtom && strings.HasPrefix(toks[i+6].text, "$") {
				this = toks[i+6].text
			}
			if toks[i+2].text == module && toks[i+3].text == name {
				internal, funcIdx = this, fiCounter
			}
			fiCounter++
		}
	}
	if funcIdx < 0 {
		return 0, nil // not imported at all
	}
	idxStr := strconv.Itoa(funcIdx)
	count := 0
	for i := 0; i+1 < len(toks); i++ {
		if toks[i].kind == tAtom && toks[i].text == "call" && toks[i+1].kind == tAtom {
			a := toks[i+1].text
			if (internal != "" && a == internal) || a == idxStr {
				count++
			}
		}
	}
	return count, nil
}

// ---------------------------------------------------------------------------
// Tokenizer
// ---------------------------------------------------------------------------

type tokKind int

const (
	tLParen tokKind = iota
	tRParen
	tString
	tAtom
	tEOF
)

type token struct {
	kind  tokKind
	text  string // atom text, or raw string contents (for diagnostics)
	bytes []byte // decoded bytes for string tokens
	line  int
	col   int
}

// idchar reports whether c is a WAT identifier/keyword character.
func idchar(c byte) bool {
	if c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
		return true
	}
	switch c {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '/', ':', '<',
		'=', '>', '?', '@', '\\', '^', '_', '`', '|', '~':
		return true
	}
	return false
}

func tokenize(src string) ([]token, error) {
	var toks []token
	line, col := 1, 1
	i, n := 0, len(src)

	adv := func(k int) {
		for j := 0; j < k; j++ {
			if src[i] == '\n' {
				line++
				col = 1
			} else {
				col++
			}
			i++
		}
	}

	for i < n {
		c := src[i]

		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			adv(1)
			continue
		}

		// Block comment (; ... ;) with nesting.
		if c == '(' && i+1 < n && src[i+1] == ';' {
			depth := 1
			adv(2)
			for i < n && depth > 0 {
				if i+1 < n && src[i] == '(' && src[i+1] == ';' {
					depth++
					adv(2)
				} else if i+1 < n && src[i] == ';' && src[i+1] == ')' {
					depth--
					adv(2)
				} else {
					adv(1)
				}
			}
			continue
		}

		// Line comment ;; ...
		if c == ';' && i+1 < n && src[i+1] == ';' {
			for i < n && src[i] != '\n' {
				adv(1)
			}
			continue
		}

		if c == '(' {
			toks = append(toks, token{kind: tLParen, text: "(", line: line, col: col})
			adv(1)
			continue
		}
		if c == ')' {
			toks = append(toks, token{kind: tRParen, text: ")", line: line, col: col})
			adv(1)
			continue
		}

		if c == '"' {
			startLine, startCol := line, col
			adv(1) // opening quote
			var buf []byte
			for i < n && src[i] != '"' {
				if src[i] == '\\' {
					if i+1 >= n {
						return nil, errf(startLine, "unterminated escape in string at line %d", startLine)
					}
					e := src[i+1]
					switch e {
					case 'n':
						buf = append(buf, '\n')
						adv(2)
					case 't':
						buf = append(buf, '\t')
						adv(2)
					case 'r':
						buf = append(buf, '\r')
						adv(2)
					case '"':
						buf = append(buf, '"')
						adv(2)
					case '\'':
						buf = append(buf, '\'')
						adv(2)
					case '\\':
						buf = append(buf, '\\')
						adv(2)
					case 'u':
						// \u{XXXX}
						if i+2 >= n || src[i+2] != '{' {
							return nil, errf(line, "malformed \\u escape at line %d", line)
						}
						j := i + 3
						for j < n && src[j] != '}' {
							j++
						}
						if j >= n {
							return nil, errf(line, "unterminated \\u escape at line %d", line)
						}
						cp, perr := strconv.ParseUint(src[i+3:j], 16, 32)
						if perr != nil {
							return nil, errf(line, "invalid unicode escape at line %d", line)
						}
						buf = append(buf, []byte(string(rune(cp)))...)
						adv(j - i + 1)
					default:
						// Two hex digits => raw byte.
						if i+2 < n && isHex(src[i+1]) && isHex(src[i+2]) {
							v, _ := strconv.ParseUint(src[i+1:i+3], 16, 8)
							buf = append(buf, byte(v))
							adv(3)
						} else {
							return nil, errf(line, "invalid string escape \\%c at line %d", e, line)
						}
					}
					continue
				}
				buf = append(buf, src[i])
				adv(1)
			}
			if i >= n {
				return nil, errf(startLine, "unclosed string starting at line %d, col %d", startLine, startCol)
			}
			adv(1) // closing quote
			toks = append(toks, token{kind: tString, text: string(buf), bytes: buf, line: startLine, col: startCol})
			continue
		}

		// Atom: maximal run of idchars.
		if idchar(c) {
			startCol := col
			start := i
			for i < n && idchar(src[i]) {
				adv(1)
			}
			toks = append(toks, token{kind: tAtom, text: src[start:i], line: line, col: startCol})
			continue
		}

		return nil, errf(line, "unexpected character %q at line %d, col %d", c, line, col)
	}

	toks = append(toks, token{kind: tEOF, line: line, col: col})
	return toks, nil
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// ---------------------------------------------------------------------------
// Cursor
// ---------------------------------------------------------------------------

type cursor struct {
	toks []token
	pos  int
}

func (c *cursor) cur() token {
	if c.pos >= len(c.toks) {
		return token{kind: tEOF}
	}
	return c.toks[c.pos]
}

func (c *cursor) peekText() string {
	if c.pos+1 < len(c.toks) && c.toks[c.pos+1].kind == tAtom {
		return c.toks[c.pos+1].text
	}
	return ""
}

func (c *cursor) advance() token {
	t := c.cur()
	c.pos++
	return t
}

func (c *cursor) atEnd() bool { return c.cur().kind == tEOF }

// ---------------------------------------------------------------------------
// Module AST
// ---------------------------------------------------------------------------

type funcType struct {
	params  []byte
	results []byte
}

type importDef struct {
	module string
	name   string
	kind   byte // 0x00 func, 0x02 memory, 0x03 global
	typeIx uint32
	memMin uint32
	memMax int64 // -1 => no max
	gValt  byte
	gMut   bool
}

type funcDef struct {
	typeIx     uint32
	localTypes []byte
	body       []byte // encoded, including trailing 0x0b
}

type memoryDef struct {
	min uint32
	max int64 // -1 => no max
}

type globalDef struct {
	valt byte
	mut  bool
	init []byte // const expr incl trailing 0x0b
}

type exportDef struct {
	name string
	kind byte // 0x00 func, 0x01 table, 0x02 mem, 0x03 global
	idx  uint32
}

// pendingExport is resolved after the full index space is known, so exports
// may forward-reference funcs/globals defined later in the module.
type pendingExport struct {
	name string
	kind byte
	mode exportMode
	sval string
	ival uint32
}

type exportMode int

const (
	exFuncName exportMode = iota // resolve sval via funcNames (absolute)
	exFuncDefined                // ival is a defined-func ordinal
	exGlobalName                 // resolve sval via globalNames (absolute)
	exGlobalDefined              // ival is a defined-global ordinal
	exDirect                     // ival is already an absolute index
)

type dataDef struct {
	offset []byte // const expr incl trailing 0x0b
	bytes  []byte
}

type watModule struct {
	types    []funcType
	typeIdx  map[string]uint32
	imports  []importDef
	funcs    []funcDef
	memories []memoryDef
	globals  []globalDef
	exports  []exportDef
	datas    []dataDef
	start    int64 // -1 => none

	pendingExports []pendingExport
	funcNames      map[string]uint32
	globalNames    map[string]uint32
	numImpFuncs    uint32
	numImpGlobals  uint32
	guard          *MemoryGuard
}

func newModule() *watModule {
	return &watModule{
		typeIdx:     map[string]uint32{},
		funcNames:   map[string]uint32{},
		globalNames: map[string]uint32{},
		start:       -1,
	}
}

func (m *watModule) getType(params, results []byte) uint32 {
	key := string(params) + "|" + string(results)
	if i, ok := m.typeIdx[key]; ok {
		return i
	}
	i := uint32(len(m.types))
	m.typeIdx[key] = i
	m.types = append(m.types, funcType{params: params, results: results})
	return i
}

// ---------------------------------------------------------------------------
// Assembler entry point
// ---------------------------------------------------------------------------

func assemble(src string, guard *MemoryGuard) ([]byte, error) {
	toks, err := tokenize(src)
	if err != nil {
		return nil, err
	}
	c := &cursor{toks: toks}

	if err := expect(c, tLParen); err != nil {
		return nil, err
	}
	if err := expectKeyword(c, "module"); err != nil {
		return nil, err
	}

	m := newModule()
	m.guard = guard

	// A holding area for func bodies whose signatures are parsed in pass 1 but
	// whose bodies must be encoded in pass 2 (after all names are known).
	type pendingFunc struct {
		localNames []string // params first, then locals
		localTypes []byte
		bodyToks   []token
		defIndex   int // index into m.funcs
	}
	var pending []pendingFunc

	for c.cur().kind == tLParen {
		c.advance() // '('
		kw := c.cur()
		if kw.kind != tAtom {
			return nil, errf(kw.line, "expected section keyword, got %q at line %d", kw.text, kw.line)
		}
		c.advance()

		switch kw.text {
		case "type":
			// (type $?name (func (param..)(result..)))
			skipName(c)
			if err := expect(c, tLParen); err != nil {
				return nil, err
			}
			if err := expectKeyword(c, "func"); err != nil {
				return nil, err
			}
			params, results, err := parseParamsResults(c)
			if err != nil {
				return nil, err
			}
			if err := expect(c, tRParen); err != nil { // close (func
				return nil, err
			}
			if err := expect(c, tRParen); err != nil { // close (type
				return nil, err
			}
			m.getType(params, results)

		case "import":
			if err := parseImport(c, m); err != nil {
				return nil, err
			}

		case "memory":
			name := readName(c)
			_ = name
			// Inline export(s): (memory (export "x") min max?)
			exports := parseInlineExports(c)
			// Inline import: (memory (import "m" "n") min max?)
			if isInlineImport(c) {
				mod, nm, err := parseInlineImport(c)
				if err != nil {
					return nil, err
				}
				min, max, err := parseLimits(c)
				if err != nil {
					return nil, err
				}
				m.imports = append(m.imports, importDef{module: mod, name: nm, kind: 0x02, memMin: min, memMax: max})
			} else {
				min, max, err := parseLimits(c)
				if err != nil {
					return nil, err
				}
				idx := uint32(len(m.memories))
				m.memories = append(m.memories, memoryDef{min: min, max: max})
				for _, en := range exports {
					m.pendingExports = append(m.pendingExports, pendingExport{name: en, kind: 0x02, mode: exDirect, ival: idx})
				}
			}
			if err := expect(c, tRParen); err != nil {
				return nil, err
			}

		case "global":
			if err := parseGlobal(c, m); err != nil {
				return nil, err
			}

		case "export":
			if err := parseExport(c, m); err != nil {
				return nil, err
			}

		case "start":
			t := c.advance()
			idx, err := resolveRef(t.text, m.funcNames)
			if err != nil {
				return nil, errf(t.line, "start: %v", err)
			}
			m.start = int64(idx)
			if err := expect(c, tRParen); err != nil {
				return nil, err
			}

		case "data":
			if err := parseData(c, m); err != nil {
				return nil, err
			}

		case "func":
			// Assign the defined-func index (import funcs are counted later,
			// so we record names against a defined slot and fix up ordering
			// after imports are all known — but WAT lists imports and defs in
			// source order and the func index space is imports-first. We track
			// defined funcs separately and offset names in pass 2.)
			defIx := len(m.funcs)
			m.funcs = append(m.funcs, funcDef{})
			name := readName(c)

			exports := parseInlineExports(c)

			if isInlineImport(c) {
				mod, nm, err := parseInlineImport(c)
				if err != nil {
					return nil, err
				}
				params, results, err := parseParamsResults(c)
				if err != nil {
					return nil, err
				}
				// An imported func is not a defined func: undo the slot.
				m.funcs = m.funcs[:defIx]
				tix := m.getType(params, results)
				m.imports = append(m.imports, importDef{module: mod, name: nm, kind: 0x00, typeIx: tix})
				if err := expect(c, tRParen); err != nil {
					return nil, err
				}
				break
			}

			params, results, localNames, localTypes, err := parseFuncHeader(c)
			if err != nil {
				return nil, err
			}
			tix := m.getType(params, results)
			m.funcs[defIx].typeIx = tix
			m.funcs[defIx].localTypes = localTypes

			// Capture body tokens up to the matching close paren of (func.
			bodyToks, err := captureGroup(c)
			if err != nil {
				return nil, err
			}

			pending = append(pending, pendingFunc{
				localNames: localNames,
				localTypes: localTypes,
				bodyToks:   bodyToks,
				defIndex:   defIx,
			})

			if name != "" {
				// Defined-relative; offset by numImpFuncs during fixup.
				m.funcNames[name] = uint32(defIx)
			}
			for _, en := range exports {
				m.pendingExports = append(m.pendingExports, pendingExport{name: en, kind: 0x00, mode: exFuncDefined, ival: uint32(defIx)})
			}

		default:
			return nil, errf(kw.line, "unsupported module field %q at line %d", kw.text, kw.line)
		}
	}

	if err := expect(c, tRParen); err != nil { // close (module
		return nil, err
	}

	// Resolve the func/global index spaces: imported entities occupy the low
	// indices, defined entities follow. Imported names were stored absolute
	// (under an "#imp$" marker); defined names were stored defined-relative.
	for _, im := range m.imports {
		switch im.kind {
		case 0x00:
			m.numImpFuncs++
		case 0x03:
			m.numImpGlobals++
		}
	}
	resolved := map[string]uint32{}
	for k, v := range m.funcNames {
		if strings.HasPrefix(k, "#imp$") {
			resolved[k[len("#imp$"):]] = v // already absolute
		} else {
			resolved[k] = v + m.numImpFuncs
		}
	}
	m.funcNames = resolved
	for k, v := range m.globalNames {
		m.globalNames[k] = v + m.numImpGlobals
	}

	if err := m.finalizeExports(); err != nil {
		return nil, err
	}

	// Pass 2: encode func bodies now that all names/indices are resolved.
	for _, pf := range pending {
		be := &bodyEnc{
			m:          m,
			localNames: map[string]uint32{},
			localTypes: pf.localTypes,
		}
		for i, nm := range pf.localNames {
			if nm != "" {
				be.localNames[nm] = uint32(i)
			}
		}
		if m.guard != nil {
			// Scratch locals for guarded stores start after all params+locals.
			be.guard = m.guard
			be.scratchBase = uint32(len(pf.localNames))
		}
		bc := &cursor{toks: append(pf.bodyToks, token{kind: tEOF})}
		term, err := be.parseSeq(bc)
		if err != nil {
			return nil, err
		}
		if term != "eof" {
			return nil, errf(bc.cur().line, "unexpected %q in function body", term)
		}
		be.out = append(be.out, 0x0b) // function end
		if be.usedScratch {
			// Declare the guard scratch locals: addr(i32), v32(i32), v64(i64),
			// vf32(f32), vf64(f64).
			pf.localTypes = append(pf.localTypes, 0x7f, 0x7f, 0x7e, 0x7d, 0x7c)
			m.funcs[pf.defIndex].localTypes = pf.localTypes
		}
		m.funcs[pf.defIndex].body = be.out
	}

	return m.encode()
}

// ---------------------------------------------------------------------------
// Field parsers
// ---------------------------------------------------------------------------

func expect(c *cursor, k tokKind) error {
	t := c.advance()
	if t.kind != k {
		return errf(t.line, "expected %s, got %q at line %d", kindName(k), t.text, t.line)
	}
	return nil
}

func expectKeyword(c *cursor, kw string) error {
	t := c.advance()
	if t.kind != tAtom || t.text != kw {
		return errf(t.line, "expected %q, got %q at line %d", kw, t.text, t.line)
	}
	return nil
}

func kindName(k tokKind) string {
	switch k {
	case tLParen:
		return "'('"
	case tRParen:
		return "')'"
	case tString:
		return "string"
	case tAtom:
		return "atom"
	default:
		return "eof"
	}
}

// readName returns the leading $name of a field (without the '$'), or "".
func readName(c *cursor) string {
	if c.cur().kind == tAtom && strings.HasPrefix(c.cur().text, "$") {
		return c.advance().text
	}
	return ""
}

func skipName(c *cursor) { readName(c) }

func parseInlineExports(c *cursor) []string {
	var out []string
	for c.cur().kind == tLParen && c.peekText() == "export" {
		c.advance() // '('
		c.advance() // 'export'
		out = append(out, c.advance().text)
		// closing ')'
		c.advance()
	}
	return out
}

func isInlineImport(c *cursor) bool {
	return c.cur().kind == tLParen && c.peekText() == "import"
}

func parseInlineImport(c *cursor) (string, string, error) {
	c.advance() // '('
	c.advance() // 'import'
	mod := c.advance()
	nm := c.advance()
	if err := expect(c, tRParen); err != nil {
		return "", "", err
	}
	return mod.text, nm.text, nil
}

func parseLimits(c *cursor) (uint32, int64, error) {
	minT := c.advance()
	min, err := parseU32(minT.text)
	if err != nil {
		return 0, 0, errf(minT.line, "invalid memory min %q at line %d", minT.text, minT.line)
	}
	max := int64(-1)
	if c.cur().kind == tAtom {
		maxT := c.advance()
		mx, err := parseU32(maxT.text)
		if err != nil {
			return 0, 0, errf(maxT.line, "invalid memory max %q at line %d", maxT.text, maxT.line)
		}
		max = int64(mx)
	}
	return min, max, nil
}

func parseImport(c *cursor, m *watModule) error {
	mod := c.advance()
	nm := c.advance()
	if err := expect(c, tLParen); err != nil {
		return err
	}
	kind := c.advance()
	switch kind.text {
	case "func":
		name := readName(c)
		params, results, err := parseParamsResults(c)
		if err != nil {
			return err
		}
		tix := m.getType(params, results)
		// Imported funcs occupy the low func indices in source order.
		absIx := uint32(0)
		for _, im := range m.imports {
			if im.kind == 0x00 {
				absIx++
			}
		}
		if name != "" {
			m.funcNames["#imp$"+name] = absIx // absolute, marked
		}
		m.imports = append(m.imports, importDef{module: mod.text, name: nm.text, kind: 0x00, typeIx: tix})
	case "memory":
		min, max, err := parseLimits(c)
		if err != nil {
			return err
		}
		m.imports = append(m.imports, importDef{module: mod.text, name: nm.text, kind: 0x02, memMin: min, memMax: max})
	case "global":
		valt, mut, err := parseGlobalType(c)
		if err != nil {
			return err
		}
		name := readName(c)
		_ = name
		m.imports = append(m.imports, importDef{module: mod.text, name: nm.text, kind: 0x03, gValt: valt, gMut: mut})
	default:
		return errf(kind.line, "unsupported import kind %q at line %d", kind.text, kind.line)
	}
	if err := expect(c, tRParen); err != nil { // close (func|memory|global
		return err
	}
	return expect(c, tRParen) // close (import
}

func parseGlobalType(c *cursor) (byte, bool, error) {
	if c.cur().kind == tLParen && c.peekText() == "mut" {
		c.advance() // '('
		c.advance() // 'mut'
		vt := c.advance()
		valt, ok := valType(vt.text)
		if !ok {
			return 0, false, errf(vt.line, "invalid value type %q at line %d", vt.text, vt.line)
		}
		if err := expect(c, tRParen); err != nil {
			return 0, false, err
		}
		return valt, true, nil
	}
	vt := c.advance()
	valt, ok := valType(vt.text)
	if !ok {
		return 0, false, errf(vt.line, "invalid value type %q at line %d", vt.text, vt.line)
	}
	return valt, false, nil
}

func parseGlobal(c *cursor, m *watModule) error {
	name := readName(c)
	exports := parseInlineExports(c)
	if isInlineImport(c) {
		mod, nm, err := parseInlineImport(c)
		if err != nil {
			return err
		}
		valt, mut, err := parseGlobalType(c)
		if err != nil {
			return err
		}
		m.imports = append(m.imports, importDef{module: mod, name: nm, kind: 0x03, gValt: valt, gMut: mut})
		return expect(c, tRParen)
	}

	valt, mut, err := parseGlobalType(c)
	if err != nil {
		return err
	}
	idx := uint32(len(m.globals))
	if name != "" {
		m.globalNames[name] = idx
	}
	// Init expression: a single const/global.get instruction (folded or linear).
	be := &bodyEnc{m: m, localNames: map[string]uint32{}}
	if _, err := be.parseSeq(c); err != nil {
		return err
	}
	be.out = append(be.out, 0x0b)
	m.globals = append(m.globals, globalDef{valt: valt, mut: mut, init: be.out})
	for _, en := range exports {
		m.pendingExports = append(m.pendingExports, pendingExport{name: en, kind: 0x03, mode: exGlobalDefined, ival: idx})
	}
	return expect(c, tRParen)
}

func parseExport(c *cursor, m *watModule) error {
	nm := c.advance()
	if err := expect(c, tLParen); err != nil {
		return err
	}
	kind := c.advance()
	ref := c.advance()
	pe := pendingExport{name: nm.text}
	switch kind.text {
	case "func":
		pe.kind = 0x00
		if strings.HasPrefix(ref.text, "$") {
			pe.mode = exFuncName
			pe.sval = ref.text
		} else {
			idx, err := parseU32(ref.text)
			if err != nil {
				return errf(ref.line, "export %q: invalid func index %q", nm.text, ref.text)
			}
			pe.mode = exDirect
			pe.ival = idx
		}
	case "memory":
		pe.kind = 0x02
		pe.mode = exDirect
		if !strings.HasPrefix(ref.text, "$") {
			idx, err := parseU32(ref.text)
			if err != nil {
				return errf(ref.line, "export %q: invalid memory index %q", nm.text, ref.text)
			}
			pe.ival = idx
		}
	case "global":
		pe.kind = 0x03
		if strings.HasPrefix(ref.text, "$") {
			pe.mode = exGlobalName
			pe.sval = ref.text
		} else {
			idx, err := parseU32(ref.text)
			if err != nil {
				return errf(ref.line, "export %q: invalid global index %q", nm.text, ref.text)
			}
			pe.mode = exDirect
			pe.ival = idx
		}
	default:
		return errf(kind.line, "unsupported export kind %q at line %d", kind.text, kind.line)
	}
	if err := expect(c, tRParen); err != nil { // close (func|memory|global
		return err
	}
	m.pendingExports = append(m.pendingExports, pe)
	return expect(c, tRParen) // close (export
}

func parseData(c *cursor, m *watModule) error {
	skipName(c)
	// Optional explicit memory index (ignored, single memory only).
	if c.cur().kind == tAtom {
		c.advance()
	}
	// Offset: (i32.const N) or (offset (i32.const N)).
	be := &bodyEnc{m: m, localNames: map[string]uint32{}}
	if c.cur().kind == tLParen && c.peekText() == "offset" {
		c.advance() // '('
		c.advance() // 'offset'
		if _, err := be.parseSeq(c); err != nil {
			return err
		}
		if err := expect(c, tRParen); err != nil {
			return err
		}
	} else if c.cur().kind == tLParen {
		if err := be.emitFolded(c); err != nil {
			return err
		}
	} else {
		return errf(c.cur().line, "data segment requires an offset expression at line %d", c.cur().line)
	}
	be.out = append(be.out, 0x0b)

	var payload []byte
	for c.cur().kind == tString {
		payload = append(payload, c.advance().bytes...)
	}
	m.datas = append(m.datas, dataDef{offset: be.out, bytes: payload})
	return expect(c, tRParen)
}

// parseParamsResults parses (param ...)* (result ...)* groups.
func parseParamsResults(c *cursor) ([]byte, []byte, error) {
	var params, results []byte
	for c.cur().kind == tLParen {
		kw := c.peekText()
		if kw != "param" && kw != "result" {
			break
		}
		c.advance() // '('
		c.advance() // 'param'|'result'
		if kw == "param" {
			if c.cur().kind == tAtom && strings.HasPrefix(c.cur().text, "$") {
				c.advance() // name
				vt := c.advance()
				b, ok := valType(vt.text)
				if !ok {
					return nil, nil, errf(vt.line, "invalid param type %q at line %d", vt.text, vt.line)
				}
				params = append(params, b)
			} else {
				for c.cur().kind == tAtom {
					vt := c.advance()
					b, ok := valType(vt.text)
					if !ok {
						return nil, nil, errf(vt.line, "invalid param type %q at line %d", vt.text, vt.line)
					}
					params = append(params, b)
				}
			}
		} else {
			for c.cur().kind == tAtom {
				vt := c.advance()
				b, ok := valType(vt.text)
				if !ok {
					return nil, nil, errf(vt.line, "invalid result type %q at line %d", vt.text, vt.line)
				}
				results = append(results, b)
			}
		}
		if err := expect(c, tRParen); err != nil {
			return nil, nil, err
		}
	}
	return params, results, nil
}

// parseFuncHeader parses params, results and locals, returning local names
// (params first, then locals) alongside their types.
func parseFuncHeader(c *cursor) (params, results []byte, localNames []string, localTypes []byte, err error) {
	// Params & results, but we also need param names -> reparse manually.
	for c.cur().kind == tLParen {
		kw := c.peekText()
		if kw != "param" && kw != "result" {
			break
		}
		c.advance() // '('
		c.advance() // kw
		if kw == "param" {
			if c.cur().kind == tAtom && strings.HasPrefix(c.cur().text, "$") {
				name := c.advance().text
				vt := c.advance()
				b, ok := valType(vt.text)
				if !ok {
					return nil, nil, nil, nil, errf(vt.line, "invalid param type %q at line %d", vt.text, vt.line)
				}
				params = append(params, b)
				localNames = append(localNames, name)
			} else {
				for c.cur().kind == tAtom {
					vt := c.advance()
					b, ok := valType(vt.text)
					if !ok {
						return nil, nil, nil, nil, errf(vt.line, "invalid param type %q at line %d", vt.text, vt.line)
					}
					params = append(params, b)
					localNames = append(localNames, "")
				}
			}
		} else {
			for c.cur().kind == tAtom {
				vt := c.advance()
				b, ok := valType(vt.text)
				if !ok {
					return nil, nil, nil, nil, errf(vt.line, "invalid result type %q at line %d", vt.text, vt.line)
				}
				results = append(results, b)
			}
		}
		if e := expect(c, tRParen); e != nil {
			return nil, nil, nil, nil, e
		}
	}

	// Locals.
	for c.cur().kind == tLParen && c.peekText() == "local" {
		c.advance() // '('
		c.advance() // 'local'
		if c.cur().kind == tAtom && strings.HasPrefix(c.cur().text, "$") {
			name := c.advance().text
			vt := c.advance()
			b, ok := valType(vt.text)
			if !ok {
				return nil, nil, nil, nil, errf(vt.line, "invalid local type %q at line %d", vt.text, vt.line)
			}
			localTypes = append(localTypes, b)
			localNames = append(localNames, name)
		} else {
			for c.cur().kind == tAtom {
				vt := c.advance()
				b, ok := valType(vt.text)
				if !ok {
					return nil, nil, nil, nil, errf(vt.line, "invalid local type %q at line %d", vt.text, vt.line)
				}
				localTypes = append(localTypes, b)
				localNames = append(localNames, "")
			}
		}
		if e := expect(c, tRParen); e != nil {
			return nil, nil, nil, nil, e
		}
	}
	return params, results, localNames, localTypes, nil
}

// captureGroup collects tokens until the paren that closes the currently-open
// group (the '(' has already been consumed), consuming that ')'. The returned
// slice excludes the closing ')'.
func captureGroup(c *cursor) ([]token, error) {
	depth := 1
	var out []token
	for {
		t := c.cur()
		if t.kind == tEOF {
			return nil, errf(t.line, "unexpected end of input inside group")
		}
		if t.kind == tLParen {
			depth++
		} else if t.kind == tRParen {
			depth--
			if depth == 0 {
				c.advance() // consume closing ')'
				return out, nil
			}
		}
		out = append(out, t)
		c.advance()
	}
}

// ---------------------------------------------------------------------------
// Value types
// ---------------------------------------------------------------------------

func valType(s string) (byte, bool) {
	switch s {
	case "i32":
		return 0x7f, true
	case "i64":
		return 0x7e, true
	case "f32":
		return 0x7d, true
	case "f64":
		return 0x7c, true
	case "funcref":
		return 0x70, true
	case "externref":
		return 0x6f, true
	}
	return 0, false
}

// ---------------------------------------------------------------------------
// Body encoder
// ---------------------------------------------------------------------------

type bodyEnc struct {
	m          *watModule
	localNames map[string]uint32
	localTypes []byte
	labels     []string // innermost last; "" for anonymous
	out        []byte
	// Guarded-store injection (MemoryGuard). scratchBase is the index of the
	// first injected scratch local; usedScratch records whether any were used.
	guard       *MemoryGuard
	scratchBase uint32
	usedScratch bool
}

func (b *bodyEnc) pushLabel(name string) { b.labels = append(b.labels, name) }
func (b *bodyEnc) popLabel()             { b.labels = b.labels[:len(b.labels)-1] }

func (b *bodyEnc) resolveLabel(atom string, line int) (uint32, error) {
	if strings.HasPrefix(atom, "$") {
		for i := len(b.labels) - 1; i >= 0; i-- {
			if b.labels[i] == atom {
				return uint32(len(b.labels) - 1 - i), nil
			}
		}
		return 0, errf(line, "undefined label %q at line %d", atom, line)
	}
	v, err := parseU32(atom)
	if err != nil {
		return 0, errf(line, "invalid label %q at line %d", atom, line)
	}
	return v, nil
}

// parseSeq consumes an instruction sequence, appending encoded bytes. It stops
// (without consuming) at ')' , EOF, or an 'end'/'else' atom, returning
// "rparen", "eof", "end" or "else".
func (b *bodyEnc) parseSeq(c *cursor) (string, error) {
	for {
		t := c.cur()
		switch t.kind {
		case tEOF:
			return "eof", nil
		case tRParen:
			return "rparen", nil
		case tLParen:
			if err := b.emitFolded(c); err != nil {
				return "", err
			}
		case tAtom:
			if t.text == "end" || t.text == "else" {
				return t.text, nil
			}
			if err := b.emitLinear(c); err != nil {
				return "", err
			}
		}
	}
}

// parseBlockType parses an optional label and (param)/(result)/(type) groups,
// returning the label and the encoded blocktype bytes.
func (b *bodyEnc) parseBlockType(c *cursor) (string, []byte, error) {
	label := ""
	if c.cur().kind == tAtom && strings.HasPrefix(c.cur().text, "$") {
		label = c.advance().text
	}
	var params, results []byte
	for c.cur().kind == tLParen {
		kw := c.peekText()
		if kw != "param" && kw != "result" && kw != "type" {
			break
		}
		if kw == "type" {
			// (type $x) reference: skip, rely on inline param/result.
			c.advance()
			c.advance()
			if c.cur().kind == tAtom {
				c.advance()
			}
			if err := expect(c, tRParen); err != nil {
				return "", nil, err
			}
			continue
		}
		p, r, err := parseParamsResults(c)
		if err != nil {
			return "", nil, err
		}
		params = append(params, p...)
		results = append(results, r...)
	}

	var bt []byte
	switch {
	case len(params) == 0 && len(results) == 0:
		bt = []byte{0x40}
	case len(params) == 0 && len(results) == 1:
		bt = []byte{results[0]}
	default:
		idx := b.m.getType(params, results)
		bt = encodeSLEB(int64(idx))
	}
	return label, bt, nil
}

func peekOpcode(c *cursor) string {
	if c.cur().kind == tLParen {
		return c.peekText()
	}
	return ""
}

// emitFolded encodes a single folded (parenthesized) instruction.
func (b *bodyEnc) emitFolded(c *cursor) error {
	c.advance() // '('
	op := c.advance()
	if op.kind != tAtom {
		return errf(op.line, "expected instruction, got %q at line %d", op.text, op.line)
	}

	switch op.text {
	case "block", "loop":
		opcode := byte(0x02)
		if op.text == "loop" {
			opcode = 0x03
		}
		label, bt, err := b.parseBlockType(c)
		if err != nil {
			return err
		}
		b.out = append(b.out, opcode)
		b.out = append(b.out, bt...)
		b.pushLabel(label)
		term, err := b.parseSeq(c)
		if err != nil {
			return err
		}
		if term != "rparen" {
			return errf(op.line, "expected ')' to close %s, got %q", op.text, term)
		}
		b.popLabel()
		b.out = append(b.out, 0x0b)
		return expect(c, tRParen)

	case "if":
		label, bt, err := b.parseBlockType(c)
		if err != nil {
			return err
		}
		// Condition operands: folded instrs before (then ...).
		for c.cur().kind == tLParen && peekOpcode(c) != "then" {
			if err := b.emitFolded(c); err != nil {
				return err
			}
		}
		b.out = append(b.out, 0x04)
		b.out = append(b.out, bt...)
		b.pushLabel(label)
		// (then ...)
		if !(c.cur().kind == tLParen && peekOpcode(c) == "then") {
			return errf(op.line, "folded if requires a (then ...) clause at line %d", op.line)
		}
		c.advance() // '('
		c.advance() // 'then'
		if _, err := b.parseSeq(c); err != nil {
			return err
		}
		if err := expect(c, tRParen); err != nil {
			return err
		}
		// (else ...) optional
		if c.cur().kind == tLParen && peekOpcode(c) == "else" {
			c.advance() // '('
			c.advance() // 'else'
			b.out = append(b.out, 0x05)
			if _, err := b.parseSeq(c); err != nil {
				return err
			}
			if err := expect(c, tRParen); err != nil {
				return err
			}
		}
		b.popLabel()
		b.out = append(b.out, 0x0b)
		return expect(c, tRParen)

	default:
		// Normal folded op: leading atoms are immediates; child groups are
		// operands emitted before the op itself.
		var imms []string
		for c.cur().kind == tAtom {
			imms = append(imms, c.advance().text)
		}
		for c.cur().kind == tLParen {
			if err := b.emitFolded(c); err != nil {
				return err
			}
		}
		if err := b.encodeOp(op.text, imms, op.line); err != nil {
			return err
		}
		return expect(c, tRParen)
	}
}

// emitLinear encodes a single linear (stack-machine) instruction.
func (b *bodyEnc) emitLinear(c *cursor) error {
	op := c.advance()
	switch op.text {
	case "block", "loop":
		opcode := byte(0x02)
		if op.text == "loop" {
			opcode = 0x03
		}
		label, bt, err := b.parseBlockType(c)
		if err != nil {
			return err
		}
		b.out = append(b.out, opcode)
		b.out = append(b.out, bt...)
		b.pushLabel(label)
		term, err := b.parseSeq(c)
		if err != nil {
			return err
		}
		if term != "end" {
			return errf(op.line, "expected 'end' to close %s at line %d, got %q", op.text, op.line, term)
		}
		c.advance() // consume 'end'
		b.popLabel()
		b.out = append(b.out, 0x0b)
		return nil

	case "if":
		label, bt, err := b.parseBlockType(c)
		if err != nil {
			return err
		}
		b.out = append(b.out, 0x04)
		b.out = append(b.out, bt...)
		b.pushLabel(label)
		term, err := b.parseSeq(c)
		if err != nil {
			return err
		}
		if term == "else" {
			c.advance() // consume 'else'
			b.out = append(b.out, 0x05)
			term2, err := b.parseSeq(c)
			if err != nil {
				return err
			}
			if term2 != "end" {
				return errf(op.line, "expected 'end' to close if/else at line %d, got %q", op.line, term2)
			}
			c.advance()
		} else if term == "end" {
			c.advance()
		} else {
			return errf(op.line, "expected 'else' or 'end' to close if at line %d, got %q", op.line, term)
		}
		b.popLabel()
		b.out = append(b.out, 0x0b)
		return nil

	default:
		imms, err := b.readImms(c, op.text)
		if err != nil {
			return err
		}
		return b.encodeOp(op.text, imms, op.line)
	}
}

// readImms reads the immediate atoms for a linear op, according to its arity.
func (b *bodyEnc) readImms(c *cursor, op string) ([]string, error) {
	switch immCategory(op) {
	case immOne:
		if c.cur().kind != tAtom {
			return nil, errf(c.cur().line, "%s requires an immediate at line %d", op, c.cur().line)
		}
		return []string{c.advance().text}, nil
	case immMemarg:
		var out []string
		for c.cur().kind == tAtom &&
			(strings.HasPrefix(c.cur().text, "offset=") || strings.HasPrefix(c.cur().text, "align=")) {
			out = append(out, c.advance().text)
		}
		return out, nil
	case immBrTable:
		var out []string
		for c.cur().kind == tAtom &&
			(strings.HasPrefix(c.cur().text, "$") || isNumeric(c.cur().text)) {
			out = append(out, c.advance().text)
		}
		return out, nil
	default:
		return nil, nil
	}
}

type immCat int

const (
	immNone immCat = iota
	immOne
	immMemarg
	immBrTable
)

func immCategory(op string) immCat {
	switch op {
	case "i32.const", "i64.const", "f32.const", "f64.const",
		"local.get", "local.set", "local.tee", "global.get", "global.set",
		"call", "br", "br_if":
		return immOne
	case "br_table":
		return immBrTable
	}
	if _, ok := loadStoreOps[op]; ok {
		return immMemarg
	}
	return immNone
}

// encodeOp appends the binary encoding of a single non-structured instruction.
func (b *bodyEnc) encodeOp(op string, imms []string, line int) error {
	// Memory load/store with memarg.
	if ls, ok := loadStoreOps[op]; ok {
		align := ls.align
		offset := uint32(0)
		for _, im := range imms {
			if strings.HasPrefix(im, "align=") {
				v, err := parseU32(im[len("align="):])
				if err != nil {
					return errf(line, "invalid align %q at line %d", im, line)
				}
				align = log2exp(v)
			} else if strings.HasPrefix(im, "offset=") {
				v, err := parseU32(im[len("offset="):])
				if err != nil {
					return errf(line, "invalid offset %q at line %d", im, line)
				}
				offset = v
			}
		}
		// Injected pointer-bounds guard on stores (tenant isolation).
		if b.guard != nil {
			if vOff := storeValueScratch(op); vOff > 0 {
				b.emitGuardedStore(ls.opcode, align, offset, b.scratchBase+uint32(vOff))
				return nil
			}
		}
		b.out = append(b.out, ls.opcode)
		b.out = append(b.out, encodeULEB(uint64(align))...)
		b.out = append(b.out, encodeULEB(uint64(offset))...)
		return nil
	}

	switch op {
	case "i32.const":
		v, err := parseInt(imms, line, 32)
		if err != nil {
			return err
		}
		b.out = append(b.out, 0x41)
		b.out = append(b.out, encodeSLEB(v)...)
		return nil
	case "i64.const":
		v, err := parseInt(imms, line, 64)
		if err != nil {
			return err
		}
		b.out = append(b.out, 0x42)
		b.out = append(b.out, encodeSLEB(v)...)
		return nil
	case "f32.const":
		if len(imms) != 1 {
			return errf(line, "f32.const requires one immediate at line %d", line)
		}
		f, err := parseFloat(imms[0])
		if err != nil {
			return errf(line, "invalid f32 %q at line %d", imms[0], line)
		}
		bits := math.Float32bits(float32(f))
		b.out = append(b.out, 0x43, byte(bits), byte(bits>>8), byte(bits>>16), byte(bits>>24))
		return nil
	case "f64.const":
		if len(imms) != 1 {
			return errf(line, "f64.const requires one immediate at line %d", line)
		}
		f, err := parseFloat(imms[0])
		if err != nil {
			return errf(line, "invalid f64 %q at line %d", imms[0], line)
		}
		bits := math.Float64bits(f)
		b.out = append(b.out, 0x44)
		for i := 0; i < 8; i++ {
			b.out = append(b.out, byte(bits>>(8*i)))
		}
		return nil

	case "local.get", "local.set", "local.tee":
		if len(imms) != 1 {
			return errf(line, "%s requires one immediate at line %d", op, line)
		}
		idx, err := b.resolveLocal(imms[0], line)
		if err != nil {
			return err
		}
		b.out = append(b.out, localOps[op])
		b.out = append(b.out, encodeULEB(uint64(idx))...)
		return nil

	case "global.get", "global.set":
		if len(imms) != 1 {
			return errf(line, "%s requires one immediate at line %d", op, line)
		}
		idx, err := resolveRef(imms[0], b.m.globalNames)
		if err != nil {
			return errf(line, "%s: %v", op, err)
		}
		b.out = append(b.out, globalOps[op])
		b.out = append(b.out, encodeULEB(uint64(idx))...)
		return nil

	case "call":
		if len(imms) != 1 {
			return errf(line, "call requires one immediate at line %d", line)
		}
		idx, err := resolveFuncRef(imms[0], b.m)
		if err != nil {
			return errf(line, "call: %v", err)
		}
		b.out = append(b.out, 0x10)
		b.out = append(b.out, encodeULEB(uint64(idx))...)
		return nil

	case "br", "br_if":
		if len(imms) != 1 {
			return errf(line, "%s requires one immediate at line %d", op, line)
		}
		idx, err := b.resolveLabel(imms[0], line)
		if err != nil {
			return err
		}
		opcode := byte(0x0c)
		if op == "br_if" {
			opcode = 0x0d
		}
		b.out = append(b.out, opcode)
		b.out = append(b.out, encodeULEB(uint64(idx))...)
		return nil

	case "br_table":
		if len(imms) < 1 {
			return errf(line, "br_table requires at least a default label at line %d", line)
		}
		b.out = append(b.out, 0x0e)
		targets := imms[:len(imms)-1]
		def := imms[len(imms)-1]
		b.out = append(b.out, encodeULEB(uint64(len(targets)))...)
		for _, t := range targets {
			idx, err := b.resolveLabel(t, line)
			if err != nil {
				return err
			}
			b.out = append(b.out, encodeULEB(uint64(idx))...)
		}
		didx, err := b.resolveLabel(def, line)
		if err != nil {
			return err
		}
		b.out = append(b.out, encodeULEB(uint64(didx))...)
		return nil

	case "memory.size":
		b.out = append(b.out, 0x3f, 0x00)
		return nil
	case "memory.grow":
		b.out = append(b.out, 0x40, 0x00)
		return nil

	case "call_indirect":
		return errf(line, "call_indirect is unsupported in Gen 0 at line %d", line)
	}

	if opcode, ok := simpleOps[op]; ok {
		if len(imms) != 0 {
			return errf(line, "%s takes no immediates at line %d", op, line)
		}
		b.out = append(b.out, opcode)
		return nil
	}

	return errf(line, "unsupported instruction %q at line %d", op, line)
}

func (b *bodyEnc) resolveLocal(atom string, line int) (uint32, error) {
	if strings.HasPrefix(atom, "$") {
		if idx, ok := b.localNames[atom]; ok {
			return idx, nil
		}
		return 0, errf(line, "undefined local %q at line %d", atom, line)
	}
	v, err := parseU32(atom)
	if err != nil {
		return 0, errf(line, "invalid local index %q at line %d", atom, line)
	}
	return v, nil
}

// storeValueScratch returns the scratch-local offset (from scratchBase) that
// holds the value operand for a store op, or 0 if op is not a store. Layout:
// base+0 addr(i32), base+1 i32, base+2 i64, base+3 f32, base+4 f64.
func storeValueScratch(op string) int {
	switch {
	case strings.HasPrefix(op, "i32.store"):
		return 1
	case strings.HasPrefix(op, "i64.store"):
		return 2
	case op == "f32.store":
		return 3
	case op == "f64.store":
		return 4
	}
	return 0
}

// emitGuardedStore emits a bounds-checked store: it stashes (addr,value) into
// scratch locals, traps (unreachable) if the base address is outside the
// guard's [lower,upper), then performs the original store.
func (b *bodyEnc) emitGuardedStore(opcode byte, align, offset, vScratch uint32) {
	b.usedScratch = true
	a := b.scratchBase
	put := func(bs ...byte) { b.out = append(b.out, bs...) }
	uleb := func(v uint32) { b.out = append(b.out, encodeULEB(uint64(v))...) }

	put(0x21)
	uleb(vScratch) // local.set value
	put(0x21)
	uleb(a) // local.set addr
	put(0x20)
	uleb(a) // local.get addr
	put(0x41)
	b.out = append(b.out, encodeSLEB(int64(int32(b.guard.Lower)))...)
	put(0x49) // i32.lt_u
	put(0x20)
	uleb(a) // local.get addr
	put(0x41)
	b.out = append(b.out, encodeSLEB(int64(int32(b.guard.Upper)))...)
	put(0x4f)       // i32.ge_u
	put(0x72)       // i32.or  (out of bounds?)
	put(0x04, 0x40) // if (empty blocktype)
	put(0x00)       // unreachable
	put(0x0b)       // end
	put(0x20)
	uleb(a) // local.get addr
	put(0x20)
	uleb(vScratch) // local.get value
	put(opcode)
	uleb(align)
	uleb(offset)
}

// ---------------------------------------------------------------------------
// Name / index resolution helpers
// ---------------------------------------------------------------------------

func resolveRef(atom string, names map[string]uint32) (uint32, error) {
	if strings.HasPrefix(atom, "$") {
		if idx, ok := names[atom]; ok {
			return idx, nil
		}
		return 0, fmt.Errorf("undefined name %q", atom)
	}
	return parseU32(atom)
}

// resolveFuncRef resolves a func reference by name or absolute index. Callable
// only after the func index space is finalized (pass 2 / finalize).
func resolveFuncRef(atom string, m *watModule) (uint32, error) {
	if strings.HasPrefix(atom, "$") {
		if idx, ok := m.funcNames[atom]; ok {
			return idx, nil
		}
		return 0, fmt.Errorf("undefined function %q", atom)
	}
	return parseU32(atom)
}

// finalizeExports resolves deferred export references now that the full func
// and global index spaces are known, allowing forward references.
func (m *watModule) finalizeExports() error {
	for _, pe := range m.pendingExports {
		var idx uint32
		switch pe.mode {
		case exFuncName:
			v, ok := m.funcNames[pe.sval]
			if !ok {
				return fmt.Errorf("export %q: undefined function %q", pe.name, pe.sval)
			}
			idx = v
		case exFuncDefined:
			idx = pe.ival + m.numImpFuncs
		case exGlobalName:
			v, ok := m.globalNames[pe.sval]
			if !ok {
				return fmt.Errorf("export %q: undefined global %q", pe.name, pe.sval)
			}
			idx = v
		case exGlobalDefined:
			idx = pe.ival + m.numImpGlobals
		default: // exDirect
			idx = pe.ival
		}
		m.exports = append(m.exports, exportDef{name: pe.name, kind: pe.kind, idx: idx})
	}
	return nil
}

// ---------------------------------------------------------------------------
// Number parsing & LEB encoding
// ---------------------------------------------------------------------------

func stripUnderscore(s string) string {
	if !strings.Contains(s, "_") {
		return s
	}
	return strings.ReplaceAll(s, "_", "")
}

func parseU32(s string) (uint32, error) {
	s = stripUnderscore(s)
	v, err := strconv.ParseUint(s, 0, 64)
	if err != nil {
		return 0, err
	}
	return uint32(v), nil
}

func isNumeric(s string) bool {
	_, err := strconv.ParseInt(stripUnderscore(s), 0, 64)
	if err == nil {
		return true
	}
	_, err = strconv.ParseUint(stripUnderscore(s), 0, 64)
	return err == nil
}

// parseInt parses an integer immediate that may be signed or unsigned, and
// returns it sign-extended into the requested bit width (32 or 64).
func parseInt(imms []string, line, bits int) (int64, error) {
	if len(imms) != 1 {
		return 0, errf(line, "const requires one immediate at line %d", line)
	}
	s := stripUnderscore(imms[0])
	if v, err := strconv.ParseInt(s, 0, 64); err == nil {
		if bits == 32 {
			return int64(int32(v)), nil
		}
		return v, nil
	}
	if u, err := strconv.ParseUint(s, 0, 64); err == nil {
		if bits == 32 {
			return int64(int32(uint32(u))), nil
		}
		return int64(u), nil
	}
	return 0, errf(line, "invalid integer %q at line %d", imms[0], line)
}

func parseFloat(s string) (float64, error) {
	s = stripUnderscore(s)
	switch strings.ToLower(s) {
	case "inf", "+inf":
		return math.Inf(1), nil
	case "-inf":
		return math.Inf(-1), nil
	case "nan", "+nan", "-nan":
		return math.NaN(), nil
	}
	if strings.HasPrefix(strings.ToLower(s), "nan:") {
		return math.NaN(), nil
	}
	return strconv.ParseFloat(s, 64)
}

func encodeSLEB(v int64) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if (v == 0 && b&0x40 == 0) || (v == -1 && b&0x40 != 0) {
			out = append(out, b)
			return out
		}
		out = append(out, b|0x80)
	}
}

func encodeULEB(v uint64) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v == 0 {
			out = append(out, b)
			return out
		}
		out = append(out, b|0x80)
	}
}

// log2exp returns the base-2 exponent of a natural-alignment value.
func log2exp(v uint32) uint32 {
	e := uint32(0)
	for v > 1 {
		v >>= 1
		e++
	}
	return e
}

// ---------------------------------------------------------------------------
// Module binary encoding
// ---------------------------------------------------------------------------

func (m *watModule) encode() ([]byte, error) {
	var buf []byte
	buf = append(buf, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)

	section := func(id byte, body []byte) {
		if len(body) == 0 {
			return
		}
		buf = append(buf, id)
		buf = append(buf, encodeULEB(uint64(len(body)))...)
		buf = append(buf, body...)
	}

	// 1: Type
	{
		var s []byte
		s = append(s, encodeULEB(uint64(len(m.types)))...)
		for _, t := range m.types {
			s = append(s, 0x60)
			s = append(s, encodeULEB(uint64(len(t.params)))...)
			s = append(s, t.params...)
			s = append(s, encodeULEB(uint64(len(t.results)))...)
			s = append(s, t.results...)
		}
		if len(m.types) > 0 {
			section(1, s)
		}
	}

	// 2: Import
	{
		var s []byte
		var count uint32
		var body []byte
		for _, im := range m.imports {
			count++
			body = append(body, encodeName(im.module)...)
			body = append(body, encodeName(im.name)...)
			switch im.kind {
			case 0x00:
				body = append(body, 0x00)
				body = append(body, encodeULEB(uint64(im.typeIx))...)
			case 0x02:
				body = append(body, 0x02)
				body = append(body, encodeLimits(im.memMin, im.memMax)...)
			case 0x03:
				body = append(body, 0x03, im.gValt)
				if im.gMut {
					body = append(body, 0x01)
				} else {
					body = append(body, 0x00)
				}
			}
		}
		if count > 0 {
			s = append(s, encodeULEB(uint64(count))...)
			s = append(s, body...)
			section(2, s)
		}
	}

	// 3: Function
	{
		var s []byte
		s = append(s, encodeULEB(uint64(len(m.funcs)))...)
		for _, f := range m.funcs {
			s = append(s, encodeULEB(uint64(f.typeIx))...)
		}
		if len(m.funcs) > 0 {
			section(3, s)
		}
	}

	// 5: Memory
	{
		var s []byte
		s = append(s, encodeULEB(uint64(len(m.memories)))...)
		for _, mem := range m.memories {
			s = append(s, encodeLimits(mem.min, mem.max)...)
		}
		if len(m.memories) > 0 {
			section(5, s)
		}
	}

	// 6: Global
	{
		var s []byte
		s = append(s, encodeULEB(uint64(len(m.globals)))...)
		for _, g := range m.globals {
			s = append(s, g.valt)
			if g.mut {
				s = append(s, 0x01)
			} else {
				s = append(s, 0x00)
			}
			s = append(s, g.init...)
		}
		if len(m.globals) > 0 {
			section(6, s)
		}
	}

	// 7: Export
	{
		var s []byte
		s = append(s, encodeULEB(uint64(len(m.exports)))...)
		for _, e := range m.exports {
			s = append(s, encodeName(e.name)...)
			s = append(s, e.kind)
			s = append(s, encodeULEB(uint64(e.idx))...)
		}
		if len(m.exports) > 0 {
			section(7, s)
		}
	}

	// 8: Start
	if m.start >= 0 {
		section(8, encodeULEB(uint64(m.start)))
	}

	// 10: Code
	{
		var s []byte
		s = append(s, encodeULEB(uint64(len(m.funcs)))...)
		for _, f := range m.funcs {
			var code []byte
			// Locals compressed into (count, type) runs.
			runs := compressLocals(f.localTypes)
			code = append(code, encodeULEB(uint64(len(runs)))...)
			for _, r := range runs {
				code = append(code, encodeULEB(uint64(r.count))...)
				code = append(code, r.typ)
			}
			code = append(code, f.body...)
			s = append(s, encodeULEB(uint64(len(code)))...)
			s = append(s, code...)
		}
		if len(m.funcs) > 0 {
			section(10, s)
		}
	}

	// 11: Data
	{
		var s []byte
		s = append(s, encodeULEB(uint64(len(m.datas)))...)
		for _, d := range m.datas {
			s = append(s, 0x00) // active, memory 0
			s = append(s, d.offset...)
			s = append(s, encodeULEB(uint64(len(d.bytes)))...)
			s = append(s, d.bytes...)
		}
		if len(m.datas) > 0 {
			section(11, s)
		}
	}

	return buf, nil
}

type localRun struct {
	count uint32
	typ   byte
}

func compressLocals(types []byte) []localRun {
	var runs []localRun
	for _, t := range types {
		if len(runs) > 0 && runs[len(runs)-1].typ == t {
			runs[len(runs)-1].count++
		} else {
			runs = append(runs, localRun{count: 1, typ: t})
		}
	}
	return runs
}

func encodeName(s string) []byte {
	out := encodeULEB(uint64(len(s)))
	return append(out, []byte(s)...)
}

func encodeLimits(min uint32, max int64) []byte {
	if max < 0 {
		out := []byte{0x00}
		return append(out, encodeULEB(uint64(min))...)
	}
	out := []byte{0x01}
	out = append(out, encodeULEB(uint64(min))...)
	out = append(out, encodeULEB(uint64(max))...)
	return out
}

// ---------------------------------------------------------------------------
// Opcode tables
// ---------------------------------------------------------------------------

var localOps = map[string]byte{
	"local.get": 0x20,
	"local.set": 0x21,
	"local.tee": 0x22,
}

var globalOps = map[string]byte{
	"global.get": 0x23,
	"global.set": 0x24,
}

type loadStore struct {
	opcode byte
	align  uint32 // natural alignment exponent
}

var loadStoreOps = map[string]loadStore{
	"i32.load":     {0x28, 2},
	"i64.load":     {0x29, 3},
	"f32.load":     {0x2a, 2},
	"f64.load":     {0x2b, 3},
	"i32.load8_s":  {0x2c, 0},
	"i32.load8_u":  {0x2d, 0},
	"i32.load16_s": {0x2e, 1},
	"i32.load16_u": {0x2f, 1},
	"i64.load8_s":  {0x30, 0},
	"i64.load8_u":  {0x31, 0},
	"i64.load16_s": {0x32, 1},
	"i64.load16_u": {0x33, 1},
	"i64.load32_s": {0x34, 2},
	"i64.load32_u": {0x35, 2},
	"i32.store":    {0x36, 2},
	"i64.store":    {0x37, 3},
	"f32.store":    {0x38, 2},
	"f64.store":    {0x39, 3},
	"i32.store8":   {0x3a, 0},
	"i32.store16":  {0x3b, 1},
	"i64.store8":   {0x3c, 0},
	"i64.store16":  {0x3d, 1},
	"i64.store32":  {0x3e, 2},
}

// simpleOps holds every instruction with no immediate operands.
var simpleOps = map[string]byte{
	"unreachable": 0x00,
	"nop":         0x01,
	"return":      0x0f,
	"drop":        0x1a,
	"select":      0x1b,

	// i32 comparison
	"i32.eqz":  0x45,
	"i32.eq":   0x46,
	"i32.ne":   0x47,
	"i32.lt_s": 0x48,
	"i32.lt_u": 0x49,
	"i32.gt_s": 0x4a,
	"i32.gt_u": 0x4b,
	"i32.le_s": 0x4c,
	"i32.le_u": 0x4d,
	"i32.ge_s": 0x4e,
	"i32.ge_u": 0x4f,

	// i64 comparison
	"i64.eqz":  0x50,
	"i64.eq":   0x51,
	"i64.ne":   0x52,
	"i64.lt_s": 0x53,
	"i64.lt_u": 0x54,
	"i64.gt_s": 0x55,
	"i64.gt_u": 0x56,
	"i64.le_s": 0x57,
	"i64.le_u": 0x58,
	"i64.ge_s": 0x59,
	"i64.ge_u": 0x5a,

	// f32 comparison
	"f32.eq": 0x5b,
	"f32.ne": 0x5c,
	"f32.lt": 0x5d,
	"f32.gt": 0x5e,
	"f32.le": 0x5f,
	"f32.ge": 0x60,

	// f64 comparison
	"f64.eq": 0x61,
	"f64.ne": 0x62,
	"f64.lt": 0x63,
	"f64.gt": 0x64,
	"f64.le": 0x65,
	"f64.ge": 0x66,

	// i32 arithmetic
	"i32.clz":    0x67,
	"i32.ctz":    0x68,
	"i32.popcnt": 0x69,
	"i32.add":    0x6a,
	"i32.sub":    0x6b,
	"i32.mul":    0x6c,
	"i32.div_s":  0x6d,
	"i32.div_u":  0x6e,
	"i32.rem_s":  0x6f,
	"i32.rem_u":  0x70,
	"i32.and":    0x71,
	"i32.or":     0x72,
	"i32.xor":    0x73,
	"i32.shl":    0x74,
	"i32.shr_s":  0x75,
	"i32.shr_u":  0x76,
	"i32.rotl":   0x77,
	"i32.rotr":   0x78,

	// i64 arithmetic
	"i64.clz":    0x79,
	"i64.ctz":    0x7a,
	"i64.popcnt": 0x7b,
	"i64.add":    0x7c,
	"i64.sub":    0x7d,
	"i64.mul":    0x7e,
	"i64.div_s":  0x7f,
	"i64.div_u":  0x80,
	"i64.rem_s":  0x81,
	"i64.rem_u":  0x82,
	"i64.and":    0x83,
	"i64.or":     0x84,
	"i64.xor":    0x85,
	"i64.shl":    0x86,
	"i64.shr_s":  0x87,
	"i64.shr_u":  0x88,
	"i64.rotl":   0x89,
	"i64.rotr":   0x8a,

	// f32 arithmetic
	"f32.abs":      0x8b,
	"f32.neg":      0x8c,
	"f32.ceil":     0x8d,
	"f32.floor":    0x8e,
	"f32.trunc":    0x8f,
	"f32.nearest":  0x90,
	"f32.sqrt":     0x91,
	"f32.add":      0x92,
	"f32.sub":      0x93,
	"f32.mul":      0x94,
	"f32.div":      0x95,
	"f32.min":      0x96,
	"f32.max":      0x97,
	"f32.copysign": 0x98,

	// f64 arithmetic
	"f64.abs":      0x99,
	"f64.neg":      0x9a,
	"f64.ceil":     0x9b,
	"f64.floor":    0x9c,
	"f64.trunc":    0x9d,
	"f64.nearest":  0x9e,
	"f64.sqrt":     0x9f,
	"f64.add":      0xa0,
	"f64.sub":      0xa1,
	"f64.mul":      0xa2,
	"f64.div":      0xa3,
	"f64.min":      0xa4,
	"f64.max":      0xa5,
	"f64.copysign": 0xa6,

	// conversions
	"i32.wrap_i64":        0xa7,
	"i32.trunc_f32_s":     0xa8,
	"i32.trunc_f32_u":     0xa9,
	"i32.trunc_f64_s":     0xaa,
	"i32.trunc_f64_u":     0xab,
	"i64.extend_i32_s":    0xac,
	"i64.extend_i32_u":    0xad,
	"i64.trunc_f32_s":     0xae,
	"i64.trunc_f32_u":     0xaf,
	"i64.trunc_f64_s":     0xb0,
	"i64.trunc_f64_u":     0xb1,
	"f32.convert_i32_s":   0xb2,
	"f32.convert_i32_u":   0xb3,
	"f32.convert_i64_s":   0xb4,
	"f32.convert_i64_u":   0xb5,
	"f32.demote_f64":      0xb6,
	"f64.convert_i32_s":   0xb7,
	"f64.convert_i32_u":   0xb8,
	"f64.convert_i64_s":   0xb9,
	"f64.convert_i64_u":   0xba,
	"f64.promote_f32":     0xbb,
	"i32.reinterpret_f32": 0xbc,
	"i64.reinterpret_f64": 0xbd,
	"f32.reinterpret_i32": 0xbe,
	"f64.reinterpret_i64": 0xbf,

	// sign extension
	"i32.extend8_s":  0xc0,
	"i32.extend16_s": 0xc1,
	"i64.extend8_s":  0xc2,
	"i64.extend16_s": 0xc3,
	"i64.extend32_s": 0xc4,
}
