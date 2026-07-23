package compiler

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero"
)

// compileValid assembles WAT and fails the test on any compiler error.
func compileValid(t *testing.T, wat string) []byte {
	t.Helper()
	cs := NewCompilerService()
	art, err := cs.CompileGenotype(wat)
	if err != nil {
		t.Fatalf("compile failed: %v\ncontext: %s (line %d)", err, art.ErrorContext, art.ErrorLine)
	}
	if !art.SyntaxPassed {
		t.Fatalf("SyntaxPassed=false: %s (line %d)", art.ErrorContext, art.ErrorLine)
	}
	if len(art.Bytecode) < 8 ||
		art.Bytecode[0] != 0x00 || art.Bytecode[1] != 0x61 ||
		art.Bytecode[2] != 0x73 || art.Bytecode[3] != 0x6d {
		t.Fatalf("bad wasm magic header: % x", art.Bytecode[:min(8, len(art.Bytecode))])
	}
	return art.Bytecode
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// call1 compiles, instantiates via wazero (the validation+execution oracle),
// and calls a single exported function returning one value.
func call1(t *testing.T, wat, fn string, args ...uint64) uint64 {
	t.Helper()
	ctx := context.Background()
	bc := compileValid(t, wat)
	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)
	mod, err := r.Instantiate(ctx, bc)
	if err != nil {
		t.Fatalf("wazero rejected the binary: %v", err)
	}
	res, err := mod.ExportedFunction(fn).Call(ctx, args...)
	if err != nil {
		t.Fatalf("call %s failed: %v", fn, err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 result, got %d", len(res))
	}
	return res[0]
}

func TestArithmetic(t *testing.T) {
	wat := `(module
	  (func (export "add") (param i32 i32) (result i32)
	    local.get 0 local.get 1 i32.add)
	  (func (export "muladd") (param $a i32) (param $b i32) (result i32)
	    local.get $a local.get $b i32.mul
	    local.get $a i32.add)
	  (func (export "divrem") (param $a i32) (param $b i32) (result i32)
	    local.get $a local.get $b i32.div_s
	    local.get $a local.get $b i32.rem_s
	    i32.add))`
	if got := call1(t, wat, "add", 2, 3); got != 5 {
		t.Errorf("add(2,3)=%d want 5", got)
	}
	if got := call1(t, wat, "muladd", 4, 5); got != 24 {
		t.Errorf("muladd(4,5)=%d want 24", got)
	}
	if got := call1(t, wat, "divrem", 17, 5); got != 5 { // 17/5=3, 17%5=2, 3+2=5
		t.Errorf("divrem(17,5)=%d want 5", got)
	}
}

func TestControlFlowLoop(t *testing.T) {
	wat := `(module
	  (func (export "sum") (param $n i32) (result i32)
	    (local $i i32) (local $acc i32)
	    (block $break
	      (loop $cont
	        local.get $i local.get $n i32.ge_s
	        br_if $break
	        local.get $acc local.get $i i32.add local.set $acc
	        local.get $i i32.const 1 i32.add local.set $i
	        br $cont))
	    local.get $acc))`
	if got := call1(t, wat, "sum", 5); got != 10 {
		t.Errorf("sum(5)=%d want 10", got)
	}
	if got := call1(t, wat, "sum", 100); got != 4950 {
		t.Errorf("sum(100)=%d want 4950", got)
	}
}

func TestIfElse(t *testing.T) {
	wat := `(module
	  (func (export "max") (param $a i32) (param $b i32) (result i32)
	    local.get $a local.get $b i32.gt_s
	    if (result i32)
	      local.get $a
	    else
	      local.get $b
	    end))`
	if got := call1(t, wat, "max", 3, 7); got != 7 {
		t.Errorf("max(3,7)=%d want 7", got)
	}
	if got := call1(t, wat, "max", 9, 2); got != 9 {
		t.Errorf("max(9,2)=%d want 9", got)
	}
}

func TestBrTable(t *testing.T) {
	wat := `(module
	  (func (export "sel") (param $i i32) (result i32)
	    (block $b2
	      (block $b1
	        (block $b0
	          local.get $i
	          br_table $b0 $b1 $b2)
	        i32.const 10 return)
	      i32.const 20 return)
	    i32.const 30))`
	for _, tc := range []struct{ in, want uint64 }{{0, 10}, {1, 20}, {2, 30}, {7, 30}} {
		if got := call1(t, wat, "sel", tc.in); got != tc.want {
			t.Errorf("sel(%d)=%d want %d", tc.in, got, tc.want)
		}
	}
}

func TestMemoryLoadStore(t *testing.T) {
	wat := `(module
	  (memory (export "mem") 1)
	  (func (export "rw") (param $p i32) (param $v i32) (result i32)
	    local.get $p local.get $v i32.store
	    local.get $p i32.load)
	  (func (export "rw8") (param $p i32) (param $v i32) (result i32)
	    local.get $p local.get $v i32.store8
	    local.get $p i32.load8_u))`
	if got := call1(t, wat, "rw", 16, 42); got != 42 {
		t.Errorf("rw(16,42)=%d want 42", got)
	}
	if got := call1(t, wat, "rw8", 3, 0x1ff); got != 0xff { // truncated to a byte
		t.Errorf("rw8=%d want 255", got)
	}
}

func TestDataSegment(t *testing.T) {
	wat := `(module
	  (memory (export "mem") 1)
	  (data (i32.const 0) "\2a\00\00\00")
	  (func (export "read") (result i32)
	    i32.const 0 i32.load))`
	if got := call1(t, wat, "read"); got != 42 {
		t.Errorf("read()=%d want 42 (little-endian 0x2a)", got)
	}
}

func TestFoldedForm(t *testing.T) {
	// x*x + 1, written entirely in folded S-expression form.
	wat := `(module
	  (func (export "poly") (param $x i32) (result i32)
	    (i32.add (i32.mul (local.get $x) (local.get $x)) (i32.const 1))))`
	if got := call1(t, wat, "poly", 4); got != 17 {
		t.Errorf("poly(4)=%d want 17", got)
	}
}

func TestFoldedIf(t *testing.T) {
	wat := `(module
	  (func (export "absish") (param $x i32) (result i32)
	    (if (result i32) (i32.lt_s (local.get $x) (i32.const 0))
	      (then (i32.sub (i32.const 0) (local.get $x)))
	      (else (local.get $x)))))`
	if got := call1(t, wat, "absish", 0xfffffffb); got != 5 { // -5 -> 5
		t.Errorf("absish(-5)=%d want 5", got)
	}
	if got := call1(t, wat, "absish", 8); got != 8 {
		t.Errorf("absish(8)=%d want 8", got)
	}
}

func TestGlobal(t *testing.T) {
	wat := `(module
	  (global $g (mut i32) (i32.const 10))
	  (func (export "bump") (result i32)
	    global.get $g i32.const 5 i32.add global.set $g
	    global.get $g))`
	if got := call1(t, wat, "bump"); got != 15 {
		t.Errorf("bump()=%d want 15", got)
	}
}

func TestI64AndConversions(t *testing.T) {
	wat := `(module
	  (func (export "wideadd") (param $a i32) (result i64)
	    local.get $a i64.extend_i32_s
	    i64.const 1000000000000 i64.add))`
	if got := call1(t, wat, "wideadd", 5); got != 1000000000005 {
		t.Errorf("wideadd(5)=%d want 1000000000005", got)
	}
}

// TestBootstrapExecutes proves the compiler assembles a standards-compliant
// bootstrap cell — memory import, a multi-value host import, named locals,
// data segments, drops — into a binary wazero accepts and runs.
func TestBootstrapExecutes(t *testing.T) {
	wat := `(module
	  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
	  (import "hdm:kernel/cognitive-engine" "invoke-reasoning"
	    (func $invoke_reasoning (param i32 i32 i32 i32) (result i32 i32)))
	  (data (i32.const 0) "SYSTEM ROLE: HDM REFACTORING CORE.")
	  (func (export "run-tick") (param $urn_ptr i32) (param $urn_len i32) (result i32)
	    (local $status i32)
	    i32.const 0 i32.const 34
	    local.get $urn_ptr local.get $urn_len
	    call $invoke_reasoning
	    drop drop
	    i32.const 1))`
	ctx := context.Background()
	bc := compileValid(t, wat)
	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)
	// Provide the shared cluster memory by compiling a memory module with the
	// compiler under test (same path the hypervisor uses).
	memBC := compileValid(t, `(module (memory (export "shared-cluster-memory") 100))`)
	memCompiled, err := r.CompileModule(ctx, memBC)
	if err != nil {
		t.Fatalf("compile shared memory module: %v", err)
	}
	if _, err := r.InstantiateModule(ctx, memCompiled,
		wazero.NewModuleConfig().WithName("hdm:kernel/hardware-io")); err != nil {
		t.Fatalf("instantiate shared memory: %v", err)
	}
	if _, err := r.NewHostModuleBuilder("hdm:kernel/cognitive-engine").
		NewFunctionBuilder().
		WithFunc(func(a, b, c, d uint32) (uint32, uint32) { return 0, 0 }).
		Export("invoke-reasoning").Instantiate(ctx); err != nil {
		t.Fatalf("host cognitive-engine module: %v", err)
	}
	mod, err := r.Instantiate(ctx, bc)
	if err != nil {
		t.Fatalf("wazero rejected bootstrap: %v", err)
	}
	res, err := mod.ExportedFunction("run-tick").Call(ctx, 0, 0)
	if err != nil {
		t.Fatalf("run-tick failed: %v", err)
	}
	if res[0] != 1 {
		t.Errorf("run-tick=%d want 1", res[0])
	}
}

// TestCanonicalSpecSyntax proves the compiler fully parses & encodes the exact
// A canonical genotype: three distinct import modules, a multi-value
// (result i32 i32) import, named locals, linear if/else, and a WIT-style
// '#'-qualified export name. (Note: this verbatim artifact is stack-unbalanced
// at the WebAssembly type level — compile_genotype declares two results but the
// body consumes one — so wazero would reject it. The compiler's job is faithful
// encoding, which this asserts; the spec bug is reported separately.)
func TestCanonicalSpecSyntax(t *testing.T) {
	wat := `(module
	  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
	  (import "hdm:kernel/cognitive-engine" "invoke-reasoning"
	    (func $invoke_reasoning (param i32 i32 i32 i32) (result i32 i32)))
	  (import "hdm:kernel/compiler-service" "compile-genotype"
	    (func $compile_genotype (param i32 i32) (result i32 i32)))
	  (data (i32.const 0) "SYSTEM ROLE: HDM REFACTORING CORE. LOWER METED SYSTEM ENERGY.")
	  (func (export "hdm:kernel/bootstrap-runtime#run-annealing-tick")
	    (param $target_urn_ptr i32) (param $target_urn_len i32) (result i32)
	    (local $candidate_text_ptr i32)
	    (local $candidate_text_len i32)
	    (local $status_code i32)
	    i32.const 0 i32.const 56
	    local.get $target_urn_ptr local.get $target_urn_len
	    call $invoke_reasoning
	    local.set $candidate_text_len
	    local.set $candidate_text_ptr
	    local.get $candidate_text_ptr local.get $candidate_text_len
	    call $compile_genotype
	    local.set $status_code
	    local.get $status_code i32.const 0 i32.eq
	    if (result i32) i32.const 1 else i32.const 0 end))`
	// Compiler-level success (structural parse + encode) is the assertion here.
	compileValid(t, wat)
}
