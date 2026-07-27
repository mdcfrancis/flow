package execution

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/inference"
	"github.com/mdcfrancis/flow/manifest"
	"github.com/mdcfrancis/flow/stdlib"
	"github.com/mdcfrancis/flow/storage"
	"github.com/mdcfrancis/flow/telemetry"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// maxDispatchDepth bounds inter-cell dispatch recursion.
const maxDispatchDepth = 8

// bootstrapReasonTimeout caps how long the per-heartbeat bootstrap tick will wait
// on the cognitive engine, so a model-server outage fails this tick fast (the
// next tick retries) instead of freezing the scheduler for the full client
// reconnect window. A normal completion is well under this.
const bootstrapReasonTimeout = 15 * time.Second

// Shared-memory geometry. Host functions return data to
// guests by bump-allocating within the dynamic evolutionary sandbox region.
// Shared-memory region layout.
const (
	sharedPages    = 100
	sharedMemName  = "shared-cluster-memory"
	hardwareIOName = "hdm:kernel/hardware-io"

	registryEnd = 0x00010000 // 0x0..0xFFFF   system pointer registry (read-only)
	InboundBase = 0x00010000 // 0x10000..0x4FFFF inbound packet frame (Edge Gateway)
	InboundEnd  = 0x00050000
	InputBase   = 0x00050000 // 0x50000..0x50FFF HMI input event register desk
	InputEnd    = 0x00051000
	CanvasBase  = 0x00051000 // 0x51000..0xAFFFF Canvas UI draw-output buffer
	CanvasEnd   = 0x000B0000
	scratchBase = 0x000B0000 // 0xB0000..scratchEnd dynamic sandbox / host-return scratch
	scratchEnd  = windowBase
	// Software-paging regions (see pages.go). The window is the guest-visible flat block a
	// cell's private page is mapped into; the page zone is host-only physical page storage
	// that guests never address directly.
	windowBase   = 0x00400000 // per-cell VIRTUAL page window (guest sees a flat block here)
	windowEnd    = 0x00410000 // 64 KiB window (one mapped page at a time)
	pageZoneBase = 0x00410000 // host-managed PHYSICAL page zone
	pageZoneEnd  = sharedPages * 65536
)

// HMI Input Event Register (Peripheral Input Gateway). An unpadded,
// little-endian struct the host writes at InputBase and guest cells poll. The
// register has two zones: a CONTINUOUS-STATE zone (mouse position + held
// buttons/modifiers, overwritten by every event including mouse moves) and a
// DISCRETE-EVENT latch (the most recent non-move event — down/up/click/key —
// stamped with a monotonic sequence so a cell can detect a new event by
// comparing InEventSeq against the value it saw last tick). Keeping discrete
// events out of the continuous zone means a flood of mouse-move packets can
// never clobber an unconsumed click.
const (
	InMouseX    = InputBase + 0x00 // i32 canvas-space cursor x (0..319)
	InMouseY    = InputBase + 0x04 // i32 canvas-space cursor y (0..239)
	InButtons   = InputBase + 0x08 // u32 held-button mask: bit0 left, bit1 right, bit2 middle
	InModifiers = InputBase + 0x0C // u32 modifier mask: bit0 shift, bit1 ctrl, bit2 alt, bit3 meta
	InEventSeq  = InputBase + 0x10 // u32 monotonic discrete-event counter (0 = none yet)
	InEventType = InputBase + 0x14 // u32 last discrete event: 2 down, 3 up, 4 click, 5 keydown, 6 keyup
	InEventX    = InputBase + 0x18 // i32 cursor x at event time
	InEventY    = InputBase + 0x1C // i32 cursor y at event time
	InEventKey  = InputBase + 0x20 // u32 key code for key events (0 for mouse events)
)

// Input event type codes shared with the edge Input Gateway.
const (
	EvMove    = 1
	EvMouseDn = 2
	EvMouseUp = 3
	EvClick   = 4
	EvKeyDown = 5
	EvKeyUp   = 6
)

// RuntimeManager manages the compilation and execution of guest cells, the
// shared cluster memory, and the kernel host interfaces.
type RuntimeManager struct {
	runtime wazero.Runtime
	cells   map[string]wazero.CompiledModule
	// dispInst caches an INSTANTIATED module per dispatched URN so cell-dispatch
	// reuses one instance instead of instantiating (and closing) per call — the
	// FUSION optimization that makes a map over N elements N calls, not N
	// instantiations. dispFrom records the compiled module each instance was built
	// from, so a hot-swapped cell re-instantiates. Accessed under mu (dispatch is
	// serialized), so it needs no separate lock.
	dispInst map[string]api.Module
	dispFrom map[string]wazero.CompiledModule
	// memo caches a PURE dispatched cell's return value keyed by (urn + exact arg
	// bytes). A cell that imports ZERO host functions is a deterministic function of
	// the memory it reads; and a green composition LEAF's acceptance seeds ONLY its
	// arg (leafScenarios), so its result is a function of its arg alone — making
	// (urn, argBytes) a SOUND cache key. This is native MEMOIZATION: since everything
	// is deterministic and inputs are explicit, a map/fold whose input is unchanged
	// across frames becomes all cache hits — the escape kernel runs once per distinct
	// point, then is free. Cleared on any hot-swap (behavior change). Under mu.
	memo     map[string]uint64
	memoPure map[string]bool
	memoHit  uint64
	memoMiss uint64
	// cellMasks is the enforced read/write mask per cell: the byte ranges it may NOT
	// read (poison — zeroed for the duration of its tick) and may NOT write (revert —
	// snapshot and restored after). Applied in execTrampoline, so EVERY live execution
	// path (frame tick, async tick, render-frame, and nested dispatch inside them) is
	// governed by the OUTER cell's declared interface. Deny-by-default: a cell present
	// here with an empty allow-set is denied all shared-state access. Cells with no
	// registered mask (system/library cells) run unmasked. Registered from the AppMap.
	cellMasks    map[string]cellMask
	masksEnabled bool
	// loadedHash tracks the phenotype hash currently compiled into cells[urn], so
	// the frame loop only recompiles a cell when it has been hot-swapped. Accessed
	// under mu.
	loadedHash map[string]string
	// asyncByHash memoizes whether a phenotype imports the cognitive engine (and is
	// therefore an ASYNC cell — model latency far exceeds a frame budget, so it runs
	// off the frame thread). Keyed by phenotype hash. Accessed under mu.
	asyncByHash map[string]bool
	detByHash   map[string]bool
	dispByHash  map[string]bool
	// frames is a ring of the most recent rendered draw streams (oldest first), so
	// the vision path can look at the last N frames — a single image for a static
	// view, a sequence for motion. Accessed under mu.
	frames    [][]byte
	ledger    *storage.LedgerEngine
	repo      *manifest.Repository
	sieve     *compiler.CompilerService
	inference *inference.LocalModelClient
	// visionClient, when set at boot, serves the multimodal vision path — so the
	// visual critic can run on a vision-typed model distinct from the reasoning base.
	// Falls back to inference when unset.
	visionClient *inference.LocalModelClient
	sharedMem    api.Memory
	scratchNext  uint32
	// microTicks enrols a cell to advance N run-ticks per scheduling slot (a MICRO-TICK
	// burst) instead of one, so an iterative/stream cell (a parser, a sub-stepping
	// integrator) is not capped at one step per display frame. Absent ⇒ one tick.
	microTicks map[string]int
	// Software paging (pages.go): physical private pages + the per-cell window mapping.
	pages       []pageRec         // host-managed physical pages in the page zone
	pageNext    uint32            // bump pointer within the page zone
	mapped      map[string]uint32 // urn -> STANDING handle mapped into its window each tick
	pendingPage uint32            // guest-selected page for the NEXT dispatch (paging.select)

	dispatch    int    // current inter-cell dispatch depth
	inputSeq    uint32 // monotonic discrete-input-event counter (HMI register)
	inputActive bool   // set once any peripheral event has arrived
	// reasoningNanos accumulates wall time spent in the cognitive-engine host
	// call during the current top-level trampoline, so callers can subtract that
	// external I/O wait and report the cell's own (guest) latency.
	reasoningNanos int64
	// reasoningGate, when set, short-circuits the cognitive-engine host call to
	// (0,0) without contacting the model. The scheduler raises it once the system
	// has converged, so a per-tick reasoning cell stops burning tokens deriving
	// that there is no work to do; it is lowered when fresh work appears.
	reasoningGate bool
	// tickCount is the monadic monotonic heartbeat counter exposed to guests via
	// chronos.tick(): a fixed timing beat the scheduler advances once per heartbeat
	// (AdvanceTick), independent of wall-clock jitter. Atomic — read from guest
	// execution (under mu) and advanced from the scheduler goroutine (without mu).
	tickCount int64
	// randState is the splitmix64 state behind chronos.random(): a real entropy
	// stream seeded from the boot seed. Mutated only from guest execution, which is
	// serialized by mu, so it needs no separate lock.
	randState uint64
	// growthActive counts in-flight application growth/scaffold operations. While
	// > 0 the Gen-0 bootstrap optimizer YIELDS the (single) model — its per-tick
	// reasoning is short-circuited so growth's model calls are not starved. Atomic.
	growthActive int32
	mu           sync.Mutex // serializes trampoline execution (loop + gateway + input)
	// fuel is a persistent tracker; the listener factory (installed at compile
	// time via ictx) increments it, and each ExecuteTrampoline resets it to
	// measure a single invocation.
	fuel *telemetry.FuelTracker
	ictx context.Context
}

// NewRuntimeManager creates a wazero runtime, allocates the shared cluster
// memory, and instantiates the kernel host modules: block-storage,
// cognitive-engine, compiler-service, and cell-logger.
func NewRuntimeManager(ctx context.Context, ledger *storage.LedgerEngine, model *inference.LocalModelClient) (*RuntimeManager, error) {
	rm := &RuntimeManager{
		cells:     make(map[string]wazero.CompiledModule),
		ledger:    ledger,
		repo:      manifest.NewRepository(ledger),
		sieve:     compiler.NewCompilerService(),
		inference: model,
		fuel:      &telemetry.FuelTracker{},
	}
	// Install the fuel-accounting listener factory at compile time so every
	// compiled cell is instrumented; per-call counts come from resetting fuel.
	rm.ictx = telemetry.Instrument(ctx, rm.fuel)
	r := wazero.NewRuntimeWithConfig(rm.ictx, wazero.NewRuntimeConfigCompiler())
	rm.runtime = r

	// Allocate the shared cluster memory by compiling a memory module with the
	// native WAT compiler and instantiating it under the hardware-io module.
	memArtifact, err := rm.sieve.CompileGenotype(
		stdlib.MustTemplate("mem-export", map[string]any{"Name": sharedMemName, "Pages": sharedPages}))
	if err != nil {
		_ = r.Close(ctx)
		return nil, fmt.Errorf("failed to compile shared memory module: %w", err)
	}
	compiledMem, err := r.CompileModule(rm.ictx, memArtifact.Bytecode)
	if err != nil {
		_ = r.Close(ctx)
		return nil, fmt.Errorf("failed to compile memory in wazero: %w", err)
	}
	memInst, err := r.InstantiateModule(rm.ictx, compiledMem, wazero.NewModuleConfig().WithName(hardwareIOName))
	if err != nil {
		_ = r.Close(ctx)
		return nil, fmt.Errorf("failed to instantiate hardware-io: %w", err)
	}
	rm.sharedMem = memInst.ExportedMemory(sharedMemName)
	if rm.sharedMem == nil {
		_ = r.Close(ctx)
		return nil, fmt.Errorf("shared cluster memory not exported")
	}

	if err := rm.registerHostModules(rm.ictx); err != nil {
		_ = r.Close(ctx)
		return nil, err
	}

	return rm, nil
}

// registerHostModules wires the kernel host interfaces the core-runtime world
// imports, each using the shared cluster memory for its reference-container ABI.
func (rm *RuntimeManager) registerHostModules(ctx context.Context) error {
	i32 := api.ValueTypeI32

	// hdm:kernel/block-storage
	_, err := rm.runtime.NewHostModuleBuilder("hdm:kernel/block-storage").
		NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(rm.hostReadBlock),
			[]api.ValueType{i32, i32}, []api.ValueType{i32, i32}).Export("read-block").
		NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(rm.hostWriteBlock),
			[]api.ValueType{i32, i32}, []api.ValueType{i32, i32}).Export("write-block").
		NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(rm.hostGetRef),
			[]api.ValueType{i32, i32}, []api.ValueType{i32, i32}).Export("get-ref").
		NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(rm.hostUpdateRef),
			[]api.ValueType{i32, i32, i32, i32}, []api.ValueType{i32}).Export("update-ref").
		Instantiate(ctx)
	if err != nil {
		return fmt.Errorf("register block-storage: %w", err)
	}

	// hdm:kernel/cognitive-engine
	_, err = rm.runtime.NewHostModuleBuilder("hdm:kernel/cognitive-engine").
		NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(rm.hostInvokeReasoning),
			[]api.ValueType{i32, i32, i32, i32}, []api.ValueType{i32, i32}).Export("invoke-reasoning").
		Instantiate(ctx)
	if err != nil {
		return fmt.Errorf("register cognitive-engine: %w", err)
	}

	// hdm:kernel/vision-engine — the vision path: ask the multimodal model a
	// question about the recent rendered frames (see hostInvokeVision).
	_, err = rm.runtime.NewHostModuleBuilder("hdm:kernel/vision-engine").
		NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(rm.hostInvokeVision),
			[]api.ValueType{i32, i32, i32, i32}, []api.ValueType{i32, i32}).Export("invoke-vision").
		Instantiate(ctx)
	if err != nil {
		return fmt.Errorf("register vision-engine: %w", err)
	}

	// hdm:kernel/compiler-service
	_, err = rm.runtime.NewHostModuleBuilder("hdm:kernel/compiler-service").
		NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(rm.hostCompileGenotype),
			[]api.ValueType{i32, i32}, []api.ValueType{i32, i32}).Export("compile-genotype").
		Instantiate(ctx)
	if err != nil {
		return fmt.Errorf("register compiler-service: %w", err)
	}

	// hdm:kernel/cell-logger
	_, err = rm.runtime.NewHostModuleBuilder("hdm:kernel/cell-logger").
		NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(rm.hostEmitLog),
			[]api.ValueType{i32, i32, i32}, nil).Export("emit-log").
		Instantiate(ctx)
	if err != nil {
		return fmt.Errorf("register cell-logger: %w", err)
	}

	// hdm:kernel/cell-dispatch — inter-cell composition: a cell invokes another
	// by URN across the host bridge (the barrier Fusion later collapses).
	_, err = rm.runtime.NewHostModuleBuilder("hdm:kernel/cell-dispatch").
		NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(rm.hostInvokeCell),
			[]api.ValueType{i32, i32, i32, i32}, []api.ValueType{i32}).Export("invoke-cell").
		Instantiate(ctx)
	if err != nil {
		return fmt.Errorf("register cell-dispatch: %w", err)
	}

	// hdm:kernel/paging — guest-facing private memory. alloc(size)->handle creates a private
	// page; select(handle)->ok picks the page the NEXT dispatched op runs against (so one cell
	// can drive several instances); free(handle) releases it. Selection/mapping is the only
	// guest surface — the physical base is never exposed.
	_, err = rm.runtime.NewHostModuleBuilder("hdm:kernel/paging").
		NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(rm.hostPageAlloc),
		[]api.ValueType{i32}, []api.ValueType{i32}).Export("alloc").
		NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(rm.hostPageSelect),
		[]api.ValueType{i32}, []api.ValueType{i32}).Export("select").
		NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(rm.hostPageFree),
		[]api.ValueType{i32}, []api.ValueType{}).Export("free").
		Instantiate(ctx)
	if err != nil {
		return fmt.Errorf("register paging: %w", err)
	}

	// hdm:kernel/chronos — production clock + boot-seeded entropy. Guests must
	// route non-deterministic reads through here; shadow replay rebinds these
	// to a tape's fixed envelope for reproducibility.
	var seed [16]byte
	_, _ = rand.Read(seed[:])
	rm.randState = binary.LittleEndian.Uint64(seed[:8]) // seed the random() stream
	_, err = rm.runtime.NewHostModuleBuilder("hdm:kernel/chronos").
		NewFunctionBuilder().WithFunc(func() int64 { return time.Now().UnixNano() }).Export("now-ns").
		// tick: the monotonic heartbeat counter — a fixed timing beat for cells
		// (e.g. "advance the fleet every 8 ticks") that does not depend on wall time.
		NewFunctionBuilder().WithFunc(func() int64 { return atomic.LoadInt64(&rm.tickCount) }).Export("tick").
		NewFunctionBuilder().WithFunc(func(idx uint32) uint32 {
		if idx < uint32(len(seed)) {
			return uint32(seed[idx])
		}
		return 0
	}).Export("entropy").
		// random: a real pseudo-random stream (splitmix64) seeded from the boot
		// entropy — successive calls return different values, unlike entropy().
		NewFunctionBuilder().WithFunc(func() int64 { return int64(rm.nextRandom()) }).Export("random").
		Instantiate(ctx)
	if err != nil {
		return fmt.Errorf("register chronos: %w", err)
	}
	return nil
}

// nextRandom advances the splitmix64 stream behind chronos.random(). Called only
// from guest execution, which mu serializes, so it needs no atomic.
func (rm *RuntimeManager) nextRandom() uint64 {
	rm.randState += 0x9E3779B97F4A7C15
	z := rm.randState
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// AdvanceTick increments the monadic heartbeat counter exposed via chronos.tick().
// The scheduler calls it once per heartbeat so cells share a fixed timing beat.
func (rm *RuntimeManager) AdvanceTick() int64 { return atomic.AddInt64(&rm.tickCount, 1) }

// Tick reports the current heartbeat counter.
func (rm *RuntimeManager) Tick() int64 { return atomic.LoadInt64(&rm.tickCount) }

// BeginGrowth / EndGrowth bracket an application growth or scaffold operation so
// the Gen-0 bootstrap optimizer yields the model to it (see hostInvokeReasoning).
// Nestable; the yield lifts only when the last in-flight growth ends.
func (rm *RuntimeManager) BeginGrowth() { atomic.AddInt32(&rm.growthActive, 1) }
func (rm *RuntimeManager) EndGrowth()   { atomic.AddInt32(&rm.growthActive, -1) }

// GrowthActive reports whether any application growth is in flight.
func (rm *RuntimeManager) GrowthActive() bool { return atomic.LoadInt32(&rm.growthActive) > 0 }

// --- shared-memory ABI helpers -------------------------------------------------

func (rm *RuntimeManager) resetScratch() { rm.scratchNext = scratchBase }

// alloc reserves n (8-byte-aligned) bytes in the scratch arena.
func (rm *RuntimeManager) alloc(n uint32) (uint32, bool) {
	a := (n + 7) &^ uint32(7)
	if rm.scratchNext+a > scratchEnd {
		return 0, false
	}
	off := rm.scratchNext
	rm.scratchNext += a
	return off, true
}

// putScratch copies data into the scratch arena and returns its (ptr, len).
func (rm *RuntimeManager) putScratch(data []byte) (uint32, uint32, bool) {
	if len(data) == 0 {
		return 0, 0, true
	}
	off, ok := rm.alloc(uint32(len(data)))
	if !ok || !rm.sharedMem.Write(off, data) {
		return 0, 0, false
	}
	return off, uint32(len(data)), true
}

func (rm *RuntimeManager) read(ptr, length uint32) ([]byte, bool) {
	if rm.sharedMem == nil {
		return nil, false
	}
	return rm.sharedMem.Read(ptr, length)
}

// --- kernel host functions -----------------------------------------------------

func (rm *RuntimeManager) hostReadBlock(_ context.Context, _ api.Module, stack []uint64) {
	hash, ok := rm.read(uint32(stack[0]), uint32(stack[1]))
	if !ok {
		stack[0], stack[1] = 0, 0
		return
	}
	data, err := rm.ledger.ReadBlock(string(hash))
	if err != nil {
		stack[0], stack[1] = 0, 0
		return
	}
	ptr, length, ok := rm.putScratch(data)
	if !ok {
		stack[0], stack[1] = 0, 0
		return
	}
	stack[0], stack[1] = uint64(ptr), uint64(length)
}

func (rm *RuntimeManager) hostWriteBlock(_ context.Context, _ api.Module, stack []uint64) {
	data, ok := rm.read(uint32(stack[0]), uint32(stack[1]))
	if !ok {
		stack[0], stack[1] = 0, 0
		return
	}
	// Copy: the returned slice aliases the shared buffer, which we're about to
	// write into via putScratch.
	payload := append([]byte(nil), data...)
	hash, err := rm.ledger.WriteBlock(payload)
	if err != nil {
		stack[0], stack[1] = 0, 0
		return
	}
	ptr, length, ok := rm.putScratch([]byte(hash))
	if !ok {
		stack[0], stack[1] = 0, 0
		return
	}
	stack[0], stack[1] = uint64(ptr), uint64(length)
}

func (rm *RuntimeManager) hostGetRef(_ context.Context, _ api.Module, stack []uint64) {
	urn, ok := rm.read(uint32(stack[0]), uint32(stack[1]))
	if !ok {
		stack[0], stack[1] = 0, 0
		return
	}
	hash, err := rm.ledger.GetRef(string(urn))
	if err != nil {
		stack[0], stack[1] = 0, 0
		return
	}
	ptr, length, ok := rm.putScratch([]byte(hash))
	if !ok {
		stack[0], stack[1] = 0, 0
		return
	}
	stack[0], stack[1] = uint64(ptr), uint64(length)
}

func (rm *RuntimeManager) hostUpdateRef(_ context.Context, _ api.Module, stack []uint64) {
	urn, ok1 := rm.read(uint32(stack[0]), uint32(stack[1]))
	hash, ok2 := rm.read(uint32(stack[2]), uint32(stack[3]))
	if !ok1 || !ok2 {
		stack[0] = 1
		return
	}
	if err := rm.ledger.UpdateRef(string(urn), string(hash)); err != nil {
		stack[0] = 1
		return
	}
	stack[0] = 0
}

func (rm *RuntimeManager) hostInvokeReasoning(ctx context.Context, _ api.Module, stack []uint64) {
	if rm.reasoningGate || atomic.LoadInt32(&rm.growthActive) > 0 {
		// Gated (converged) or yielding to in-flight application growth: the
		// bootstrap optimizer does not contact the model, freeing it for the app.
		stack[0], stack[1] = 0, 0
		return
	}
	sys, ok1 := rm.read(uint32(stack[0]), uint32(stack[1]))
	usr, ok2 := rm.read(uint32(stack[2]), uint32(stack[3]))
	if !ok1 || !ok2 || rm.inference == nil {
		stack[0], stack[1] = 0, 0
		return
	}
	// Bound the per-tick bootstrap reasoning: it is frequent and low-priority, so
	// it must NOT block the scheduler for the whole client reconnect window when
	// the model is down — fail fast and let the next tick try. Growth/evolution
	// call the client directly with the app context and keep the full reconnect.
	rctx, cancel := context.WithTimeout(ctx, bootstrapReasonTimeout)
	defer cancel()
	t0 := time.Now()
	resp, err := rm.invokeReasoningUnlocked(rctx, string(sys), string(usr))
	rm.reasoningNanos += time.Since(t0).Nanoseconds()
	if err != nil {
		stack[0], stack[1] = 0, 0
		return
	}
	ptr, length, ok := rm.putScratch([]byte(resp))
	if !ok {
		stack[0], stack[1] = 0, 0
		return
	}
	stack[0], stack[1] = uint64(ptr), uint64(length)
}

// invokeReasoningUnlocked performs the (multi-second) cognitive-engine round-trip
// WITHOUT holding rm.mu, so regular ticks — canvas render, HMI input, cell
// dispatch — are not blocked behind the model. This is safe because the calling
// guest is parked in Go here (not executing WASM or touching shared memory) for
// the duration; only ONE other trampoline can run in the released window (they
// all still contend on rm.mu), so there is never concurrent memory access. The
// lock is re-acquired (even on panic) before returning, so the caller's balanced
// defer Unlock and the subsequent putScratch both run under the lock as before.
func (rm *RuntimeManager) invokeReasoningUnlocked(ctx context.Context, sys, usr string) (string, error) {
	rm.mu.Unlock()
	defer rm.mu.Lock()
	return rm.inference.InvokeReasoning(ctx, sys, usr)
}

func (rm *RuntimeManager) hostCompileGenotype(_ context.Context, _ api.Module, stack []uint64) {
	wat, ok := rm.read(uint32(stack[0]), uint32(stack[1]))
	if !ok {
		stack[0], stack[1] = 0, 0
		return
	}
	art, err := rm.sieve.CompileGenotype(string(wat))
	if err != nil || !art.SyntaxPassed {
		stack[0], stack[1] = 0, 0
		return
	}
	ptr, length, ok := rm.putScratch(art.Bytecode)
	if !ok {
		stack[0], stack[1] = 0, 0
		return
	}
	stack[0], stack[1] = uint64(ptr), uint64(length)
}

// hostInvokeCell resolves the target cell by URN, instantiates its phenotype in
// the shared runtime, and calls its run-tick entry with the given argument
// container — an inter-cell call across the host bridge. Returns 0 on any error
// or if the recursion depth guard trips.
func (rm *RuntimeManager) hostInvokeCell(ctx context.Context, caller api.Module, stack []uint64) {
	urnBytes, ok := rm.read(uint32(stack[0]), uint32(stack[1]))
	if !ok {
		stack[0] = 0
		return
	}
	argPtr, argLen := uint32(stack[2]), uint32(stack[3])

	if rm.dispatch >= maxDispatchDepth {
		stack[0] = 0
		return
	}
	rm.dispatch++
	defer func() { rm.dispatch-- }()

	compiled, err := rm.resolveCell(ctx, string(urnBytes))
	if err != nil {
		stack[0] = 0
		return
	}
	// FUSION: reuse a cached instance instead of instantiating (and closing) per
	// call — turning a map/fold over N elements from N instantiations into N cheap
	// calls. Cells are stateless over the shared memory, so one instance reuses safely.
	mod, err := rm.dispatchInstance(ctx, string(urnBytes), compiled)
	if err != nil {
		stack[0] = 0
		return
	}
	fn := mod.ExportedFunction("run-tick")
	if fn == nil {
		stack[0] = 0
		return
	}
	// MEMOIZATION: a cell with zero host-function imports is a deterministic
	// function of the memory it reads. Keyed on the exact arg bytes (which, for a
	// composition leaf, is its whole input by ABI), a repeat call is a cache hit —
	// so an unchanged map/fold across frames costs nothing after the first pass.
	// Gated to VALUE-SEMANTIC combinator callers (map/fold/…): they pass element
	// values to write-nothing leaves, so caching the return is complete. Streams and
	// iterate pass mutable state / write side effects, so they are never memoized.
	memoKey := ""
	if isValueCombinator(caller) {
		memoKey = rm.memoKeyFor(string(urnBytes), compiled, argPtr, argLen)
	}
	if memoKey != "" {
		if rv, hit := rm.memo[memoKey]; hit {
			rm.memoHit++
			stack[0] = rv
			return
		}
		rm.memoMiss++
	}
	// DISPATCH-PATH PAGING: pick the callee's page — a guest-SELECTED page (paging.select,
	// one-shot, for per-instance ops) takes priority over the callee's STANDING mapping. If a
	// page is chosen, context-switch the window: save the caller's window, page the callee's
	// page in, run, then page out and restore the caller's window. Callees with no page run
	// exactly as before with zero overhead.
	calleeURN := string(urnBytes)
	pageHandle := rm.mapped[calleeURN]
	if rm.pendingPage != 0 {
		pageHandle = rm.pendingPage
		rm.pendingPage = 0 // consumed by this dispatch
	}
	pg := rm.pageRecByHandle(pageHandle)
	var winSave []byte
	if pg != nil {
		if b, ok := rm.read(windowBase, windowEnd-windowBase); ok {
			winSave = make([]byte, len(b))
			copy(winSave, b)
		}
		rm.pageInRec(pg)
	}
	res, err := fn.Call(ctx, uint64(argPtr), uint64(argLen))
	if err != nil || len(res) == 0 {
		if pg != nil && winSave != nil {
			rm.sharedMem.Write(windowBase, winSave) // restore caller window; don't persist a failed callee
		}
		stack[0] = 0
		return
	}
	if pg != nil {
		rm.pageOutRec(pg) // persist the callee's page
		if winSave != nil {
			rm.sharedMem.Write(windowBase, winSave) // restore the caller's window
		}
	}
	stack[0] = res[0]
	if memoKey != "" {
		if rm.memo == nil {
			rm.memo = map[string]uint64{}
		}
		if len(rm.memo) >= memoCap { // bounded: drop wholesale on overflow (e.g. a panning view)
			rm.memo = map[string]uint64{}
		}
		rm.memo[memoKey] = res[0]
	}
}

const memoCap = 262144

// dispName is the instance name given to a dispatched cell, so a callee can identify
// its caller (see isValueCombinator).
func dispName(urn string) string { return "disp:" + urn }

// valueCombinators are the combinators that apply a leaf as a pure VALUE→VALUE
// function (passing the element value, expecting a returned value, no side effects) —
// the only callers for which return-only memoization is sound. Streams (mutable state)
// and iterate (evolving accumulator with a mutating step) are deliberately excluded.
var valueCombinators = map[string]bool{
	dispName(SysMapURN):    true,
	dispName(SysFoldURN):   true,
	dispName(SysFilterURN): true,
	dispName(SysScanURN):   true,
	dispName(SysZipURN):    true,
}

// isValueCombinator reports whether the calling module is a value-semantic combinator,
// making a memoized dispatch of its leaf sound.
func isValueCombinator(caller api.Module) bool {
	return caller != nil && valueCombinators[caller.Name()]
}

// memoKeyFor returns a sound memo key (urn + exact arg bytes) for a PURE dispatched
// cell — one importing zero host functions, hence a deterministic function of the
// memory it reads — with a small, bounded argument. Returns "" when the call is not
// safely memoizable (impure, or an unbounded/empty arg).
func (rm *RuntimeManager) memoKeyFor(urn string, compiled wazero.CompiledModule, argPtr, argLen uint32) string {
	if argLen == 0 || argLen > 256 {
		return ""
	}
	if rm.memoPure == nil {
		rm.memoPure = map[string]bool{}
	}
	pure, known := rm.memoPure[urn]
	if !known {
		pure = len(compiled.ImportedFunctions()) == 0
		rm.memoPure[urn] = pure
	}
	if !pure {
		return ""
	}
	arg, ok := rm.read(argPtr, argLen)
	if !ok {
		return ""
	}
	return urn + "\x00" + string(arg)
}

// MemoStats reports cumulative dispatch-memoization hits and misses.
func (rm *RuntimeManager) MemoStats() (hits, misses uint64) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return rm.memoHit, rm.memoMiss
}

// dispatchInstance returns a cached, reusable instance of a dispatched cell —
// instantiating it once (per compiled module) instead of per call. Re-instantiates
// when the compiled module changed (a hot-swap). Caller holds rm.mu.
func (rm *RuntimeManager) dispatchInstance(ctx context.Context, urn string, compiled wazero.CompiledModule) (api.Module, error) {
	if rm.dispInst == nil {
		rm.dispInst = map[string]api.Module{}
		rm.dispFrom = map[string]wazero.CompiledModule{}
	}
	if inst, ok := rm.dispInst[urn]; ok && rm.dispFrom[urn] == compiled {
		return inst, nil
	}
	if old, ok := rm.dispInst[urn]; ok {
		_ = old.Close(ctx)
		// The cell's behavior changed: drop memoized results and its purity verdict.
		rm.memo = nil
		delete(rm.memoPure, urn)
	}
	// Name the instance "disp:<urn>" so a callee can see WHICH cell dispatched it
	// (used to gate memoization to value-semantic combinators). Unique per URN, and
	// the old one is closed above before re-instantiating, so names never collide.
	inst, err := rm.runtime.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithName(dispName(urn)))
	if err != nil {
		return nil, err
	}
	rm.dispInst[urn] = inst
	rm.dispFrom[urn] = compiled
	return inst, nil
}

// resolveCell returns the compiled phenotype for a cell URN, compiling and
// caching it on first use.
func (rm *RuntimeManager) resolveCell(ctx context.Context, urn string) (wazero.CompiledModule, error) {
	if c, ok := rm.cells[urn]; ok {
		return c, nil
	}
	desc, err := rm.repo.Load(urn)
	if err != nil {
		return nil, err
	}
	bytecode, err := rm.repo.Phenotype(desc)
	if err != nil {
		return nil, err
	}
	compiled, err := rm.runtime.CompileModule(ctx, bytecode)
	if err != nil {
		return nil, err
	}
	rm.cells[urn] = compiled
	return compiled, nil
}

var logLevels = [...]string{"DEBUG", "INFO", "WARN", "ERROR"}

func (rm *RuntimeManager) hostEmitLog(_ context.Context, _ api.Module, stack []uint64) {
	level := uint32(stack[0])
	msg, ok := rm.read(uint32(stack[1]), uint32(stack[2]))
	if !ok {
		return
	}
	name := "INFO"
	if level < uint32(len(logLevels)) {
		name = logLevels[level]
	}
	log.Printf("[CELL %s] %s", name, string(msg))
}

// --- lifecycle -----------------------------------------------------------------

// LoadCell compiles WASM bytecode and stores it in the in-memory cell registry.
// Compilation uses the instrumented context so the fuel listener is attached.
func (rm *RuntimeManager) LoadCell(urn string, wasmBytecode []byte) error {
	compiled, err := rm.runtime.CompileModule(rm.ictx, wasmBytecode)
	if err != nil {
		return fmt.Errorf("failed to compile cell %s: %w", urn, err)
	}
	// Guard rm.cells: the frame loop (TickAppCell) and RenderFrame also write it,
	// on other goroutines. Go maps are not safe for concurrent write.
	rm.mu.Lock()
	rm.cells[urn] = compiled
	rm.mu.Unlock()
	return nil
}

// TickAppCell resolves targetURN to its CURRENT phenotype (recompiling only when
// the hash changed, i.e. after a hot-swap) and runs run-tick on LIVE shared memory,
// all under the runtime lock — so the 30 FPS frame loop can advance the app's state
// safely alongside evolution, RenderFrame, and the input gateway. The caller passes
// the freshly-loaded phenotype (hash + bytecode) it read from the manifest; nothing
// is recompiled while the hash is unchanged.
func (rm *RuntimeManager) TickAppCell(sourceURN, targetURN, phenotypeHash string, bytecode []byte) (res uint32, fuel uint64, tokens uint64, err error) {
	return rm.TickAppCellN(sourceURN, targetURN, phenotypeHash, bytecode, 1)
}

// TickAppCellN runs a cell's run-tick n times in a SINGLE lock acquisition — a MICRO-TICK
// BURST. It decouples a cell's internal step rate from the frame rate: an iterative or
// stream cell (a parser consuming a token buffer, a physics integrator sub-stepping) can
// advance many steps per scheduling slot instead of one per display frame. State persists
// across the inner ticks exactly as it does across frames. Returns the LAST tick's result
// and the SUMMED fuel/tokens; n<=1 behaves like a single tick. A trap on any inner tick
// stops the burst and returns the error (the writes of the ticks before it persist).
func (rm *RuntimeManager) TickAppCellN(sourceURN, targetURN, phenotypeHash string, bytecode []byte, n int) (res uint32, fuel uint64, tokens uint64, err error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.loadedHash == nil {
		rm.loadedHash = map[string]string{}
	}
	if rm.loadedHash[targetURN] != phenotypeHash || rm.cells[targetURN] == nil {
		compiled, cerr := rm.runtime.CompileModule(rm.ictx, bytecode)
		if cerr != nil {
			return 0, 0, 0, fmt.Errorf("compile cell %s: %w", targetURN, cerr)
		}
		rm.cells[targetURN] = compiled
		rm.loadedHash[targetURN] = phenotypeHash
	}
	return rm.burstLocked(sourceURN, targetURN, n)
}

// burstLocked is the shared micro-tick engine: run run-tick n times, summing fuel/tokens
// and returning the last result. The caller MUST hold rm.mu and the target cell MUST be
// loaded. Used by TickAppCellN (per-frame enrollment) and the on-demand parser bursts.
func (rm *RuntimeManager) burstLocked(sourceURN, targetURN string, n int) (res uint32, fuel uint64, tokens uint64, err error) {
	if n < 1 {
		n = 1
	}
	for i := 0; i < n; i++ {
		rm.reasoningNanos = 0
		r, f, tk, e := rm.execTrampoline(sourceURN, targetURN, "run-tick", 0, 0)
		res, fuel, tokens = r, fuel+f, tokens+tk
		if e != nil {
			return res, fuel, tokens, e
		}
	}
	return res, fuel, tokens, nil
}

// EnrollMicroTick enrols a cell to advance n run-ticks per frame (a micro-tick burst),
// so an iterative/stream cell is not capped at one step per display frame. n<=1 clears
// the enrollment (normal one-tick cadence).
func (rm *RuntimeManager) EnrollMicroTick(urn string, n int) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.microTicks == nil {
		rm.microTicks = map[string]int{}
	}
	if n <= 1 {
		delete(rm.microTicks, urn)
		return
	}
	rm.microTicks[urn] = n
}

// MicroTicksFor reports how many ticks per frame a cell is enrolled to run (default 1).
func (rm *RuntimeManager) MicroTicksFor(urn string) int {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if n, ok := rm.microTicks[urn]; ok && n > 1 {
		return n
	}
	return 1
}

// cellMask is a cell's enforced read/write mask over shared memory: absolute
// [offset,len] byte ranges it may NOT read (poison) and may NOT write (revert).
type cellMask struct {
	poison [][2]uint32
	revert [][2]uint32
}

// SetCellMask registers (or with nil,nil clears) a cell's enforced read/write mask.
// execTrampoline applies it to every live execution of the cell. Deny-by-default is the
// caller's responsibility: pass poison/revert that cover every field the cell did NOT
// explicitly declare, so undeclared access is hidden (reads) or undone (writes).
func (rm *RuntimeManager) SetCellMask(urn string, poison, revert [][2]uint32) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.cellMasks == nil {
		rm.cellMasks = map[string]cellMask{}
	}
	if poison == nil && revert == nil {
		delete(rm.cellMasks, urn)
		return
	}
	rm.cellMasks[urn] = cellMask{poison: poison, revert: revert}
}

// SetMasksEnabled turns mask enforcement on or off globally.
func (rm *RuntimeManager) SetMasksEnabled(on bool) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.masksEnabled = on
}

// IsAsyncCell reports whether a phenotype imports the cognitive engine and is thus
// an ASYNC cell: its run-tick may make a multi-second model call, so it must run on
// the async worker off the frame thread, not in the 30 FPS sync loop. Compiled once
// per phenotype hash and memoized. Fail-open to sync (false) on a compile error.
func (rm *RuntimeManager) IsAsyncCell(phenotypeHash string, bytecode []byte) bool {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.asyncByHash == nil {
		rm.asyncByHash = map[string]bool{}
	}
	if v, ok := rm.asyncByHash[phenotypeHash]; ok {
		return v
	}
	async := false
	if cm, err := rm.runtime.CompileModule(rm.ictx, bytecode); err == nil {
		for _, f := range cm.ImportedFunctions() {
			if mod, _, ok := f.Import(); ok && mod == "hdm:kernel/cognitive-engine" {
				async = true
				break
			}
		}
		_ = cm.Close(rm.ictx)
	}
	rm.asyncByHash[phenotypeHash] = async
	return async
}

// ExecuteTrampoline executes an exported function of a compiled guest module,
// returning its result and the execution fuel consumed (measured via a wazero
// function-call listener installed for this invocation).
func (rm *RuntimeManager) ExecuteTrampoline(sourceURN, targetURN, targetFunc string, ptr, length uint32) (res uint32, fuel uint64, tokens uint64, err error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.reasoningNanos = 0 // reset host-call accumulator for this top-level call tree
	return rm.execTrampoline(sourceURN, targetURN, targetFunc, ptr, length)
}

// LastReasoningNanos reports wall time spent in the cognitive-engine host call
// during the most recent ExecuteTrampoline. Subtract it from the measured
// wall-clock latency to get the cell's own (guest) execution latency.
func (rm *RuntimeManager) LastReasoningNanos() uint64 {
	if rm.reasoningNanos < 0 {
		return 0
	}
	return uint64(rm.reasoningNanos)
}

// SetReasoningGate raises or lowers the cognitive-engine gate. While raised,
// guest invoke-reasoning calls resolve to (0,0) without contacting the model.
func (rm *RuntimeManager) SetReasoningGate(on bool) {
	rm.mu.Lock()
	rm.reasoningGate = on
	rm.mu.Unlock()
}

// execTrampoline is the unlocked implementation; callers must hold rm.mu.
func (rm *RuntimeManager) execTrampoline(sourceURN, targetURN, targetFunc string, ptr, length uint32) (res uint32, fuel uint64, tokens uint64, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("guest runtime panic caught: %v", r)
		}
	}()

	compiled, ok := rm.cells[targetURN]
	if !ok {
		return 0, 0, 0, fmt.Errorf("target cell %s not loaded", targetURN)
	}

	// Fresh scratch arena and fuel meter for this invocation; snapshot tokens
	// so we can attribute the cognitive cost consumed during the tick.
	rm.resetScratch()
	rm.fuel.Reset()
	ctx := rm.ictx
	var tok0 uint64
	if rm.inference != nil {
		tok0 = rm.inference.TotalTokens()
	}

	mod, err := rm.runtime.InstantiateModule(ctx, compiled,
		wazero.NewModuleConfig().WithName(targetURN))
	if err != nil {
		return 0, 0, 0, fmt.Errorf("instantiation failed: %w", err)
	}
	defer mod.Close(ctx)

	f := mod.ExportedFunction(targetFunc)
	if f == nil {
		return 0, 0, 0, fmt.Errorf("function %s not found in %s", targetFunc, targetURN)
	}

	// SOFTWARE PAGING: map this cell's private page into its window (page-in). The guest
	// sees a simple flat block at windowBase; the physical page is host-managed and never
	// addressed directly. Isolation is structural — only this cell's page is mapped in — so
	// it holds even with mask enforcement off.
	rm.pageIn(targetURN)

	// MASK ENFORCEMENT: hide the fields this cell may not read (zero them for the tick)
	// and snapshot the fields it may not write; restore the latter afterward regardless
	// of outcome. A cell — and anything it dispatches — thus cannot observe state outside
	// its declared reads nor persist a write outside its declared writes. The physical page
	// zone is folded in here as host-only: hidden (poison) AND preserved (revert) for EVERY
	// cell, so a guest that tries to address a physical page directly sees nothing and can
	// change nothing — the window is the only guest-visible surface.
	var maskSaved [][]byte
	var maskRevert [][2]uint32
	if rm.masksEnabled {
		var poison, revert [][2]uint32
		if m, ok := rm.cellMasks[targetURN]; ok {
			poison, revert = m.poison, m.revert
		}
		if pz := rm.allocatedPages(); len(pz) > 0 {
			poison = append(append([][2]uint32(nil), poison...), pz...)
			revert = append(append([][2]uint32(nil), revert...), pz...)
		}
		if len(revert) > 0 || len(poison) > 0 {
			maskRevert = revert
			maskSaved = make([][]byte, len(revert))
			for i, r := range revert {
				if b, ok := rm.read(r[0], r[1]); ok {
					c := make([]byte, len(b))
					copy(c, b)
					maskSaved[i] = c
				}
			}
			for _, p := range poison {
				if b, ok := rm.read(p[0], p[1]); ok { // live view — zero in place
					for j := range b {
						b[j] = 0
					}
				}
			}
		}
	}

	results, cerr := f.Call(ctx, uint64(ptr), uint64(length))

	for i, r := range maskRevert { // restore non-writable fields (undo any stray write)
		if maskSaved[i] != nil {
			rm.sharedMem.Write(r[0], maskSaved[i])
		}
	}
	if cerr != nil {
		return 0, 0, 0, fmt.Errorf("guest runtime error: %w", cerr)
	}
	// PAGE-OUT: persist the cell's window back to its physical page. Runs after the mask
	// revert (which restored the physical zone to its pre-tick bytes) so the mapped page ends
	// the tick holding exactly the guest's new window contents.
	rm.pageOut(targetURN)

	fuel = rm.fuel.Fuel()
	if rm.inference != nil {
		tokens = rm.inference.TotalTokens() - tok0
	}
	if len(results) == 0 {
		return 0, fuel, tokens, nil
	}
	return uint32(results[0]), fuel, tokens, nil
}

// RoutePacket is the Edge Ingress entry point: it copies raw inbound bytes into
// the packet-frame region of shared memory and register-jumps into the router
// cell's route-packet export with a (ptr, len) reference — zero-copy across the
// host/guest boundary. Returns the router's status.
func (rm *RuntimeManager) RoutePacket(routerURN string, data []byte) (uint32, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.sharedMem == nil {
		return 0, fmt.Errorf("shared memory unavailable")
	}
	if InboundBase+len(data) > InboundEnd {
		return 0, fmt.Errorf("packet of %d bytes exceeds inbound frame space", len(data))
	}
	if len(data) > 0 && !rm.sharedMem.Write(uint32(InboundBase), data) {
		return 0, fmt.Errorf("failed to map packet into inbound frame")
	}
	res, _, _, err := rm.execTrampoline("urn:hdm:gateway", routerURN, "route-packet", uint32(InboundBase), uint32(len(data)))
	return res, err
}

// InputEvent is a normalized peripheral event handed to the Input Gateway. X/Y
// are already in canvas space (0..319, 0..239).
type InputEvent struct {
	Type      uint32 // EvMove/EvMouseDn/EvMouseUp/EvClick/EvKeyDown/EvKeyUp
	X, Y      int32
	Buttons   uint32
	Modifiers uint32
	Key       uint32
}

// writeU32 stores a little-endian u32 into the shared cluster memory.
func (rm *RuntimeManager) writeU32(off uint32, v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	rm.sharedMem.Write(off, b[:])
}

// readU32 loads a little-endian u32 from the shared cluster memory.
func (rm *RuntimeManager) readU32(off uint32) uint32 {
	b, ok := rm.sharedMem.Read(off, 4)
	if !ok {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

// PeekU32 reads a little-endian u32 from live shared memory at off, serialized
// against trampoline execution so it never observes a torn word. Read-only —
// exposed for the inspect API (e.g. reading a contract field's current value).
func (rm *RuntimeManager) PeekU32(off uint32) (uint32, bool) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.sharedMem == nil {
		return 0, false
	}
	return rm.readU32(off), true
}

// PeekBytes returns a COPY of length shared-memory bytes at off, or ok=false. Used to
// hash a cell's declared read-set for frame-level memoization.
func (rm *RuntimeManager) PeekBytes(off, length uint32) ([]byte, bool) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.sharedMem == nil {
		return nil, false
	}
	b, ok := rm.read(off, length)
	if !ok {
		return nil, false
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out, true
}

// nondeterministicImports are host modules whose results are NOT a function of shared
// memory (a clock, randomness, the model, vision) — a cell importing any of them can
// produce different output for the same memory, so it must never be memo-skipped.
var nondeterministicImports = map[string]bool{
	"hdm:kernel/chronos":          true,
	"hdm:kernel/cognitive-engine": true,
	"hdm:kernel/vision-engine":    true,
}

// CellImportsDispatch reports whether a cell can invoke other cells (imports
// cell-dispatch). Such a cell's true read-set is the UNION of everything it and its
// callees read — impractical to declare — so read-poisoning is skipped for it (its
// WRITE mask is still enforced). Cached by hash.
func (rm *RuntimeManager) CellImportsDispatch(phenotypeHash string, bytecode []byte) bool {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.dispByHash == nil {
		rm.dispByHash = map[string]bool{}
	}
	if v, ok := rm.dispByHash[phenotypeHash]; ok {
		return v
	}
	dispatches := false
	if cm, err := rm.runtime.CompileModule(rm.ictx, bytecode); err == nil {
		for _, f := range cm.ImportedFunctions() {
			if mod, _, ok := f.Import(); ok && mod == "hdm:kernel/cell-dispatch" {
				dispatches = true
				break
			}
		}
		_ = cm.Close(rm.ictx)
	}
	rm.dispByHash[phenotypeHash] = dispatches
	return dispatches
}

// CellIsDeterministic reports whether a cell's output is purely a function of the
// shared memory it reads — i.e. it imports no clock/random/model/vision host services.
// Such a cell can be skipped when its declared inputs are unchanged. Cached by hash.
func (rm *RuntimeManager) CellIsDeterministic(phenotypeHash string, bytecode []byte) bool {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.detByHash == nil {
		rm.detByHash = map[string]bool{}
	}
	if v, ok := rm.detByHash[phenotypeHash]; ok {
		return v
	}
	det := false
	if cm, err := rm.runtime.CompileModule(rm.ictx, bytecode); err == nil {
		det = true
		for _, f := range cm.ImportedFunctions() {
			if mod, _, ok := f.Import(); ok && nondeterministicImports[mod] {
				det = false
				break
			}
		}
		_ = cm.Close(rm.ictx)
	}
	rm.detByHash[phenotypeHash] = det
	return det
}

// PokeU32 writes a little-endian u32 into live shared memory at off, under the
// runtime lock. Used to boot an application's shared state into a valid initial
// condition (from its own accepted scenario seeds) before the frame loop runs it.
func (rm *RuntimeManager) PokeU32(off, val uint32) bool {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.sharedMem == nil {
		return false
	}
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], val)
	return rm.sharedMem.Write(off, b[:])
}

// WriteInputEvent is the Peripheral Input Gateway: it normalizes a raw
// windowing-layer event into the HMI Input Event Register. Mouse moves refresh
// only the continuous-state zone; every other event also latches the
// discrete-event slot under a fresh monotonic sequence so guests never miss a
// click behind a burst of moves. Serialized against trampoline execution so a
// cell never observes a torn register.
func (rm *RuntimeManager) WriteInputEvent(ev InputEvent) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.sharedMem == nil {
		return fmt.Errorf("shared memory unavailable")
	}
	rm.inputActive = true
	// Continuous state — refreshed by every event, moves included.
	rm.writeU32(InMouseX, uint32(ev.X))
	rm.writeU32(InMouseY, uint32(ev.Y))
	rm.writeU32(InButtons, ev.Buttons)
	rm.writeU32(InModifiers, ev.Modifiers)
	// Discrete-event latch — bumped only by non-move events.
	if ev.Type != EvMove {
		rm.inputSeq++
		rm.writeU32(InEventSeq, rm.inputSeq)
		rm.writeU32(InEventType, ev.Type)
		rm.writeU32(InEventX, uint32(ev.X))
		rm.writeU32(InEventY, uint32(ev.Y))
		rm.writeU32(InEventKey, ev.Key)
	}
	return nil
}

// putRecord encodes one 24-byte little-endian draw record into dst. The op word
// carries the compositing layer in its high byte: op = (layer<<8)|primitive.
func putRecord(dst []byte, op, a, b, c, d int32, rgba uint32) {
	binary.LittleEndian.PutUint32(dst[0:], uint32(op))
	binary.LittleEndian.PutUint32(dst[4:], uint32(a))
	binary.LittleEndian.PutUint32(dst[8:], uint32(b))
	binary.LittleEndian.PutUint32(dst[12:], uint32(c))
	binary.LittleEndian.PutUint32(dst[16:], uint32(d))
	binary.LittleEndian.PutUint32(dst[20:], rgba)
}

// RenderFrame is the Canvas UI exit point: it ticks a UI cell's render-frame
// export (which emits a vector draw stream into the Canvas region), composites
// the host-owned overlay/cursor plane (layer 2) on top, and reads the resulting
// stream back out-of-band. The cell returns the byte length it wrote.
func (rm *RuntimeManager) RenderFrame(cellURN string) ([]byte, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.sharedMem == nil {
		return nil, fmt.Errorf("shared memory unavailable")
	}
	// Reload the cell when its committed phenotype has changed, so the canvas tracks
	// evolution instead of rendering a stale cached module. Render cells reach the
	// runtime ONLY here (the frame loop skips them — they draw on canvas poll), so
	// without this a converged cell keeps drawing its no-op SEED forever even though
	// its genome and phenotype have advanced. Mirrors TickAppCell's hash check for
	// run-tick cells; the repo.Load cost falls on just the one focused canvas cell.
	if desc, derr := rm.repo.Load(cellURN); derr == nil {
		if rm.loadedHash == nil {
			rm.loadedHash = map[string]string{}
		}
		if rm.loadedHash[cellURN] != desc.PhenotypeHash {
			if bc, perr := rm.repo.Phenotype(desc); perr == nil {
				if compiled, cerr := rm.runtime.CompileModule(rm.ictx, bc); cerr == nil {
					rm.cells[cellURN] = compiled
					rm.loadedHash[cellURN] = desc.PhenotypeHash
				}
			}
		}
	}
	// Resolve grown cells (present in the manifest but not pre-loaded).
	if _, ok := rm.cells[cellURN]; !ok {
		if _, err := rm.resolveCell(rm.ictx, cellURN); err != nil {
			return nil, fmt.Errorf("cell %s not available: %w", cellURN, err)
		}
	}
	n, _, _, err := rm.execTrampoline("urn:hdm:canvas", cellURN, "render-frame", uint32(CanvasBase), uint32(CanvasEnd-CanvasBase))
	if err != nil {
		return nil, err
	}
	if CanvasBase+int(n) > CanvasEnd {
		return nil, fmt.Errorf("canvas frame length %d exceeds buffer", n)
	}
	// Stratified compositor: the host owns layer 2. Append a crosshair
	// cursor at the live pointer position so the input pipeline is visible
	// regardless of what the cell drew. Layer bits keep it above the cell's
	// basemap/widget planes on the browser side.
	if rm.inputActive && CanvasBase+int(n)+48 <= CanvasEnd {
		const op = (2 << 8) | 2 // layer 2, line primitive
		const cur = 0xFFD54FFF  // amber
		mx, my := int32(rm.readU32(InMouseX)), int32(rm.readU32(InMouseY))
		var rec [48]byte
		putRecord(rec[0:24], op, mx-6, my, mx+6, my, cur)
		putRecord(rec[24:48], op, mx, my-6, mx, my+6, cur)
		if rm.sharedMem.Write(uint32(CanvasBase)+n, rec[:]) {
			n += 48
		}
	}
	if n == 0 {
		return nil, nil
	}
	buf, ok := rm.sharedMem.Read(uint32(CanvasBase), n)
	if !ok {
		return nil, fmt.Errorf("failed to read canvas frame")
	}
	frame := append([]byte(nil), buf...)
	// Retain in the frame ring (most recent last) so the vision path can look at
	// the last N frames. Under rm.mu.
	rm.frames = append(rm.frames, frame)
	if len(rm.frames) > frameRingSize {
		rm.frames = rm.frames[len(rm.frames)-frameRingSize:]
	}
	return frame, nil
}

// frameRingSize is how many recent rendered frames the vision path retains — a
// short sequence, enough to judge motion (glide vs teleport) as well as a still.
const frameRingSize = 8

// rasterizeFramesLocked rasterizes the last up-to-n frames in the ring to PNGs.
// Caller must hold rm.mu (it reads rm.frames).
func (rm *RuntimeManager) rasterizeFramesLocked(n int) [][]byte {
	if n <= 0 || n > len(rm.frames) {
		n = len(rm.frames)
	}
	imgs := make([][]byte, 0, n)
	for _, s := range rm.frames[len(rm.frames)-n:] {
		if png, err := RasterizeFrame(s, 160, 120); err == nil && png != nil {
			imgs = append(imgs, png)
		}
	}
	return imgs
}

// VisionJudge asks the multimodal model a question about the last up-to-n rendered
// frames (rasterized to PNGs) — the vision path. It snapshots + rasterizes
// under the lock, then makes the (slow) model call WITHOUT holding it, so rendering
// and ticks continue. Caller must NOT hold rm.mu.
// SetVisionClient routes the multimodal vision path to a distinct client (e.g. the
// router's vision-typed model). Set once at boot, before ticks run.
func (rm *RuntimeManager) SetVisionClient(c *inference.LocalModelClient) { rm.visionClient = c }

// visionInfer returns the client that serves the vision path: the dedicated vision
// client when set, otherwise the base inference client.
func (rm *RuntimeManager) visionInfer() *inference.LocalModelClient {
	if rm.visionClient != nil {
		return rm.visionClient
	}
	return rm.inference
}

func (rm *RuntimeManager) VisionJudge(ctx context.Context, sysPrompt, question string, n int) (string, error) {
	rm.mu.Lock()
	imgs := rm.rasterizeFramesLocked(n)
	infer := rm.visionInfer()
	rm.mu.Unlock()
	if len(imgs) == 0 {
		return "", fmt.Errorf("no frames to judge yet")
	}
	if infer == nil {
		return "", fmt.Errorf("no inference client")
	}
	return infer.InvokeVision(ctx, sysPrompt, question, imgs)
}

// invokeVisionUnlocked runs the (slow) multimodal call without holding rm.mu, the
// same discipline as invokeReasoningUnlocked — the calling guest is parked in Go, so
// releasing the lock lets rendering/ticks proceed. Re-acquires before returning.
func (rm *RuntimeManager) invokeVisionUnlocked(ctx context.Context, sys, question string, imgs [][]byte) (string, error) {
	rm.mu.Unlock()
	defer rm.mu.Lock()
	return rm.visionInfer().InvokeVision(ctx, sys, question, imgs)
}

// hostInvokeVision is the invoke-vision kernel host function, symmetric with
// invoke-reasoning: a guest passes a system prompt and a question about the recent
// rendered frames; the host rasterizes the last few frames and asks the multimodal
// model, returning its text answer via scratch (ptr,len). This is how a WAT cell
// "sees". Args: (sysPtr,sysLen,qPtr,qLen) -> (respPtr,respLen).
func (rm *RuntimeManager) hostInvokeVision(ctx context.Context, _ api.Module, stack []uint64) {
	sys, ok1 := rm.read(uint32(stack[0]), uint32(stack[1]))
	usr, ok2 := rm.read(uint32(stack[2]), uint32(stack[3]))
	if !ok1 || !ok2 || rm.inference == nil {
		stack[0], stack[1] = 0, 0
		return
	}
	imgs := rm.rasterizeFramesLocked(4) // a short recent window
	if len(imgs) == 0 {
		stack[0], stack[1] = 0, 0
		return
	}
	t0 := time.Now()
	resp, err := rm.invokeVisionUnlocked(ctx, string(sys), string(usr), imgs)
	rm.reasoningNanos += time.Since(t0).Nanoseconds()
	if err != nil {
		stack[0], stack[1] = 0, 0
		return
	}
	ptr, length, ok := rm.putScratch([]byte(resp))
	if !ok {
		stack[0], stack[1] = 0, 0
		return
	}
	stack[0], stack[1] = uint64(ptr), uint64(length)
}

// Close shuts down the hypervisor runtime.
func (rm *RuntimeManager) Close(ctx context.Context) error {
	return rm.runtime.Close(ctx)
}
