package evolution

// Package evolution runs the evolution loop: the step-by-step mutation frame that
// composes the sieve, the adversarial gauntlet, fuel telemetry, and the MVCC
// commit coordinator into a single self-directed optimization loop.

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/telemetry"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// CellResolver returns the compiled phenotype bytecode for a cell URN. When
// supplied to a shadow replay it enables inter-cell dispatch (invoke-cell) so a
// dispatching baseline can be faithfully replayed for fusion/fission verification.
type CellResolver func(urn string) ([]byte, bool)

// Default shadow-replay geometry. Inbound transaction payloads are memory-
// mapped into the packet-frame region; the system-pointer registry window is
// hashed to capture the post-execution state delta.
const (
	DefaultPayloadOffset uint32 = 0x00010000 // inbound packet frame region
	DefaultStateWindow   uint32 = 0x00010000 // system-pointer registry window
)

// execResult is the observable outcome of one shadow execution slice.
type execResult struct {
	Results    []uint64
	Fuel       uint64
	LatencyNS  uint64   // wall-clock duration of the entry call
	Coverage   []uint32 // sorted function indices entered (coverage signature)
	OutputHash [32]byte // SHA-256 of the returned reference container
	StateHash  [32]byte // SHA-256 of the state window after execution
}

// replayEnv carries the controlled transaction envelope for one shadow replay:
// the inbound payload plus the monadic clock and entropy seed that make guest
// non-determinism reproducible.
type replayEnv struct {
	payloadOffset uint32
	payload       []byte
	stateWindow   uint32
	clock         uint64
	seed          [16]byte
	resolver      CellResolver           // optional; enables inter-cell dispatch in shadow
	chaos         ChaosProfile           // optional adversarial perturbations
	tracker       *telemetry.FuelTracker // set when fuel is measured; used to refund sys:* dispatch
}

// ChaosProfile perturbs a shadow replay to stress-test candidate robustness
// against real-world network anomalies.
type ChaosProfile struct {
	ClockDriftNS    int64  // added to the monadic clock (sliding-window stress)
	MemoryPages     uint32 // shared-memory pages (0 => default 100)
	DropHostCalls   bool   // inter-cell dispatch and reasoning resolve to 0
	EntropyScramble bool   // chronos entropy bytes are perturbed (no ambient-randomness dependence)
	TruncateInput   int    // bytes trimmed from the tail of the inbound payload (partial packets)
}

// Active reports whether the profile perturbs anything.
func (c ChaosProfile) Active() bool {
	return c.ClockDriftNS != 0 || c.MemoryPages != 0 || c.DropHostCalls ||
		c.EntropyScramble || c.TruncateInput != 0
}

// installHostStubs wires the standard HDM kernel host modules into a shadow
// runtime. Non-deterministic primitives are replaced by the controlled
// envelope: the chronos clock and entropy are bound to the replay's fixed
// values so identical inputs produce identical state, regardless of wall time.
func installHostStubs(ctx context.Context, r wazero.Runtime, env replayEnv) error {
	pages := uint32(100)
	if env.chaos.MemoryPages > 0 {
		pages = env.chaos.MemoryPages
	}
	memArt, err := compiler.NewCompilerService().
		CompileGenotype(fmt.Sprintf(`(module (memory (export "shared-cluster-memory") %d))`, pages))
	if err != nil {
		return fmt.Errorf("compile shared memory: %w", err)
	}
	memMod, err := r.CompileModule(ctx, memArt.Bytecode)
	if err != nil {
		return fmt.Errorf("compile shared memory module: %w", err)
	}
	if _, err := r.InstantiateModule(ctx, memMod,
		wazero.NewModuleConfig().WithName("hdm:kernel/hardware-io")); err != nil {
		return fmt.Errorf("instantiate shared memory: %w", err)
	}

	if _, err := r.NewHostModuleBuilder("hdm:kernel/cognitive-engine").
		NewFunctionBuilder().
		WithFunc(func(sysPtr, sysLen, ctxPtr, ctxLen uint32) (uint32, uint32) { return 0, 0 }).
		Export("invoke-reasoning").Instantiate(ctx); err != nil {
		return fmt.Errorf("stub cognitive-engine: %w", err)
	}
	if _, err := r.NewHostModuleBuilder("hdm:kernel/compiler-service").
		NewFunctionBuilder().
		WithFunc(func(ptr, length uint32) (uint32, uint32) { return 0, 0 }).
		Export("compile-genotype").Instantiate(ctx); err != nil {
		return fmt.Errorf("stub compiler-service: %w", err)
	}
	if _, err := r.NewHostModuleBuilder("hdm:kernel/cell-logger").
		NewFunctionBuilder().
		WithFunc(func(level, ptr, length uint32) {}).
		Export("emit-log").Instantiate(ctx); err != nil {
		return fmt.Errorf("stub cell-logger: %w", err)
	}
	if _, err := r.NewHostModuleBuilder("hdm:kernel/block-storage").
		NewFunctionBuilder().WithFunc(func(a, b uint32) (uint32, uint32) { return 0, 0 }).Export("read-block").
		NewFunctionBuilder().WithFunc(func(a, b uint32) (uint32, uint32) { return 0, 0 }).Export("write-block").
		NewFunctionBuilder().WithFunc(func(a, b uint32) (uint32, uint32) { return 0, 0 }).Export("get-ref").
		NewFunctionBuilder().WithFunc(func(a, b, c, d uint32) uint32 { return 1 }).Export("update-ref").
		Instantiate(ctx); err != nil {
		return fmt.Errorf("stub block-storage: %w", err)
	}

	// cell-dispatch: resolver-backed inter-cell invocation. With no resolver
	// (the common case) dispatch resolves to 0; with a resolver the target
	// cell is compiled and run in this shadow runtime so a dispatching baseline
	// replays faithfully (fusion/fission verification).
	depth := 0
	dispatchFn := func(dctx context.Context, m api.Module, urnPtr, urnLen, argPtr, argLen uint32) uint32 {
		if env.resolver == nil || env.chaos.DropHostCalls || depth >= 8 {
			return 0
		}
		urnBytes, ok := m.Memory().Read(urnPtr, urnLen)
		if !ok {
			return 0
		}
		urn := string(urnBytes)
		bc, ok := env.resolver(urn)
		if !ok {
			return 0
		}
		depth++
		defer func() { depth-- }()
		cm, err := r.CompileModule(dctx, bc)
		if err != nil {
			return 0
		}
		inst, err := r.InstantiateModule(dctx, cm, wazero.NewModuleConfig())
		if err != nil {
			return 0
		}
		defer inst.Close(dctx)
		fn := inst.ExportedFunction("run-tick")
		if fn == nil {
			return 0
		}
		// A SHARED, verified sys:* primitive is amortized infrastructure — refund the fuel its
		// dispatch consumed, so a cell that OFFLOADS logic to a shared primitive is not charged
		// per call site. This is what lets the optimizer prefer a shared abstraction (a queue,
		// a dict) over inlining: offloading becomes a near-pure code-size win.
		freeInfra := env.tracker != nil && strings.HasPrefix(urn, "urn:hdm:sys:")
		var beforeFuel uint64
		if freeInfra {
			beforeFuel = env.tracker.Fuel()
		}
		res, cerr := fn.Call(dctx, uint64(argPtr), uint64(argLen))
		if freeInfra {
			env.tracker.Refund(env.tracker.Fuel() - beforeFuel)
		}
		if cerr != nil || len(res) == 0 {
			return 0
		}
		return uint32(res[0])
	}
	if _, err := r.NewHostModuleBuilder("hdm:kernel/cell-dispatch").
		NewFunctionBuilder().WithFunc(dispatchFn).Export("invoke-cell").
		Instantiate(ctx); err != nil {
		return fmt.Errorf("cell-dispatch: %w", err)
	}

	// paging: no-op stubs so a STRUCTURAL refactor that reaches for private-page primitives
	// (paging.alloc/select/free) can instantiate and be verified here. The shadow sandbox has
	// one flat memory with no real paging, so a dispatched primitive operates on its window
	// (0x00400000) directly, which persists across a trajectory's steps — exactly what an
	// amortized data-structure win needs. alloc returns a fixed handle; select/free are inert.
	if _, err := r.NewHostModuleBuilder("hdm:kernel/paging").
		NewFunctionBuilder().WithFunc(func(uint32) uint32 { return 1 }).Export("alloc").
		NewFunctionBuilder().WithFunc(func(uint32) uint32 { return 1 }).Export("select").
		NewFunctionBuilder().WithFunc(func(uint32) {}).Export("free").
		Instantiate(ctx); err != nil {
		return fmt.Errorf("paging stub: %w", err)
	}

	// chronos: deterministic clock + entropy bound to the replay envelope, with
	// any chaos clock drift applied.
	clock := int64(env.clock) + env.chaos.ClockDriftNS
	seed := env.seed
	if env.chaos.EntropyScramble {
		for i := range seed {
			seed[i] ^= 0xA5 // deterministic perturbation of the entropy pool
		}
	}
	// random(): a deterministic splitmix64 stream seeded from the (possibly
	// scrambled) envelope seed, so replays are reproducible while still giving the
	// guest a varying stream. tick(): bound to the envelope clock — a fixed beat
	// per replay (deterministic, stable within a computation), unlike production
	// where the scheduler advances it per heartbeat.
	randState := binary.LittleEndian.Uint64(seed[:8])
	if _, err := r.NewHostModuleBuilder("hdm:kernel/chronos").
		NewFunctionBuilder().WithFunc(func() int64 { return clock }).Export("now-ns").
		NewFunctionBuilder().WithFunc(func() int64 { return clock }).Export("tick").
		NewFunctionBuilder().WithFunc(func(i uint32) uint32 {
		if i < uint32(len(seed)) {
			return uint32(seed[i])
		}
		return 0
	}).Export("entropy").
		NewFunctionBuilder().WithFunc(func() int64 {
		randState += 0x9E3779B97F4A7C15
		z := randState
		z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
		z = (z ^ (z >> 27)) * 0x94D049BB133111EB
		return int64(z ^ (z >> 31))
	}).Export("random").
		Instantiate(ctx); err != nil {
		return fmt.Errorf("stub chronos: %w", err)
	}
	return nil
}

// execCell runs entry with args in a shadow sandbox, hashing the default state
// window. It injects no inbound payload and uses a zero clock/entropy envelope.
func execCell(ctx context.Context, bytecode []byte, entry string, args ...uint64) (execResult, error) {
	return execReplay(ctx, bytecode, entry, replayEnv{stateWindow: DefaultStateWindow}, args...)
}

// execReplay instantiates bytecode in a fresh, fuel-instrumented shadow sandbox,
// memory-maps env.payload into the guest's (shared) memory, binds the
// deterministic clock/entropy envelope, calls entry with args, and returns the
// results, fuel consumed, a hash of the returned reference container, and a
// hash of the first env.stateWindow bytes of memory. Guest traps and host
// panics are recovered into an error so a faulty candidate can never
// destabilize the orchestrator.
func execReplay(ctx context.Context, bytecode []byte, entry string, env replayEnv, args ...uint64) (res execResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("shadow execution panic: %v", r)
		}
	}()

	tracker := &telemetry.FuelTracker{}
	ictx := telemetry.Instrument(ctx, tracker)

	r := wazero.NewRuntime(ictx)
	defer r.Close(ictx)

	env.tracker = tracker // enable sys:* dispatch refund (free shared-infra)
	if err = installHostStubs(ictx, r, env); err != nil {
		return res, err
	}

	mod, err := r.Instantiate(ictx, bytecode)
	if err != nil {
		return res, fmt.Errorf("instantiate candidate: %w", err)
	}

	mem := mod.Memory()
	payload := env.payload
	if t := env.chaos.TruncateInput; t > 0 && t < len(payload) {
		payload = payload[:len(payload)-t] // simulate a partial/short inbound packet
	}
	if len(payload) > 0 {
		if mem == nil {
			return res, fmt.Errorf("cell exposes no memory to map a %d-byte payload", len(payload))
		}
		if !mem.Write(env.payloadOffset, payload) {
			return res, fmt.Errorf("payload of %d bytes does not fit at offset 0x%X", len(payload), env.payloadOffset)
		}
	}

	fn := mod.ExportedFunction(entry)
	if fn == nil {
		return res, fmt.Errorf("entry point %q not exported", entry)
	}
	start := time.Now()
	out, err := fn.Call(ictx, args...)
	res.LatencyNS = uint64(time.Since(start).Nanoseconds())
	res.Coverage = tracker.Coverage()
	if err != nil {
		return res, fmt.Errorf("shadow trap: %w", err)
	}

	res.Results = out
	res.Fuel = tracker.Fuel()
	res.OutputHash = hashResults(out)
	if mem != nil {
		if win, ok := mem.Read(0, env.stateWindow); ok {
			res.StateHash = sha256.Sum256(win)
		}
	}
	return res, nil
}

// execTrajectory replays a SEQUENCE of run-tick inputs against a cell on ONE shared memory
// instance, so state ACCUMULATES across steps (a private-page data structure persists between
// calls). It returns each step's primary result and the TOTAL fuel over the whole sequence.
// This is what lets an AMORTIZED win register — build a structure once, read it cheaply on
// later steps — which the per-case gauntlet (fresh isolated memory per case) structurally
// cannot see. Behavior is judged by the baseline's outputs (the oracle); state is NOT hashed
// (a stateful refactor legitimately holds different memory in its private page).
func execTrajectory(ctx context.Context, bytecode []byte, entry string, env replayEnv, inputs [][]byte) (outputs []uint64, totalFuel uint64, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("trajectory execution panic: %v", r)
		}
	}()
	tracker := &telemetry.FuelTracker{}
	ictx := telemetry.Instrument(ctx, tracker)
	r := wazero.NewRuntime(ictx)
	defer r.Close(ictx)
	env.tracker = tracker // enable sys:* dispatch refund (free shared-infra)
	if err = installHostStubs(ictx, r, env); err != nil {
		return nil, 0, err
	}
	mod, err := r.Instantiate(ictx, bytecode)
	if err != nil {
		return nil, 0, fmt.Errorf("instantiate: %w", err)
	}
	mem := mod.Memory()
	fn := mod.ExportedFunction(entry)
	if fn == nil {
		return nil, 0, fmt.Errorf("entry point %q not exported", entry)
	}
	for i, payload := range inputs {
		if len(payload) > 0 {
			if mem == nil || !mem.Write(env.payloadOffset, payload) {
				return nil, 0, fmt.Errorf("payload %d (%d bytes) does not fit at 0x%X", i, len(payload), env.payloadOffset)
			}
		}
		out, cerr := fn.Call(ictx, uint64(env.payloadOffset), uint64(len(payload)))
		if cerr != nil {
			return nil, 0, fmt.Errorf("trajectory trap at step %d/%d: %w", i+1, len(inputs), cerr)
		}
		var r0 uint64
		if len(out) > 0 {
			r0 = out[0]
		}
		outputs = append(outputs, r0)
	}
	return outputs, tracker.Fuel(), nil
}

// hashResults computes a SHA-256 over the returned values, encoded big-endian,
// forming the bit-differential output signature.
func hashResults(out []uint64) [32]byte {
	buf := make([]byte, 8*len(out))
	for i, v := range out {
		binary.BigEndian.PutUint64(buf[i*8:], v)
	}
	return sha256.Sum256(buf)
}
