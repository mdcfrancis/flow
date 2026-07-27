package execution

import (
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/flux"
)

// The self-hosted parser: the language's OWN parser, expressed as Flux cells that run
// live in the cluster rather than as Go. Two stages, both authored in Flux over the
// buffer capability, composed into a complete text→result parser for RPN arithmetic:
//
//	SelfHostLexerURN  (lex): read one source byte per tick, emit a token (digit value
//	                         or a negative op code) into the token buffer.
//	SelfHostParserURN (rpn): shift-reduce the token stream — shift a number, reduce an
//	                         operator (pop two, apply, push) — to a result on the stack.
//
// The Go backend (flux.Lower / flux.Compile) compiles these cells to WAT — Lower stays
// the fixed trust anchor — but the PARSE itself executes as cells. This closes the gap
// where the self-hosting was only demonstrated in tests: the parser now EXISTS as
// enrolled, inspectable, behavior-pinned-evolvable cells (see main.go seeding + the
// appgen.SelfHostParserAcceptance gate).
const (
	SelfHostLexerURN  = "urn:hdm:sys:flux-lexer"
	SelfHostParserURN = "urn:hdm:sys:flux-parser"
)

// SelfHostCap is the fixed capacity (in i32 words) of the parser's working buffers.
const SelfHostCap = 32

// The parser's dedicated working region, placed ABOVE the app-contract zone
// (0xB0000..0xC0000) and below the per-cell page window (windowBase 0x400000). The
// parser cells make no host calls, so nothing else touches these bytes during a parse,
// and a live app's contract fields are never disturbed. Exported so the acceptance
// suites (appgen) pin behavior at the exact same offsets the cells compile against.
const (
	SelfHostLexCur = 0xC0000 // lexer input cursor  (scalar)
	SelfHostOutp   = 0xC0004 // lexer output cursor (scalar)
	SelfHostRpnCur = 0xC0008 // reducer token cursor (scalar)
	SelfHostSp     = 0xC000C // reducer stack pointer (scalar)
	SelfHostSrc    = 0xC1000 // source-byte buffer
	SelfHostTok    = 0xC2000 // token buffer
	SelfHostStk    = 0xC3000 // reduce stack (result at [0])
)

// SelfHostLexerFlux is the tokenizer stage (identical to the test-proven `lex` cell):
// per tick, read one input byte and emit a token for a single digit (0-9 → its value)
// or an operator (+ - * / → -1 -2 -3 -4); skip anything else, preserving the output
// slot (Flux stores are unconditional, so skipping re-stores the slot's own value).
const SelfHostLexerFlux = `(cell lex (reads src cursor tokens outp) (writes tokens outp cursor)
  (let ([byte (at src cursor)]
        [isdigit (and (>= byte 48) (<= byte 57))]
        [isop (or (or (= byte 43) (= byte 45)) (or (= byte 42) (= byte 47)))]
        [emit (or isdigit isop)]
        [opcode (if (= byte 43) -1 (if (= byte 45) -2 (if (= byte 42) -3 -4)))]
        [tokenval (if isdigit (- byte 48) opcode)]
        [keep (at tokens outp)])
    (write
      (store tokens outp (if emit tokenval keep))
      (outp (if emit (+ outp 1) outp))
      (cursor (+ cursor 1)))))`

// SelfHostReduceFlux is the shift-reduce engine (identical to the test-proven `rpn`
// cell): per tick, SHIFT a number (token >= 0) onto the stack or REDUCE an operator
// (token < 0: pop two, apply, push). A 0 divisor (in an unused stack slot, evaluated
// by select's both-branch semantics) is guarded so it can never trap.
const SelfHostReduceFlux = `(cell rpn (reads tokens cursor stack sp) (writes stack sp cursor)
  (let ([tok (at tokens cursor)]
        [isop (< tok 0)]
        [op (neg tok)]
        [b (at stack (- sp 1))]
        [a (at stack (- sp 2))]
        [safeb (if (= b 0) 1 b)]
        [applied (if (= op 1) (+ a b) (if (= op 2) (- a b) (if (= op 3) (* a b) (/ a safeb))))]
        [sidx (if isop (- sp 2) sp)]
        [sval (if isop applied tok)])
    (write
      (store stack sidx sval)
      (sp (if isop (- sp 1) (+ sp 1)))
      (cursor (+ cursor 1)))))`

func selfHostLexLayout() flux.Layout {
	return flux.Layout{
		"src":    {Type: flux.TBuffer, Offset: SelfHostSrc, Len: SelfHostCap},
		"tokens": {Type: flux.TBuffer, Offset: SelfHostTok, Len: SelfHostCap},
		"cursor": {Type: flux.TInt, Offset: SelfHostLexCur},
		"outp":   {Type: flux.TInt, Offset: SelfHostOutp},
	}
}

func selfHostReduceLayout() flux.Layout {
	return flux.Layout{
		"tokens": {Type: flux.TBuffer, Offset: SelfHostTok, Len: SelfHostCap},
		"stack":  {Type: flux.TBuffer, Offset: SelfHostStk, Len: SelfHostCap},
		"cursor": {Type: flux.TInt, Offset: SelfHostRpnCur},
		"sp":     {Type: flux.TInt, Offset: SelfHostSp},
	}
}

// CompileSelfHostParser lowers both parser stages to WAT via the Go compiler. Lower is
// the fixed trust anchor; the parser it compiles is the evolvable cell.
func CompileSelfHostParser() (lexWAT, reduceWAT string, err error) {
	lexWAT, err = flux.Compile("m", SelfHostLexerFlux, selfHostLexLayout())
	if err != nil {
		return "", "", fmt.Errorf("self-host lexer: %w", err)
	}
	reduceWAT, err = flux.Compile("m", SelfHostReduceFlux, selfHostReduceLayout())
	if err != nil {
		return "", "", fmt.Errorf("self-host reducer: %w", err)
	}
	return lexWAT, reduceWAT, nil
}

// SelfHostParserCells returns the parser pipeline as seedable system cells (URN + WAT +
// one-line intent), so main.go can seed them alongside the combinators.
func SelfHostParserCells() ([]Combinator, error) {
	lexWAT, reduceWAT, err := CompileSelfHostParser()
	if err != nil {
		return nil, err
	}
	return []Combinator{
		{URN: SelfHostLexerURN, WAT: lexWAT,
			Intent: "Self-hosted Flux lexer: per tick read one source byte and emit a token (digit→value, +-*/→op code) into the token buffer."},
		{URN: SelfHostParserURN, WAT: reduceWAT,
			Intent: "Self-hosted Flux parser: shift-reduce the token stream (shift numbers, reduce operators) to evaluate the expression on the stack."},
	}, nil
}

var (
	selfHostOnce               sync.Once
	selfHostLexBC, selfHostRpnBC []byte
	selfHostCompileErr         error
)

func compileSelfHostBytecode() {
	cs := compiler.NewCompilerService()
	lexWAT, reduceWAT, err := CompileSelfHostParser()
	if err != nil {
		selfHostCompileErr = err
		return
	}
	la, err := cs.CompileGenotype(lexWAT)
	if err != nil || !la.SyntaxPassed {
		selfHostCompileErr = fmt.Errorf("self-host lexer wasm: %v", err)
		return
	}
	ra, err := cs.CompileGenotype(reduceWAT)
	if err != nil || !ra.SyntaxPassed {
		selfHostCompileErr = fmt.Errorf("self-host reducer wasm: %v", err)
		return
	}
	selfHostLexBC, selfHostRpnBC = la.Bytecode, ra.Bytecode
}

// RunSelfHostParse evaluates single-digit RPN arithmetic text ENTIRELY through the live
// Flux parser cells: tokenize with the lexer cell, then shift-reduce with the parser
// cell, returning the result on the stack. It loads the cells into the runtime on first
// use, and holds the runtime lock across the whole parse so it is race-free with the
// frame loop (the parser writes only its dedicated 0xC0000+ region, never an app's).
// This is the operational self-hosted parse — the language's parser running as cells.
func (rm *RuntimeManager) RunSelfHostParse(text string) (int32, error) {
	selfHostOnce.Do(compileSelfHostBytecode)
	if selfHostCompileErr != nil {
		return 0, selfHostCompileErr
	}
	if len(text) > SelfHostCap {
		return 0, fmt.Errorf("input too long: %d bytes > cap %d", len(text), SelfHostCap)
	}
	// Load (idempotent) before taking the parse lock — LoadCell locks internally.
	if err := rm.LoadCell(SelfHostLexerURN, selfHostLexBC); err != nil {
		return 0, fmt.Errorf("load lexer: %w", err)
	}
	if err := rm.LoadCell(SelfHostParserURN, selfHostRpnBC); err != nil {
		return 0, fmt.Errorf("load parser: %w", err)
	}

	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.sharedMem == nil {
		return 0, fmt.Errorf("shared memory unavailable")
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
	// Clear the working region so a prior parse can't bleed in.
	for i := uint32(0); i < SelfHostCap; i++ {
		wr(SelfHostSrc+i*4, 0)
		wr(SelfHostTok+i*4, 0)
		wr(SelfHostStk+i*4, 0)
	}
	for i, ch := range []byte(text) {
		wr(SelfHostSrc+uint32(i)*4, int32(ch))
	}
	// Stage 1: tokenize the whole input, one tick per byte.
	wr(SelfHostLexCur, 0)
	wr(SelfHostOutp, 0)
	rm.reasoningNanos = 0
	for range text {
		if _, _, _, err := rm.execTrampoline(SelfHostParserURN, SelfHostLexerURN, "run-tick", 0, 0); err != nil {
			return 0, fmt.Errorf("lex tick: %w", err)
		}
	}
	nTok := int(rd(SelfHostOutp))
	// Stage 2: shift-reduce the token stream, one tick per token.
	wr(SelfHostRpnCur, 0)
	wr(SelfHostSp, 0)
	for i := 0; i < nTok; i++ {
		if _, _, _, err := rm.execTrampoline(SelfHostParserURN, SelfHostParserURN, "run-tick", 0, 0); err != nil {
			return 0, fmt.Errorf("reduce tick: %w", err)
		}
	}
	return rd(SelfHostStk), nil
}
