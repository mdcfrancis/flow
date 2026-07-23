package main

import (
	"context"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mdcfrancis/flow/appgen"
	"github.com/mdcfrancis/flow/axiom"
	"github.com/mdcfrancis/flow/codependency"
	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/execution"
	"github.com/mdcfrancis/flow/gc"
	"github.com/mdcfrancis/flow/inference"
	"github.com/mdcfrancis/flow/integration"
	"github.com/mdcfrancis/flow/manifest"
	"github.com/mdcfrancis/flow/status"
	"github.com/mdcfrancis/flow/storage"
	"github.com/mdcfrancis/flow/telemetry"
	"github.com/mdcfrancis/flow/tui"
)

// wastefulCell is a demo cell with deliberate redundant work, seeded so the
// evolutionary loop has genuine fuel headroom to optimize (and to exercise
// friction discovery, which should target it over the already-minimal
// optimizer).
//
//go:embed cells/wasteful.wat
var wastefulCell string

// branchyCell has input-dependent control flow and a fault path, so the
// Discovery-Invariant compactor keeps a diverse minimal corpus for it.
//
//go:embed cells/branchy.wat
var branchyCell string

// routerCell handles Edge Gateway packets; uiCell emits Canvas UI vector streams.
//
//go:embed cells/router.wat
var routerCell string

//go:embed cells/ui.wat
var uiCell string

// lifeCell is a hand-written Conway's Game of Life UI cell (animates on canvas).
//
//go:embed cells/life.wat
var lifeCell string

// evolutionCadence is how many production heartbeats elapse between out-of-band
// evolutionary mutation frames, keeping the live tick responsive while the
// cognitive engine works.
const evolutionCadence = 4

// janitorCadence is how many heartbeats elapse between Tape Compaction Janitor
// sweeps that prune the stored regression repository.
const janitorCadence = 12

// defaultModel / defaultModelURL are the cognitive engine's model id and
// endpoint when HDM_LLM_MODEL / HDM_LLM_URL are unset. gemma-4-26b-a4b has been
// the strongest local model here for both growth and optimization.
const (
	defaultModel    = "gemma-4-26b-a4b-it-oQ4"
	defaultModelURL = "http://localhost:8000"
	// defaultGeminiModel is used when the Gemini backend is selected and
	// HDM_LLM_MODEL is unset. gemini-3.5-flash reliably emits the exact HDM WAT
	// ABI (correct shared-memory import + run-tick signature) and is fast;
	// gemini-3.1-flash-lite was tried first but botched the ABI boilerplate
	// (hallucinated an env/memory import, wrong result arity).
	defaultGeminiModel = "gemini-3.5-flash"
	geminiKeyFile      = ".gemini_api_key"
)

// buildModelClient constructs the cognitive-engine client, choosing the backend
// from the environment. Gemini is selected when GEMINI_API_KEY is set (or a
// .gemini_api_key file exists), or HDM_LLM_PROVIDER=gemini; otherwise the local
// OpenAI-compatible MLX server is used. HDM_LLM_MODEL / HDM_LLM_URL still
// override the model id and endpoint for either backend.
// baseModelConfig resolves the BASE backend from the environment: Gemini when
// GEMINI_API_KEY / .gemini_api_key is present or HDM_LLM_PROVIDER=gemini, otherwise
// the local OpenAI-compatible server, with model/url overridable.
func baseModelConfig() (provider, model, url, geminiKey string) {
	provider = strings.ToLower(strings.TrimSpace(os.Getenv("HDM_LLM_PROVIDER")))
	geminiKey = strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	if geminiKey == "" {
		if b, err := os.ReadFile(geminiKeyFile); err == nil {
			geminiKey = strings.TrimSpace(string(b))
		}
	}
	if provider == "" && geminiKey != "" {
		provider = "gemini"
	}
	model = os.Getenv("HDM_LLM_MODEL")
	url = os.Getenv("HDM_LLM_URL")
	if provider == "gemini" {
		if model == "" {
			model = defaultGeminiModel
		}
		return "gemini", model, url, geminiKey // url empty → default Gemini endpoint
	}
	if model == "" {
		model = defaultModel
	}
	if url == "" {
		url = defaultModelURL
	}
	return "local", model, url, geminiKey
}

// buildClient constructs one client for a (provider, model, url).
func buildClient(provider, model, url, geminiKey string) *inference.LocalModelClient {
	if provider == "gemini" {
		return inference.NewGeminiClient(url, model, geminiKey)
	}
	return inference.NewLocalModelClient(url, model)
}

// buildModelClient constructs the BASE cognitive-engine client (see baseModelConfig).
func buildModelClient() *inference.LocalModelClient {
	provider, model, url, geminiKey := baseModelConfig()
	if provider == "gemini" {
		if geminiKey == "" {
			log.Printf("[COGNITION] WARNING: Gemini selected but no GEMINI_API_KEY / %s found; calls will 4xx", geminiKeyFile)
		}
		log.Printf("[COGNITION] provider=gemini model=%s (override with HDM_LLM_MODEL)", model)
	} else {
		log.Printf("[COGNITION] provider=local model=%s endpoint=%s (override with HDM_LLM_MODEL / HDM_LLM_URL)", model, url)
	}
	return buildClient(provider, model, url, geminiKey)
}

// buildModelRouter wraps the base client in a ModelRouter: any logical model type
// with a per-type override (HDM_LLM_MODEL_<TYPE> / _URL_ / _PROVIDER_) binds its own
// client; every other type resolves to the base. With no overrides the router behaves
// exactly like the single base model, so this is a no-op until a type is configured.
func buildModelRouter(base *inference.LocalModelClient, policyBindings map[string]string) *inference.ModelRouter {
	bp, bm, bu, bk := baseModelConfig()
	perType := map[inference.ModelType]*inference.LocalModelClient{}
	for _, t := range inference.AllModelTypes {
		em, eu, ep := inference.TypedModelEnv(t)
		// A ledger-policy binding overrides env's model id (provider/url still from
		// env/base) — so bindings persist and are editable without env.
		if pm := policyBindings[string(t)]; pm != "" {
			em = pm
		}
		if em == "" && eu == "" && ep == "" {
			continue // no override → base
		}
		p, m, u := bp, bm, bu
		if ep != "" {
			p = strings.ToLower(strings.TrimSpace(ep))
		}
		if em != "" {
			m = em
		}
		if eu != "" {
			u = eu
		}
		perType[t] = buildClient(p, m, u, bk)
		log.Printf("[COGNITION] type=%s -> provider=%s model=%s endpoint=%s", t, p, m, u)
	}
	if len(perType) == 0 {
		log.Printf("[COGNITION] model types: all -> base (%s); override per type with HDM_LLM_MODEL_<REASON|CODE|VISION|FAST> or the ledger policy", bm)
	}
	return inference.NewModelRouter(base, perType)
}

// applyModelPolicy applies the ledger policy's evolvable model settings to the live
// router: the LLM-tunable cost tiers (immediately) and the operator-set bindings
// (hot-rebinding a type whose model changed and unloading the model it left behind,
// when nothing else uses it). Called at boot and each fixpoint; nil-safe.
func applyModelPolicy(ledger *storage.LedgerEngine, router *inference.ModelRouter, hyp *execution.RuntimeManager) {
	if router == nil {
		return
	}
	p := evolution.LoadPolicy(ledger)
	if len(p.ModelCostWeights) > 0 {
		w := make(map[inference.ModelType]float64, len(p.ModelCostWeights))
		for k, v := range p.ModelCostWeights {
			w[inference.ModelType(k)] = v
		}
		inference.SetCostWeights(w)
	}
	if len(p.ModelBindings) == 0 {
		return
	}
	bp, bm, bu, bk := baseModelConfig()
	for _, t := range inference.AllModelTypes {
		want := p.ModelBindings[string(t)]
		if want == "" || router.ModelOf(t) == want {
			continue
		}
		old := router.Rebind(t, buildClient(bp, want, bu, bk))
		log.Printf("[MODEL] rebound %s -> %s (ledger policy)", t, want)
		if t == inference.ModelVision && hyp != nil {
			hyp.SetVisionClient(router.For(string(inference.ModelVision)))
		}
		// Free the model left behind, but only if nothing else uses that client and
		// it is not the base model (which the default/reason path still needs).
		if old != nil && !router.InUse(old) && old.Model() != bm {
			go func(c *inference.LocalModelClient) {
				uctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				_ = c.Unload(uctx)
			}(old)
		}
	}
}

// tokenSource is the token-accounting boundary — satisfied by both the base client
// and the ModelRouter (which sums across all typed clients), so metrics stay correct
// once calls fan out across model types.
type tokenSource interface{ TotalTokens() uint64 }

// convergenceHolds is how many consecutive no-progress frames mark a cell as
// converged (no friction left to reduce), after which it is dropped from
// selection so the loop stops re-probing it (and burning tokens) for nothing.
const convergenceHolds = 3

// maxIdleBackoff caps how many fixpoint frames the loop waits between
// steady-state re-evaluations once idle — so re-attempts never stop (the model
// is stochastic and may yet propose work), but a genuinely-done system probes
// only occasionally rather than every frame. Kept modest so the loop stays
// visibly responsive (an idle system re-checks within a handful of frames rather
// than going quiet for dozens).
const maxIdleBackoff = 8

// maxStallRetries bounds how many times a STALLED cell (parked while still
// failing acceptance checks) is re-opened for another build attempt at the
// global fixpoint before it is left parked. It keeps the loop trying to build a
// hard subsystem without retrying an unbuildable one forever. Tunable via Policy
// (a var, not a const), so it can be refined toward the correct+efficient objective.
var maxStallRetries = 3

// applyPolicy loads the evolvable Policy and applies its tunable judgement calls to the
// running loop. Called at startup and each fixpoint so a tuned policy takes effect. The
// vision interval keeps its env override (HDM_VISION_INTERVAL) precedence.
func applyPolicy(ledger *storage.LedgerEngine) {
	p := evolution.LoadPolicy(ledger)
	maxStallRetries = p.MaxStallRetries
	codeCritiqueInterval = time.Duration(p.CodeCriticIntervalSec) * time.Second
	if os.Getenv("HDM_VISION_INTERVAL") == "" {
		visionCritiqueInterval = time.Duration(p.VisionIntervalSec) * time.Second
	}
}

// SystemMetrics is a snapshot of how the system is doing against the goal: correctness
// (fraction of app cells green) and efficiency (stall rate, per-frame cost, and cumulative
// model TOKENS — the first-class efficiency measure). It drives metrics-based policy
// self-tuning — the meta-loop closing with no human.
type SystemMetrics struct {
	CellCount     int
	GreenFrac     float64
	StalledFrac   float64
	AvgFrameMs    float64
	SessionTokens uint64 // cumulative model tokens; deltas measure token burn between tunes
	// WeightedCost is cumulative cognitive spend weighted by model TIER — an expensive
	// reasoner's tokens count for more than a fast model's (see inference.ModelType
	// cost weights). Its delta is the burn signal the policy tuner reacts to, so model
	// choice is priced into the tuning. Falls back to raw tokens when unweighted.
	WeightedCost float64
}

// weightedTokenSource optionally exposes tier-weighted cognitive cost (the router does;
// a bare client does not).
type weightedTokenSource interface{ WeightedTokens() float64 }

func systemMetrics(ctx context.Context, registry *evolution.CellRegistry, orch *evolution.Orchestrator, friction map[string]evolution.FrictionState, budget *frameBudget, root string, model tokenSource) SystemMetrics {
	var m SystemMetrics
	if model != nil {
		m.SessionTokens = model.TotalTokens()
		if wt, ok := model.(weightedTokenSource); ok {
			m.WeightedCost = wt.WeightedTokens()
		} else {
			m.WeightedCost = float64(m.SessionTokens)
		}
	}
	green, stalled := 0, 0
	for _, u := range registry.List() {
		if appNamespace(u) == "" {
			continue // application cells only
		}
		m.CellCount++
		if p, t, err := orch.ScoreCell(ctx, u); err == nil && t > 0 && p >= t {
			green++
		}
		if fs := friction[u]; fs.Parked(root) && fs.Retries >= maxStallRetries {
			stalled++
		}
	}
	if m.CellCount > 0 {
		m.GreenFrac = float64(green) / float64(m.CellCount)
		m.StalledFrac = float64(stalled) / float64(m.CellCount)
	}
	if tracked := budget.tracked(); len(tracked) > 0 {
		var sum float64
		for _, ns := range tracked {
			sum += ns
		}
		m.AvgFrameMs = (sum / float64(len(tracked))) / 1e6
	}
	return m
}

// policyTuneState carries the metrics-tuning cooldown + the pre-tune policy for regression rollback.
type policyTuneState struct {
	lastAt         time.Time
	prevPolicy     *evolution.Policy
	baselineGreen  float64
	baselineTokens uint64  // session tokens at the last tune (raw, for reference)
	baselineCost   float64 // tier-weighted cognitive spend at the last tune — the burn-rate baseline
}

var policyTuneInterval = 10 * time.Minute

// autoTunePolicy self-tunes the policy from REAL OUTCOMES toward the objective — no human in
// the loop. Beyond clamping, it rolls back a prior tune that made correctness worse (green
// dropped), and only tunes when there is a signal (stuck cells / low green). Throttled.
func autoTunePolicy(ctx context.Context, grower *appgen.Grower, ledger *storage.LedgerEngine, m SystemMetrics, st *policyTuneState) {
	if time.Since(st.lastAt) < policyTuneInterval {
		return
	}
	st.lastAt = time.Now()
	if st.prevPolicy != nil && m.GreenFrac < st.baselineGreen-0.05 {
		_ = evolution.SavePolicy(ledger, *st.prevPolicy)
		applyPolicy(ledger)
		log.Printf("[POLICY] metrics-tune regressed (green %.0f%% < %.0f%%) — rolled back", m.GreenFrac*100, st.baselineGreen*100)
		st.prevPolicy = nil
		return
	}
	if m.CellCount == 0 || (m.GreenFrac >= 0.9 && m.StalledFrac == 0) {
		st.prevPolicy = nil // healthy or no data — leave policy alone
		return
	}
	before := evolution.LoadPolicy(ledger)
	// Token burn since the last tune — the first-class efficiency signal. If correctness held
	// flat while tokens poured out, the loop is wasting fuel and the tune should pull it back.
	// Tier-weighted burn since the last tune — the first-class efficiency signal, now
	// priced by model type so an expensive reasoner's spend registers heavier. If
	// correctness held flat while cost poured out, the loop is wasting spend.
	var burn float64
	if st.baselineCost > 0 && m.WeightedCost >= st.baselineCost {
		burn = m.WeightedCost - st.baselineCost
	}
	obs := fmt.Sprintf("Observed outcomes: %d application cells, %.0f%% correct (green), %.0f%% stalled beyond retries, avg per-frame cost %.2f ms. Since the last tune ~%.0f (tier-weighted) cognitive cost was spent. TOKEN EFFICIENCY IS FIRST-CLASS: if correctness is not climbing, that spend is waste — tune to burn FEWER tokens (fewer retries on cells that are not progressing, less frequent critics, a more direct path, or a cheaper model type where it suffices) while never lowering correctness. Tune the policy toward the goal.",
		m.CellCount, m.GreenFrac*100, m.StalledFrac*100, m.AvgFrameMs, burn)
	if _, changed, note, err := grower.OptimizePolicy(ctx, obs); err == nil && changed {
		applyPolicy(ledger)
		st.prevPolicy = &before
		st.baselineGreen = m.GreenFrac
		st.baselineTokens = m.SessionTokens
		st.baselineCost = m.WeightedCost
		log.Printf("[POLICY] self-tuned from metrics (green %.0f%%, stalled %.0f%%, +%.0f cost): %s", m.GreenFrac*100, m.StalledFrac*100, burn, note)
	}
}

// evolutionPaused halts the evolution heartbeat while a system benchmark runs, so the
// benchmark's grows/mutation-frames don't race the main orchestrator.
var evolutionPaused atomic.Bool

// benchmarkSuite is the fixed set of small applications the SYSTEM must be able to evolve. It is
// the system's acceptance corpus — a regression + performance bar. Small so a run is minutes.
func benchmarkSuite() []struct{ Name, Objective string } {
	return []struct{ Name, Objective string }{
		{"counter", "A counter. A logic cell increments a shared integer 'count' by 1 each tick, wrapping to 0 at 100; a view cell reads count and draws a rectangle whose width equals count."},
		{"mover", "A moving dot. A logic cell advances a shared integer 'x' by 4 each tick, wrapping to 0 at 300; a view cell reads x and draws a rectangle at that x position."},
	}
}

// runBenchmark evolves each benchmark app from scratch under a frame budget and measures the
// system's CORRECTNESS (did it converge) and EFFICIENCY at evolving (tokens + frames). Apps are
// grown ISOLATED (not enrolled in the main registry) and retired after, so the benchmark never
// pollutes real work. Pauses the evolution heartbeat for the duration.
// benchmarkAppTimeout bounds how long a single benchmark app may take to grow+evolve before it
// is abandoned as non-converged, so a hung model call can never wedge a promotion. Override
// with HDM_BENCH_APP_TIMEOUT (a Go duration, e.g. "6m").
func benchmarkAppTimeout() time.Duration {
	if s := os.Getenv("HDM_BENCH_APP_TIMEOUT"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			return d
		}
	}
	return 4 * time.Minute
}

func runBenchmark(ctx context.Context, grower *appgen.Grower, orch *evolution.Orchestrator, model tokenSource, epoch string) evolution.BenchmarkResult {
	evolutionPaused.Store(true)
	defer evolutionPaused.Store(false)
	res := evolution.BenchmarkResult{Epoch: epoch, At: time.Now().Unix()}
	const maxFrames = 40
	for _, app := range benchmarkSuite() {
		ar := evolution.BenchmarkAppResult{Name: app.Name}
		// Count tokens from BEFORE the grow: the true cost of producing a correct app is
		// decompose + design + synthesize + evolve, not just the evolution frames. Token
		// efficiency is first-class, so the benchmark measures the whole journey.
		tok0 := model.TotalTokens()
		// Per-app deadline: a hung model call must fail THIS app (counted non-converged),
		// never wedge the whole benchmark (as it did on the first v2 attempt). v1's apps each
		// finished in ~1 min; the default gives generous headroom (HDM_BENCH_APP_TIMEOUT).
		appCtx, cancel := context.WithTimeout(ctx, benchmarkAppTimeout())
		env, cells, _, err := grower.GrowConcurrent(appCtx, app.Objective, func(u string) {}) // isolated: no registry enroll
		if err == nil && env != nil {
			ns := env.ApplicationNamespace
			frames := 0
			for frames < maxFrames {
				if appCtx.Err() != nil {
					break // deadline hit mid-evolution — stop this app
				}
				target := ""
				for _, u := range cells {
					if p, t, e := orch.ScoreCell(appCtx, u); e == nil && t > 0 && p < t {
						target = u
						break
					}
				}
				if target == "" {
					break // every cell green (or no checks)
				}
				_, _ = orch.RunFrame(appCtx, target)
				frames++
			}
			green, total := 0, 0
			for _, u := range cells {
				if p, t, e := orch.ScoreCell(appCtx, u); e == nil && t > 0 {
					total++
					if p >= t {
						green++
					}
				}
			}
			ar.Green, ar.Total, ar.Frames = green, total, frames
			ar.Converged = total > 0 && green == total
			_, _ = grower.RetireApp(ns) // clean up the benchmark app
		}
		ar.Tokens = model.TotalTokens() - tok0 // record burn even for a timed-out/failed grow
		timedOut := appCtx.Err() == context.DeadlineExceeded
		cancel()
		if timedOut {
			log.Printf("[BENCH] %s: TIMED OUT after %s — counted non-converged", app.Name, benchmarkAppTimeout())
		}
		res.Apps = append(res.Apps, ar)
		log.Printf("[BENCH] %s: converged=%v green=%d/%d tokens=%d frames=%d", ar.Name, ar.Converged, ar.Green, ar.Total, ar.Tokens, ar.Frames)
	}
	res.Summarize()
	return res
}

// tryPromoteEpoch is the system's own acceptance gate for cutting a new version: run the
// benchmark, and cut a new epoch ONLY if the system did not regress in correctness and improved
// (more correct, or equally correct but more efficient at evolving) vs the last epoch's
// benchmark. On promotion it checkpoints the epoch AND records this benchmark as the new bar.
func tryPromoteEpoch(ctx context.Context, grower *appgen.Grower, orch *evolution.Orchestrator, model tokenSource, ledger *storage.LedgerEngine, name string) map[string]any {
	res := runBenchmark(ctx, grower, orch, model, name)
	baseline := evolution.LoadBenchmarkBaseline(ledger)
	promote, reason := evolution.ShouldPromote(res, baseline)
	out := map[string]any{"benchmark": res, "promoted": promote, "reason": reason}
	if baseline != nil {
		out["baseline"] = baseline
	}
	if promote {
		if _, err := grower.SaveEpoch(name); err != nil {
			out["error"] = "benchmark passed but epoch save failed: " + err.Error()
		} else {
			_ = evolution.SaveBenchmarkBaseline(ledger, res)
			log.Printf("[PROMOTE] cut epoch %q: %s", name, reason)
		}
	} else {
		log.Printf("[PROMOTE] refused epoch %q: %s", name, reason)
	}
	return out
}

// retryStalled re-opens stalled cells (parked but incomplete at the current
// root). A cell with retry budget left simply gets another build attempt. A
// cell that has EXHAUSTED its retries — the model repeatedly could not pass the
// checks — is not treated as a failure: its suite is re-evaluated against the
// goal by the adversarial auditor (blind to whether the cell passes), and if
// unfaithful checks are dropped, the cell gets a fresh start against the
// corrected spec. Returns how many cells were re-opened.
func retryStalled(ctx context.Context, orch *evolution.Orchestrator, all []string, friction map[string]evolution.FrictionState, root string, persist func(string, evolution.FrictionState), activity *status.Broker) int {
	n := 0
	for _, u := range all {
		fs := friction[u]
		if !fs.Parked(root) {
			continue
		}
		// A cell is stuck if it is flagged stalled OR — grounding in reality rather
		// than the persisted Incomplete flag — it is parked but its LIVE score is
		// still incomplete. The flag is stamped at convergence time and can be
		// wrong (e.g. a cell parked while the model was unreachable was scored 0/0
		// and mislabelled "complete"); the live-score check re-opens such wedged
		// cells too.
		stuck := fs.Stalled(root)
		if !stuck {
			if passed, total, err := orch.ScoreCell(ctx, u); err == nil && total > 0 && passed < total {
				stuck = true
			}
		}
		if !stuck {
			continue
		}
		if fs.Retries < maxStallRetries {
			persist(u, evolution.FrictionState{Retries: fs.Retries + 1, Root: root})
			activity.Event("mutate", u, fmt.Sprintf("retrying stalled cell (attempt %d/%d)", fs.Retries+1, maxStallRetries))
			log.Printf("[EVOLVE] %s stalled — retrying build (attempt %d/%d)", u, fs.Retries+1, maxStallRetries)
			n++
			continue
		}
		// Budget exhausted: maybe the TESTS are wrong, not the cell. Re-judge the
		// suite against the goal; if unfaithful checks are dropped, restart it.
		if kept, dropped, err := orch.RecertifySuite(ctx, u); err == nil && dropped > 0 {
			persist(u, evolution.FrictionState{Root: root}) // reset retries, un-park
			activity.Event("reject", u, fmt.Sprintf("re-evaluated tests vs goal: dropped %d unfaithful, %d remain", dropped, kept))
			log.Printf("[EVOLVE] %s stalled — re-evaluated tests against the goal: dropped %d unfaithful check(s), %d remain; retrying", u, dropped, kept)
			n++
		}
	}
	return n
}

// evolveBoundaries is the stall-recovery pass that challenges a stuck cell's BOUNDARY
// before it is fractured: a cell that has spent its retries may be failing not because its
// code or its tests are wrong, but because its declared read/write ports are wrong — too
// narrow (the mask hides a field it needs) or misaligned with its purpose. It re-evolves
// the cell's boundary from its purpose + the declared-vs-verified gap; if the ports change,
// it re-derives the app map (propagating the new mask + interface) and re-opens the cell to
// rebuild against the corrected boundary. Runs after retryStalled and BEFORE fractureStalled,
// so the recovery ladder is: fix code → fix tests → fix boundary → decompose. Returns how
// many cells were re-opened with an evolved boundary.
func evolveBoundaries(ctx context.Context, grower *appgen.Grower, orch *evolution.Orchestrator, registry *evolution.CellRegistry, friction map[string]evolution.FrictionState, root string, persist func(string, evolution.FrictionState), activity *status.Broker, evaluated map[string]string) int {
	n := 0
	for _, u := range registry.List() {
		fs := friction[u]
		if !(fs.Parked(root) && fs.Retries >= maxStallRetries) {
			continue
		}
		ns := appNamespace(u)
		if ns == "" {
			continue
		}
		if passed, tot, err := orch.ScoreCell(ctx, u); err != nil || tot == 0 || passed >= tot {
			continue // no checks, or actually complete — not stuck
		}
		// Boundary-stable guard (bounds token cost): skip the model call when the cell's
		// declared boundary already matches its verified behavior (nothing to evolve — its
		// problem is the code, so let it fracture), and memoize on the decision fingerprint
		// so we don't re-ask the model about inputs it has already seen unchanged.
		stable, fp := grower.BoundaryCheck(ns, u)
		if stable {
			continue
		}
		if fp != "" && evaluated[u] == fp {
			continue // same ports/verified-access/objective as last evaluated — nothing new
		}
		log.Printf("[BOUNDARY] %s stalled beyond retry — challenging its boundary before fracture", u)
		changed, err := grower.EvolveBoundary(ctx, ns, u, "stalled")
		if fp != "" {
			evaluated[u] = fp // record what we just evaluated, changed or not
		}
		if err != nil || !changed {
			continue
		}
		_ = grower.RefreshAppMap(ctx, ns) // propagate the new ports to the map/mask/interface
		reset := evolution.FrictionState{Root: root}
		persist(u, reset)
		friction[u] = reset // un-park in-memory too, so fractureStalled skips it this pass
		activity.Event("mutate", u, "boundary evolved — rebuilding against corrected ports")
		log.Printf("[EVOLVE] %s stalled — evolved its boundary; retrying against corrected ports", u)
		n++
	}
	return n
}

// fractureStalled is the stall-recovery decomposition pass: a cell that has
// exhausted its build retries AND survived test re-evaluation (its checks are
// faithful, it simply cannot be synthesized whole) is FRACTURED into smaller
// sub-cells, each owning a slice of the responsibility and coordinating through
// the app's shared-state contract. This mirrors how an engineer breaks an
// intractable task into manageable pieces; the saliency/efficiency drive (and
// component extraction) may later re-fuse the slices. The parent is retired from
// the annealing set. Runs after retryStalled in the fixpoint, so it only fires
// once retries are truly spent and re-certification dropped nothing.
func fractureStalled(ctx context.Context, grower *appgen.Grower, orch *evolution.Orchestrator, registry *evolution.CellRegistry, friction map[string]evolution.FrictionState, root string, activity *status.Broker, ledger *storage.LedgerEngine) int {
	total := 0
	for _, u := range registry.List() {
		fs := friction[u]
		// Fracture a cell that is out of active building (parked at the current
		// root) and has spent its retries. Completeness is judged by the LIVE
		// score, not fs.Incomplete: that flag is stamped at convergence time and
		// goes stale when the suite is later mutated (expanded / re-certified), so
		// a genuinely-failing cell can end up flagged "complete" and freeze. The
		// live score is ground truth.
		if !(fs.Parked(root) && fs.Retries >= maxStallRetries) {
			continue
		}
		if appNamespace(u) == "" {
			continue // only application cells (with an envelope) can fracture
		}
		if passed, tot, err := orch.ScoreCell(ctx, u); err != nil || tot == 0 || passed >= tot {
			continue // no checks, or actually complete — not stuck
		}
		var kids []evolution.GoalSpec
		n, err := grower.FractureCell(ctx, u,
			func(child string) {
				registry.Add(child)
				activity.SetCell(child, status.CellNew)
				kids = append(kids, evolution.GoalSpec{Cell: child})
			},
			func(parent string) {
				registry.Remove(parent)
				delete(friction, parent)
				activity.SetCell(parent, status.CellRejected)
			})
		if err != nil {
			log.Printf("[GROW] fracture %s failed: %v", u, err)
			continue
		}
		if n > 0 {
			// Record the decomposition in the goal tree so the walk descends into the
			// children before the next sibling, and the stall reason is carried on the
			// parent for reason-carried backtracking (Phase 2).
			if ns := appNamespace(u); ns != "" {
				tr := syncGoalTree(ledger, ns)
				if tr != nil {
					tr.RecordFracture(u, kids, "stalled beyond retry — decomposed")
					if err := evolution.SaveGoalTree(ledger, ns, tr); err != nil {
						log.Printf("[WALK] persist goal tree %s: %v", ns, err)
					}
				}
			}
			log.Printf("[EVOLVE] %s stalled beyond retry — fractured into %d sub-cells (decomposed the task); parent retired", u, n)
			total += n
		}
	}
	return total
}

// isCompositionDriver reports whether a cell is a combinator DRIVER subsystem — deterministic
// boilerplate that legitimately has no acceptance checks (the leaf carries the behavior). Such
// cells must be exempt from orphan recovery, which would otherwise try to author or fracture them.
func isCompositionDriver(ledger *storage.LedgerEngine, urn string) bool {
	ns := appNamespace(urn)
	if ns == "" {
		return false
	}
	env := appgen.LoadEnvelope(ledger, ns)
	if env == nil {
		return false
	}
	for _, s := range env.SubsystemRequirements {
		if s.Identity == urn {
			return s.Composition != nil
		}
	}
	return false
}

// maxAcceptanceReauthors bounds re-authoring attempts before an orphaned cell is fractured.
const maxAcceptanceReauthors = 3

// recoverOrphanedCells rescues a live application cell that has NO acceptance suite — a LIMBO
// state the build/stall/fracture ladder ignores because every rung is gated on total>0 (so the
// cell is never built, never retried, never fractured, and never counts green, silently wedging
// its whole app). It first RE-AUTHORS the cell's acceptance (ExpandSuite — the authoring may
// simply not have succeeded yet); if that still yields nothing after maxAcceptanceReauthors
// attempts, it FRACTURES the cell into specifiable sub-goals (the decomposition that let this
// app's renderers converge). Composition drivers and async cells are exempt (they lack
// acceptance by design). Returns the number of cells acted on.
func recoverOrphanedCells(ctx context.Context, grower *appgen.Grower, orch *evolution.Orchestrator, registry *evolution.CellRegistry, repo *manifest.Repository, ledger *storage.LedgerEngine, hyp *execution.RuntimeManager, attempts map[string]int, activity *status.Broker) int {
	n := 0
	for _, u := range registry.List() {
		if appNamespace(u) == "" || isCompositionDriver(ledger, u) {
			continue
		}
		desc, err := repo.Load(u)
		if err != nil {
			continue
		}
		if bc, perr := repo.Phenotype(desc); perr == nil && hyp.IsAsyncCell(desc.PhenotypeHash, bc) {
			continue // async (reasoning) cells lack standard acceptance by nature
		}
		if _, tot, serr := orch.ScoreCell(ctx, u); serr != nil || tot > 0 {
			continue // has acceptance (or errored) — not orphaned
		}
		attempts[u]++
		if attempts[u] <= maxAcceptanceReauthors {
			if added, aerr := grower.ExpandSuite(ctx, u); aerr == nil && added > 0 {
				log.Printf("[RECOVER] %s had no acceptance — re-authored %d scenario(s); now buildable", u, added)
				activity.Event("create", u, fmt.Sprintf("recovered acceptance (%d checks)", added))
				n++
			} else {
				log.Printf("[RECOVER] %s has no acceptance — re-author attempt %d/%d produced none", u, attempts[u], maxAcceptanceReauthors)
			}
			continue
		}
		// Re-authoring exhausted: the whole behavior can't be pinned — decompose it.
		var kids []evolution.GoalSpec
		fn, ferr := grower.FractureCell(ctx, u,
			func(child string) {
				registry.Add(child)
				activity.SetCell(child, status.CellNew)
				kids = append(kids, evolution.GoalSpec{Cell: child})
			},
			func(parent string) {
				registry.Remove(parent)
				activity.SetCell(parent, status.CellRejected)
			})
		if ferr != nil {
			log.Printf("[RECOVER] fracture %s failed: %v", u, ferr)
			continue
		}
		if fn > 0 {
			if ns := appNamespace(u); ns != "" {
				if tr := syncGoalTree(ledger, ns); tr != nil {
					tr.RecordFracture(u, kids, "no acceptance could be authored — decomposed into specifiable sub-goals")
					_ = evolution.SaveGoalTree(ledger, ns, tr)
				}
			}
			delete(attempts, u)
			log.Printf("[RECOVER] %s had no authorable acceptance — fractured into %d sub-cell(s)", u, fn)
			n += fn
		}
	}
	return n
}

// refreshAppMaps re-derives every grown application's conceptual map from ground
// truth (each cell's verified checks + its genotype's memory accesses), so the
// map every synthesis and authoring prompt is built against reflects the system
// as it currently stands rather than as it was first planned.
func refreshAppMaps(ctx context.Context, grower *appgen.Grower, registry *evolution.CellRegistry) {
	seen := map[string]bool{}
	for _, u := range registry.List() {
		ns := appNamespace(u)
		if ns == "" || seen[ns] {
			continue
		}
		seen[ns] = true
		if err := grower.RefreshAppMap(ctx, ns); err != nil {
			log.Printf("[MAP] refresh %s failed: %v", ns, err)
		}
	}
}

// refreshPlans keeps every grown application's DESIGN PLAN current as its
// architecture changes (fracture, stall, new subsystems), so plan-driven
// synthesis always implements a design that reflects the system as it now stands.
func refreshPlans(ctx context.Context, grower *appgen.Grower, registry *evolution.CellRegistry) {
	seen := map[string]bool{}
	for _, u := range registry.List() {
		ns := appNamespace(u)
		if ns == "" || seen[ns] {
			continue
		}
		seen[ns] = true
		if err := grower.RefreshPlan(ctx, ns); err != nil {
			log.Printf("[PLAN] refresh %s failed: %v", ns, err)
		}
	}
}

// humanBytes renders a byte count compactly for the data-flow log.
func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// activeCandidates returns the URNs still eligible for evolution: those not
// parked, OR parked against a stale manifest root (the system has moved since,
// so they are re-evaluated in the new context). A cell is inactive only while
// Parked at the current root — whether it parked complete (done) or incomplete
// (stalled); stalled cells are re-opened separately by retryStalled.
func activeCandidates(all []string, friction map[string]evolution.FrictionState, root string) []string {
	out := make([]string, 0, len(all))
	for _, u := range all {
		if !friction[u].Parked(root) {
			out = append(out, u)
		}
	}
	return out
}

// rehydrateApps re-enrolls previously grown application subsystems from the
// ledger so the evolution loop, /cells, and the canvas resume across restarts.
// Growth persists an envelope per app (with its subsystem list); on boot we
// replay those envelopes and re-enroll every subsystem whose descriptor is
// still live. Returns the first UI subsystem URN found, to refocus the canvas.
func rehydrateApps(ledger *storage.LedgerEngine, repo *manifest.Repository, registry *evolution.CellRegistry, activity *status.Broker) string {
	refs, err := ledger.Refs()
	if err != nil {
		return ""
	}
	var namespaces []string
	for key := range refs {
		if ns := strings.TrimSuffix(key, ":envelope"); ns != key {
			namespaces = append(namespaces, ns)
		}
	}
	sort.Strings(namespaces) // deterministic order across restarts
	var firstUI string
	enrolled := 0
	for _, ns := range namespaces {
		env := appgen.LoadEnvelope(ledger, ns)
		if env == nil {
			continue
		}
		for _, sub := range env.SubsystemRequirements {
			if _, err := repo.Load(sub.Identity); err != nil {
				continue // not a live/valid descriptor — skip
			}
			registry.Add(sub.Identity)
			activity.SetCell(sub.Identity, status.CellLive)
			enrolled++
			if firstUI == "" && sub.IsRender() {
				firstUI = sub.Identity
			}
		}
	}
	if enrolled > 0 {
		log.Printf("[REHYDRATE] re-enrolled %d grown subsystem(s) from %d app envelope(s)", enrolled, len(namespaces))
	}
	return firstUI
}

// ensureContracts authors a shared-state contract for each grown application
// that lacks one, so its subsystems can coordinate through an agreed memory
// layout. Idempotent (EnsureContract no-ops once a contract exists). Returns the
// number of apps for which a contract was newly authored.
func ensureContracts(ctx context.Context, grower *appgen.Grower, registry *evolution.CellRegistry) int {
	seen := map[string]bool{}
	n := 0
	for _, u := range registry.List() {
		ns := appNamespace(u)
		if ns == "" || seen[ns] {
			continue
		}
		seen[ns] = true
		if authored, err := grower.EnsureContract(ctx, ns); err == nil && authored {
			log.Printf("[GROW] authored shared-state contract for %s", ns)
			n++
		}
	}
	return n
}

// consolidateComponents is the steady-state "collapse into common components"
// pass: it asks the redundancy critic whether two application cells overlap
// enough to merge, and if so attempts the fusion (gauntlet-gated, so a merge
// that would change behavior is rejected). Returns the number committed.
func consolidateComponents(ctx context.Context, orch *evolution.Orchestrator, registry *evolution.CellRegistry, activity *status.Broker) int {
	var app []string
	for _, u := range registry.List() {
		if appNamespace(u) != "" {
			app = append(app, u)
		}
	}
	if len(app) < 2 {
		return 0
	}
	a, b, reason, err := orch.ConsolidationCandidate(ctx, app)
	if err != nil || a == "" || b == "" {
		return 0
	}
	// Collapse the redundancy by EXTRACTING the shared logic into a new common
	// component both cells delegate to (behavior-preserving, gauntlet-gated).
	componentURN := appNamespace(a) + ":shared-" + shortHash(a+"|"+b)
	activity.Phase("consolidating", "extracting shared component from "+a+" + "+b+" — "+reason, componentURN)
	log.Printf("[EVOLVE] consolidation: extracting shared component %s from %s + %s (%s)", componentURN, a, b, reason)
	fr, err := orch.RunExtraction(ctx, a, b, componentURN)
	if err != nil {
		log.Printf("[EVOLVE] consolidation extraction errored: %v", err)
		return 0
	}
	if fr == nil || !fr.Committed {
		if fr != nil {
			log.Printf("[EVOLVE] consolidation held: %s", fr.Reason)
		}
		return 0 // gauntlet held it — not a behavior-preserving extraction
	}
	registry.Add(componentURN)
	activity.SetCell(componentURN, status.CellNew)
	activity.SetCell(a, status.CellFused)
	activity.SetCell(b, status.CellFused)
	log.Printf("[EVOLVE] consolidation: %s", fr.Reason)
	return 1
}

// appNamespace extracts an application namespace (urn:hdm:apps:<name>) from a
// subsystem cell URN, or "" if the URN is not an app subsystem.
func appNamespace(urn string) string {
	const prefix = "urn:hdm:apps:"
	if !strings.HasPrefix(urn, prefix) {
		return ""
	}
	rest := urn[len(prefix):]
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		rest = rest[:i]
	}
	if rest == "" {
		return ""
	}
	return prefix + rest
}

// challengeArchitectures asks, for each fully-complete grown application, whether
// its architecture is complete for the objective — proposing (and scaffolding) a
// missing subsystem if not. Only complete apps are challenged, so a stuck/partial
// app is never piled on. Returns the total subsystems added.
func challengeArchitectures(ctx context.Context, grower *appgen.Grower, orch *evolution.Orchestrator, registry *evolution.CellRegistry, activity *status.Broker, ledger *storage.LedgerEngine) int {
	apps := map[string][]string{}
	var order []string
	for _, u := range registry.List() {
		ns := appNamespace(u)
		if ns == "" {
			continue
		}
		if _, ok := apps[ns]; !ok {
			order = append(order, ns)
		}
		apps[ns] = append(apps[ns], u)
	}
	total := 0
	for _, ns := range order {
		complete := true
		for _, u := range apps[ns] {
			if p, t, err := orch.ScoreCell(ctx, u); err != nil || (t > 0 && p < t) {
				complete = false
				break
			}
		}
		if !complete {
			continue // don't extend an app that isn't even passing its current spec
		}
		// FREEZE: challenge an app's architecture at most ONCE, when it first goes
		// green — not on every fixpoint. A working app is left working; the marker is
		// cleared when the objective changes (a new constraint), which re-opens it.
		marker := ns + ":arch-challenged"
		if _, err := ledger.GetRef(marker); err == nil {
			continue // already challenged once — frozen
		}
		added, err := grower.ChallengeArchitecture(ctx, ns, func(urn string) {
			registry.Add(urn)
			activity.SetCell(urn, status.CellNew)
		})
		if err != nil {
			log.Printf("[GROW] architecture challenge %s failed: %v", ns, err)
			continue
		}
		// Mark it challenged (best-effort) so it isn't re-challenged every fixpoint.
		if h, werr := ledger.WriteBlock([]byte("1")); werr == nil {
			_ = ledger.UpdateRef(marker, h)
		}
		total += added
	}
	return total
}

// judgeMotions is the SPARING judgment pass: for each cell in a fully-green app it
// asks (once) whether the cell's autonomous motion actually glides or is a
// degenerate artifact (teleport/freeze/2-oscillation) that gamed the coarse
// trajectory checks; a confirmed-degenerate cell gets a STRONGER trajectory check
// authored and is re-opened to build to it. Free-first: JudgeMotion spends a model
// call only when the cheap distinct-value signal is ambiguous. One-shot per cell
// (a persisted <cell>:motion-judged marker) so a working app isn't re-judged every
// fixpoint. Returns the number of stronger checks authored.
func judgeMotions(ctx context.Context, grower *appgen.Grower, orch *evolution.Orchestrator, registry *evolution.CellRegistry, root string, persist func(string, evolution.FrictionState), activity *status.Broker, ledger *storage.LedgerEngine) int {
	green := fullyGreenApps(ctx, orch, registry)
	n := 0
	for _, u := range registry.List() {
		ns := appNamespace(u)
		if ns == "" || !green[ns] {
			continue // only judge cells in a WORKING app (leave building apps alone)
		}
		marker := u + ":motion-judged"
		if _, err := ledger.GetRef(marker); err == nil {
			continue // already judged once — frozen unless the objective changes
		}
		added, err := grower.JudgeMotion(ctx, u)
		if err != nil {
			log.Printf("[JUDGE] motion judgment %s failed: %v", u, err)
			continue
		}
		// Mark judged (best-effort) so it isn't re-judged every fixpoint.
		if h, werr := ledger.WriteBlock([]byte("1")); werr == nil {
			_ = ledger.UpdateRef(marker, h)
		}
		if added > 0 {
			persist(u, evolution.FrictionState{Root: root}) // re-open to build the stronger check
			activity.Event("create", u, "motion judged degenerate — re-opened for a stronger trajectory check")
			log.Printf("[JUDGE] %s motion judged degenerate — authored %d stronger check(s); re-opened", u, added)
			n += added
		}
	}
	return n
}

// visionCritiqueInterval throttles the sys visual critic's (costly) multimodal
// calls. Overridable via HDM_VISION_INTERVAL (a Go duration).
var visionCritiqueInterval = 120 * time.Second

// visualCritic is HDM's sys VISUAL CRITIC: it watches the FOCUSED application and,
// throttled, asks the multimodal model whether the render satisfies the objective —
// regardless of whether the app is fully green, so it gives feedback on a
// still-building app. It re-critiques whenever the renderer's genotype changes, so
// it forms a feedback loop that can confirm a fix. On a deficient verdict it authors
// a deterministic draw-coverage check (for a grid renderer) and re-opens the cell —
// vision decides WHETHER the render is wrong; a cheap check then drives evolution.
// It never overrides tests. critiquedHash tracks the last genotype critiqued per
// cell; lastAt throttles the call rate. Returns cells re-opened.
func visualCritic(ctx context.Context, grower *appgen.Grower, repo *manifest.Repository, hyp *execution.RuntimeManager, canvas *integration.CanvasUiEngine, canvasSrv *integration.CanvasServer, root string, persist func(string, evolution.FrictionState), activity *status.Broker, ledger *storage.LedgerEngine, critiquedHash map[string]string, lastAt *time.Time) int {
	active := canvasSrv.Active()
	ns := appNamespace(active)
	if ns == "" || time.Since(*lastAt) < visionCritiqueInterval {
		return 0 // no app focused, or throttled
	}
	desc, err := repo.Load(active)
	if err != nil || !appgen.IsUISubsystem(desc.Semantics.FunctionalIntent) {
		return 0 // only critique a renderer
	}
	// Fold operator GUIDANCE into the judgement so the critic adversarially enforces the
	// user's criteria (app + system), not just the objective — the human supplies the
	// criteria, the critic judges against them.
	crit := renderCriteria(ledger, ns)
	// Re-critique when EITHER the genotype OR the operator's criteria change, so newly
	// added guidance is enforced even against an already-converged render.
	critiqueKey := desc.PhenotypeHash + "|" + crit
	if critiquedHash[active] == critiqueKey {
		return 0 // already critiqued this genotype against these criteria
	}
	// Render the focused cell's current output into the frame ring, then look.
	for i := 0; i < 4; i++ {
		_, _ = canvas.ExtractActiveFrame(active)
	}
	objective := ""
	if env := appgen.LoadEnvelope(ledger, ns); env != nil {
		objective = env.Objective
	}
	criteria := ""
	if crit != "" {
		criteria = "\nThe operator additionally requires: " + crit
	}
	const visionSys = "You are a strict, adversarial visual critic for a rendered app frame. Judge ONLY what is visibly rendered in the image(s); try to REFUTE that it satisfies the goal."
	q := fmt.Sprintf("The application's objective is: %q.%s\nDoes the rendered output clearly satisfy the objective AND every stated requirement? Answer ONLY compact JSON: {\"satisfied\": <true|false>, \"reason\": \"<short — name the first unmet requirement>\"}.", objective, criteria)
	ans, verr := hyp.VisionJudge(ctx, visionSys, q, 4)
	if verr != nil {
		log.Printf("[VISION] critic %s failed (will retry): %v", active, verr)
		return 0 // transient — retry next cycle, don't record the hash
	}
	*lastAt = time.Now()
	critiquedHash[active] = critiqueKey
	satisfied, ok := visionSatisfied(ans)
	reason := visionReason(ans)
	activity.Event("vision", active, fmt.Sprintf("critic: %v — %s", satisfied, reason))
	log.Printf("[VISION] critic %s: satisfied=%v — %s", active, satisfied, reason)
	if ok && !satisfied {
		// Persist the critic's REASON so the cell's next build sees what looked wrong
		// (fed into buildSeed) — the rich signal that was previously discarded. Also
		// author the deterministic draw-coverage floor, then re-open so the renderer
		// rebuilds against both. Re-open even if no new check was added: the feedback
		// note itself is new build input. The genotype-hash gate + throttle bound the loop.
		_ = evolution.SaveCriticNote(ledger, active, reason)
		if _, aerr := grower.AuthorVisualCoverage(ctx, active); aerr != nil {
			log.Printf("[VISION] coverage authoring %s failed: %v", active, aerr)
		}
		persist(active, evolution.FrictionState{Root: root})
		log.Printf("[VISION] %s render judged deficient (%q) — saved feedback + re-opened", active, reason)
		return 1
	}
	if ok && satisfied {
		_ = evolution.SaveCriticNote(ledger, active, "") // render looks right — clear stale feedback
	}
	return 0
}

// allCriteria returns the operator's system + app guidance statements — the criteria the
// adversarial critics hold a proposed solution to. Both scopes: a system principle governs
// every app, an app criterion only this one.
func allCriteria(ledger *storage.LedgerEngine, ns string) []string {
	var out []string
	for _, e := range evolution.LoadGuidance(ledger, evolution.SystemGuidanceKey).Entries {
		out = append(out, e.Statement)
	}
	if ns != "" {
		for _, e := range evolution.LoadGuidance(ledger, evolution.AppGuidanceKey(ns)).Entries {
			out = append(out, e.Statement)
		}
	}
	return out
}

// renderCriteria joins the guidance into one clause for the visual critic's question.
func renderCriteria(ledger *storage.LedgerEngine, ns string) string {
	return strings.Join(allCriteria(ledger, ns), "; ")
}

// codeCritiqueInterval throttles the code critic (a full-app-code LLM call) — costlier and
// less time-sensitive than the visual critic, so it runs less often.
var codeCritiqueInterval = 180 * time.Second

// codeCritic is the general (non-visual) adversarial arm: it judges the focused app's cells
// against the operator's criteria using their role, ports, and CODE (the visual critic
// handles look-and-feel). Each violation is fed back as a build note and the cell is
// re-opened. Throttled, and de-duplicated on (criteria + the app's current code) so it only
// re-judges when something changed. Returns cells re-opened.
func codeCritic(ctx context.Context, grower *appgen.Grower, orch *evolution.Orchestrator, repo *manifest.Repository, canvasSrv *integration.CanvasServer, root string, persist func(string, evolution.FrictionState), activity *status.Broker, ledger *storage.LedgerEngine, lastKey *string, lastAt *time.Time) int {
	ns := appNamespace(canvasSrv.Active())
	if ns == "" || time.Since(*lastAt) < codeCritiqueInterval {
		return 0
	}
	criteria := allCriteria(ledger, ns)
	if len(criteria) == 0 {
		return 0 // no operator criteria to enforce against the code
	}
	m := evolution.LoadAppMap(ledger, ns)
	if m == nil {
		return 0
	}
	var fp strings.Builder
	fp.WriteString(strings.Join(criteria, "|"))
	for _, c := range m.Components {
		if d, err := repo.Load(c.Identity); err == nil {
			fp.WriteString("|" + d.PhenotypeHash)
		}
	}
	key := fp.String()
	if *lastKey == key {
		return 0 // same criteria + same code as last judged
	}
	vios, err := grower.CriticizeCode(ctx, ns, criteria)
	if err != nil {
		log.Printf("[CODE-CRITIC] %s failed (will retry): %v", ns, err)
		return 0
	}
	*lastAt = time.Now()
	*lastKey = key
	reopened := 0
	for _, v := range vios {
		_ = evolution.SaveCriticNote(ledger, v.Cell, fmt.Sprintf("criterion not met: %q — %s", v.Criterion, v.Reason))
		activity.Event("mutate", v.Cell, "code criterion violated — "+v.Reason)
		// Reset friction (force a fresh build) ONLY for a cell that already PASSES its
		// acceptance — its checks are too weak, so the note must drive a rebuild. For a cell
		// still FAILING acceptance, do NOT reset: it is already in the stall->fracture ladder,
		// and zeroing its retry count every pass would trap it in a build loop that never
		// escalates to fracture — the exact bug that wedged bouncing_balls' physics at 0/5. The
		// note guides its next build; retryStalled/fractureStalled advance it toward decomposition.
		if p, t, serr := orch.ScoreCell(ctx, v.Cell); serr == nil && t > 0 && p < t {
			log.Printf("[CODE-CRITIC] %s violates %q: %s — noted (failing %d/%d; stall ladder advances toward fracture)", v.Cell, v.Criterion, v.Reason, p, t)
			continue
		}
		persist(v.Cell, evolution.FrictionState{Root: root})
		reopened++
		log.Printf("[CODE-CRITIC] %s violates %q: %s — re-opened (passes acceptance but criterion unmet)", v.Cell, v.Criterion, v.Reason)
	}
	if len(vios) == 0 {
		log.Printf("[CODE-CRITIC] %s: all %d criteria satisfied by the code", ns, len(criteria))
	}
	return reopened
}

// visionSatisfied parses a {"satisfied":bool} verdict from the model's answer;
// ok=false when no JSON verdict is found (treated as "don't act").
func visionSatisfied(ans string) (satisfied, ok bool) {
	i := strings.IndexByte(ans, '{')
	j := strings.LastIndexByte(ans, '}')
	if i < 0 || j <= i {
		return false, false
	}
	var v struct {
		Satisfied bool `json:"satisfied"`
	}
	if json.Unmarshal([]byte(ans[i:j+1]), &v) != nil {
		return false, false
	}
	return v.Satisfied, true
}

// visionReason extracts the short "reason" string from a vision verdict (best
// effort; "" if absent).
func visionReason(ans string) string {
	i := strings.IndexByte(ans, '{')
	j := strings.LastIndexByte(ans, '}')
	if i < 0 || j <= i {
		return ""
	}
	var v struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal([]byte(ans[i:j+1]), &v)
	return v.Reason
}

// syncAppCells returns, per application namespace, the count of SYNC cells (the
// run-tick, non-UI, non-async cells the frame loop ticks each frame) — the divisor
// for each cell's share of the frame budget — and the set of those cells.
func syncAppCells(registry *evolution.CellRegistry, repo *manifest.Repository, hyp *execution.RuntimeManager) (map[string]int, map[string]string) {
	counts := map[string]int{}
	cellNS := map[string]string{}
	for _, u := range registry.List() {
		ns := appNamespace(u)
		if ns == "" {
			continue
		}
		desc, err := repo.Load(u)
		if err != nil || appgen.IsUISubsystem(desc.Semantics.FunctionalIntent) {
			continue
		}
		bc, err := repo.Phenotype(desc)
		if err != nil || hyp.IsAsyncCell(desc.PhenotypeHash, bc) {
			continue
		}
		counts[ns]++
		cellNS[u] = ns
	}
	return counts, cellNS
}

// enforceFrameBudget is the frame-budget cost pass: a sync cell whose measured
// per-frame cost exceeds its share of the frame (making animation stutter) is
// re-opened for efficiency optimization, so the Hamiltonian's latency/fuel terms
// pull it back under the deadline. Only complete cells are re-opened (don't pile on
// a building cell), and each is retried at most maxBudgetReopens times so a cell
// that is already as lean as it can be doesn't churn forever. Returns the count
// re-opened.
func enforceFrameBudget(ctx context.Context, orch *evolution.Orchestrator, registry *evolution.CellRegistry, repo *manifest.Repository, hyp *execution.RuntimeManager, budget *frameBudget, root string, persist func(string, evolution.FrictionState), activity *status.Broker, attempts map[string]int) int {
	const maxBudgetReopens = 3
	counts, cellNS := syncAppCells(registry, repo, hyp)
	n := 0
	for u, ns := range cellNS {
		if !budget.overBudget(u, counts[ns]) {
			continue
		}
		if p, t, err := orch.ScoreCell(ctx, u); err != nil || t == 0 || p < t {
			continue // still building — optimize only what already works
		}
		if attempts[u] >= maxBudgetReopens {
			continue // gave it its tries; it can't get leaner — stop churning
		}
		attempts[u]++
		persist(u, evolution.FrictionState{Root: root}) // re-open for efficiency optimization
		ms := budget.avgNS(u) / 1e6
		activity.Event("optimize", u, fmt.Sprintf("over frame budget (%.1fms) — re-opened to optimize", ms))
		log.Printf("[BUDGET] %s over frame budget (%.2fms avg) — re-opened for optimization (attempt %d/%d)", u, ms, attempts[u], maxBudgetReopens)
		n++
	}
	return n
}

// chooseTarget picks the cell to evolve this frame from the candidate set.
// Cells still being BUILT (unmet acceptance checks, passed < total) are chosen
// FIRST and DIRECTLY — no friction probe, which would skip a cell that traps on
// the fixed probe input, and building doesn't need a friction ranking anyway.
// Only when every candidate is complete does it fall back to friction ranking
// (SelectTarget) to pick what to optimize. Returns "" when nothing is
// evolvable right now (all parked, or only non-run-tick / trap-on-probe cells
// remain) — the caller treats that as a fixpoint.
func chooseTarget(ctx context.Context, orch *evolution.Orchestrator, candidates []string) string {
	var incomplete string
	for _, u := range candidates {
		if passed, total, err := orch.ScoreCell(ctx, u); err == nil && total > 0 && passed < total {
			incomplete = u // build the first incomplete cell directly
			break
		}
	}
	if incomplete != "" {
		return incomplete
	}
	// All complete: optimize the highest-friction probeable cell (if any).
	if t, err := evolution.SelectTarget(ctx, orch.Repository(), candidates, "run-tick", []uint64{0, 0}, 0); err == nil {
		return t
	}
	return ""
}

// structuralPlateauK is how many consecutive failed local optimizations mark a cell as
// "local optimization exhausted" and eligible for structural escalation.
const structuralPlateauK = 2

// structuralFuelFloor bounds escalation to cells still expensive enough that a data structure
// could plausibly help — a fuel-8 cell won't benefit from a dispatch. HDM_STRUCT_FUEL_FLOOR
// overrides (lower it to exercise the path on smaller cells).
func structuralFuelFloor() uint64 {
	if s := os.Getenv("HDM_STRUCT_FUEL_FLOOR"); s != "" {
		if v, err := strconv.ParseUint(s, 10, 64); err == nil {
			return v
		}
	}
	return 32
}

// noteStructuralEscalation is the cost-driven trigger for the structural optimizer: when a
// COMPLETE cell's LOCAL optimization plateaus (attempted but not committed) K times in a row
// while it is still expensive, flag it so its next synthesis is offered the data-structure
// toolkit — it may then refactor to dispatch to a shared primitive (or mint one) instead of
// micro-optimizing. Any commit (including a structural refactor landing) clears the flag.
func noteStructuralEscalation(orch *evolution.Orchestrator, urn string, fr *evolution.FrameResult, plateau map[string]int) {
	if fr == nil || !fr.Attempted {
		return
	}
	if fr.Committed {
		if plateau[urn] > 0 || orch.IsStructural(urn) {
			plateau[urn] = 0
			if orch.IsStructural(urn) {
				orch.SetStructural(urn, false)
				log.Printf("[STRUCT] %s committed — structural flag cleared", urn)
			}
		}
		return
	}
	complete := fr.AcceptTotal == 0 || fr.AcceptCand == fr.AcceptTotal
	var fuel uint64
	if fr.Verdict != nil {
		fuel = fr.Verdict.BaselineFuel
	}
	if !complete || fuel < structuralFuelFloor() {
		return
	}
	plateau[urn]++
	if plateau[urn] == structuralPlateauK && !orch.IsStructural(urn) {
		orch.SetStructural(urn, true)
		log.Printf("[STRUCT] %s plateaued (local optimization exhausted at fuel %d) — escalating to the data-structure toolkit", urn, fuel)
	}
}

var structDetectInterval = 15 * time.Minute

// autoDetectStructure is the recurring-pattern detector that closes the self-extension loop
// WITHOUT an operator: periodically it scans the application-cell corpus, asks the model whether
// several cells share a shape a NEW shared data-structure primitive would serve, and — if a
// grounded proposal comes back — publishes the primitive's ABI as SYSTEM GUIDANCE (so the first
// cell to refactor MINTS it, witnessed by its tapes, and the rest reuse it) and FLAGS the named
// cells structural so they refactor to dispatch to it. Correctness is still guarded downstream by
// the witnessed-minting gate and the trajectory/per-case gauntlets. Throttled.
func autoDetectStructure(ctx context.Context, grower *appgen.Grower, orch *evolution.Orchestrator, registry *evolution.CellRegistry, repo *manifest.Repository, ledger *storage.LedgerEngine, st *time.Time) {
	if time.Since(*st) < structDetectInterval {
		return
	}
	*st = time.Now()
	var cells []appgen.CellWAT
	for _, u := range registry.List() {
		if strings.HasPrefix(u, "urn:hdm:sys:") {
			continue // don't propose promoting a primitive out of the primitives themselves
		}
		desc, err := repo.Load(u)
		if err != nil {
			continue
		}
		wat, err := repo.Genotype(desc)
		if err != nil {
			continue
		}
		cells = append(cells, appgen.CellWAT{URN: u, WAT: wat})
		if len(cells) >= 12 {
			break
		}
	}
	p, err := grower.DetectSharedStructure(ctx, cells)
	if err != nil || p == nil {
		return
	}
	sg := evolution.LoadGuidance(ledger, evolution.SystemGuidanceKey)
	note := fmt.Sprintf("A shared data-structure primitive %s serves a recurring pattern (%s). A cell doing this should dispatch to %s via invoke-cell — mint it if it does not exist yet. Op ABI: %s.",
		p.URN, p.Rationale, p.URN, p.Ops)
	if _, isNew := sg.Add(note); isNew {
		_ = evolution.SaveGuidance(ledger, evolution.SystemGuidanceKey, sg)
	}
	for _, u := range p.Cells {
		orch.SetStructural(u, true)
	}
	log.Printf("[STRUCT] detector proposed shared primitive %s for %d cells (%s) — published guidance + flagged them structural", p.URN, len(p.Cells), p.Rationale)
}

// applicationFirst deprioritizes system infrastructure cells (urn:hdm:sys:*,
// notably the bootstrap optimizer): it returns the non-system candidates when
// any exist, so application and demo cells are evolved first. The optimizer is
// still improvable — it just isn't selected until everything else has been
// driven to convergence.
func applicationFirst(candidates []string) []string {
	if os.Getenv("HDM_SYS_ONLY") != "" {
		return candidates // sys-only run: let sys:* cells compete on friction, not deprioritized
	}
	app := make([]string, 0, len(candidates))
	for _, u := range candidates {
		if !strings.HasPrefix(u, "urn:hdm:sys:") {
			app = append(app, u)
		}
	}
	if len(app) > 0 {
		return app
	}
	return candidates
}

// syncGoalTree loads (or creates) an application's goal tree and seeds it with any
// top-level subsystem the envelope declares that the tree doesn't yet know about.
// Seeding is idempotent and never re-parents: a fracture child already attached
// beneath its parent stays there even though the envelope also lists it flat, so
// the lineage the walk depends on is preserved. Best-effort; returns nil on error.
func syncGoalTree(ledger *storage.LedgerEngine, ns string) *evolution.GoalTree {
	env := appgen.LoadEnvelope(ledger, ns)
	if env == nil {
		return evolution.LoadGoalTree(ledger, ns)
	}
	tr := evolution.LoadGoalTree(ledger, ns)
	if tr == nil {
		tr = evolution.NewGoalTree(ns, env.Objective)
	}
	before := len(tr.Nodes)
	for _, sub := range env.SubsystemRequirements {
		tr.SeedSubsystem(sub.Identity, sub.Semantics)
	}
	if len(tr.Nodes) != before {
		if err := evolution.SaveGoalTree(ledger, ns, tr); err != nil {
			log.Printf("[WALK] persist goal tree %s: %v", ns, err)
		}
	}
	return tr
}

// walkOrder turns the breadth-first candidate set into a DEPTH-FIRST walk: within
// each application it reorders candidates into the goal tree's pre-order, so a
// fractured parent's children are built immediately after it and before the next
// sibling subsystem. Because chooseTarget locks onto the first incomplete candidate
// until it converges, this ordering alone makes the scheduler finish one path
// before spreading to the next — the Phase-1 "finished spine". Candidates in no
// tracked app keep their original relative order at the tail.
func walkOrder(candidates []string, ledger *storage.LedgerEngine) []string {
	if len(candidates) < 2 {
		return candidates
	}
	// Group by namespace, preserving first-appearance order; untracked cells trail.
	var nsOrder []string
	groups := map[string][]string{}
	var tail []string
	for _, u := range candidates {
		ns := appNamespace(u)
		if ns == "" {
			tail = append(tail, u)
			continue
		}
		if _, ok := groups[ns]; !ok {
			nsOrder = append(nsOrder, ns)
		}
		groups[ns] = append(groups[ns], u)
	}
	out := make([]string, 0, len(candidates))
	for _, ns := range nsOrder {
		out = append(out, syncGoalTree(ledger, ns).Order(groups[ns])...)
	}
	return append(out, tail...)
}

// noteConvergence updates a cell's persisted, root-keyed friction from a frame
// result: a commit resets it (still had friction); enough consecutive
// no-progress holds mark it converged AT THE CURRENT ROOT. A cell converged
// against a stale root re-confirms cheaply (its prior hold count carries over).
// Reports whether this frame tipped the cell into convergence (so the caller can
// try to extend its spec before retiring it).
func noteConvergence(fr *evolution.FrameResult, friction map[string]evolution.FrictionState, root string, persist func(string, evolution.FrictionState), activity *status.Broker) (justConverged bool) {
	if fr == nil || fr.TargetURN == "" {
		return false
	}
	u := fr.TargetURN
	if fr.Transport {
		// The model server was unreachable — infrastructure, not the cell. Do not
		// count it as a convergence hold, or a transient outage would park every
		// cell it touched. Leave the cell active to retry once the model returns.
		return false
	}
	if fr.Committed {
		persist(u, evolution.FrictionState{Converged: false, Holds: 0, Root: root})
		return false
	}
	fs := friction[u]
	wasParked := fs.Parked(root)
	fs.Holds++
	fs.Root = root
	if fs.Holds >= convergenceHolds {
		fs.Converged = true
		// A cell parked while still failing acceptance checks is STALLED (the
		// model is stuck), not done — it must be retried, never retired.
		fs.Incomplete = fr.AcceptTotal > 0 && fr.AcceptBase < fr.AcceptTotal
		persist(u, fs)
		if !wasParked {
			if fs.Incomplete {
				activity.Event("hold", u, fmt.Sprintf("stalled at %d/%d — will retry", fr.AcceptBase, fr.AcceptTotal))
				log.Printf("[EVOLVE] %s STALLED at %d/%d checks (%d holds) — parked for retry", u, fr.AcceptBase, fr.AcceptTotal, fs.Holds)
			} else {
				activity.Event("converged", u, fmt.Sprintf("no friction at root %s after %d holds", shortHash(root), fs.Holds))
				log.Printf("[EVOLVE] %s converged at root %s (%d holds) — dropping from selection", u, shortHash(root), fs.Holds)
				return true
			}
		}
		return false
	}
	fs.Converged = false
	persist(u, fs)
	return false
}

// defaultHeartbeat is the production tick interval. Because the bootstrap cell
// calls the cognitive engine on every tick, the interval must exceed one model
// round-trip so ticks do not queue behind in-flight inference. Override with
// the HDM_HEARTBEAT env var (a Go duration, e.g. "10s", "30s").
const defaultHeartbeat = 15 * time.Second

// resolveHeartbeat returns the configured tick interval.
func resolveHeartbeat() time.Duration {
	if v := os.Getenv("HDM_HEARTBEAT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		log.Printf("[WARN] invalid HDM_HEARTBEAT %q; using default %s", v, defaultHeartbeat)
	}
	return defaultHeartbeat
}

// defaultFPS is the app frame rate — the cadence at which the active application's
// sync cells advance live shared state, decoupled from the (slow) evolution
// heartbeat. Overridable via HDM_FPS.
const defaultFPS = 30

// resolveFPS returns the configured app frame rate (frames/second).
func resolveFPS() int {
	if v := os.Getenv("HDM_FPS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 240 {
			return n
		}
		log.Printf("[WARN] invalid HDM_FPS %q; using default %d", v, defaultFPS)
	}
	return defaultFPS
}

// runFrameLoop is the APP frame loop: at ~HDM_FPS it advances the active
// application's live simulation by running each of its SYNC (run-tick) cells once
// per frame on live shared memory, so the app animates at display rate independent
// of the model-paced evolution loop. UI (render-frame) cells are not ticked here —
// they are drawn on demand when the canvas polls /canvas/frame. Evolution keys off
// the (much slower) heartbeat, so a model round-trip never stalls a frame.
//
// Each frame reloads a cell only when its phenotype hash changed (TickAppCell), so
// a hot-swap from evolution is picked up without recompiling every frame. All guest
// execution serializes on the hypervisor lock, so this is safe alongside evolution,
// RenderFrame, and the input gateway.
// seedLiveState boots an application's shared state into a valid initial condition
// by writing, into live memory, the seed values the app's own ACCEPTED scenarios
// use — so a cold live start matches a state the cells were verified against (in
// their own numeric representation), rather than all-zeros (where dot += velocity
// with velocity 0 never animates). For each contract offset it prefers a non-zero
// seed (so a velocity field gets a moving value). Returns the number of fields set.
func seedLiveState(hyp *execution.RuntimeManager, ledger *storage.LedgerEngine, registry *evolution.CellRegistry, ns string) int {
	// Boot from a SINGLE scenario's seed set, not a mix across scenarios: one
	// scenario is a self-consistent snapshot (e.g. dot mid-window WITH a matching
	// window size and velocity), whereas merging offsets from different scenarios
	// yields an incoherent state (window from one, position from another). Pick the
	// scenario with the most contract-region seeds, preferring a "normal movement"
	// case (all-relative postconditions) over a boundary/edge case so the dot starts
	// inside the window and moving.
	var best map[uint32]uint32
	bestScore := -1
	consider := func(sc evolution.Scenario) {
		// Never boot from an input-driven case (a seed in the HMI register): that
		// state only makes sense mid-keypress, not as a cold start.
		m := map[uint32]uint32{}
		for _, sd := range sc.Seed {
			off, err := strconv.ParseInt(strings.TrimSpace(sd.At), 0, 64)
			if err != nil || len(sd.U32) == 0 {
				continue
			}
			if off >= 0x50000 && off < 0x51000 {
				return // input-driven scenario — not a boot state
			}
			// A SeedWrite packs consecutive words: U32[i] lands at off+4*i (e.g.
			// [dot_x,dot_y,dot_dx,dot_dy,w,h] at 0xB0000). Expand ALL of them — taking
			// only the first word leaves velocity/window zero and the sim frozen.
			for i, w := range sd.U32 {
				o := off + int64(4*i)
				if o >= 0xB0000 && o < 0xC0000 {
					m[uint32(o)] = w
				}
			}
		}
		if len(m) == 0 {
			return
		}
		// DOMINANT preference for an autonomous-movement scenario (its reads are all
		// relative — increased/decreased/changed — with no boundary threshold, or it
		// carries a trajectory assertion): that scenario's seed is the canonical
		// "running" state the cell is VERIFIED to glide from, so booting live from it
		// makes live behavior match the sandbox. A boundary/collision case parks the
		// dot at a wall with zero velocity — a terrible cold start — so it must never
		// win on seed count alone.
		score := len(m)
		movement := len(sc.Expect.Trajectory) > 0
		if len(sc.Expect.Reads) > 0 {
			movement = true
			for _, r := range sc.Expect.Reads {
				switch r.Cmp {
				case "", "eq", "gt", "lt", "ge", "le", "ne":
					movement = false
				}
			}
		}
		if movement {
			score += 1000
		}
		if score > bestScore {
			bestScore, best = score, m
		}
	}
	var suites []*evolution.AcceptanceSuite
	for _, u := range registry.List() {
		if appNamespace(u) != ns {
			continue
		}
		if suite, _ := evolution.LoadAcceptance(ledger, u); suite != nil {
			suites = append(suites, suite)
			for _, sc := range suite.Scenarios {
				consider(sc)
			}
		}
	}
	if best == nil {
		return 0
	}
	// Fill any contract field the chosen snapshot didn't set (typically static
	// config like window dimensions, which every scenario agrees on) from other
	// scenarios, preferring a non-zero value — so the dot starts moving inside a
	// real window rather than an unbounded (0-size) one.
	for _, suite := range suites {
		for _, sc := range suite.Scenarios {
			for _, sd := range sc.Seed {
				off, err := strconv.ParseInt(strings.TrimSpace(sd.At), 0, 64)
				if err != nil || len(sd.U32) == 0 {
					continue
				}
				for i, w := range sd.U32 {
					o := off + int64(4*i)
					if o < 0xB0000 || o >= 0xC0000 {
						continue
					}
					if cur, ok := best[uint32(o)]; !ok || (cur == 0 && w != 0) {
						best[uint32(o)] = w
					}
				}
			}
		}
	}
	n := 0
	for off, v := range best {
		if hyp.PokeU32(off, v) {
			n++
		}
	}
	return n
}

// frameSkip is whole-cell frame-level MEMOIZATION: it skips ticking a DETERMINISTIC
// cell whose DECLARED read-set is byte-for-byte unchanged since it last ran (its
// outputs already sit in shared memory). A static compute — a fixed-view Mandelbrot —
// thus runs once, then its map driver (and any input-stable cell) is skipped every
// frame instead of re-dispatching thousands of leaf calls. It is self-correcting:
// whenever an input changes the cell re-runs, converging to a fixpoint regardless of
// tick order. Soundness rests on the declared read-set being complete; a future
// enforced read/write mask would make that guarantee unconditional.
type frameSkip struct {
	hyp    *execution.RuntimeManager
	ledger *storage.LedgerEngine

	inputHash map[string]uint64      // urn -> hash of read-set bytes when it last ran
	pheno     map[string]string      // urn -> phenotype hash the info was built for
	ranges    map[string][][2]uint32 // urn -> cached read-set [off,len] byte ranges
	exclusive map[string]bool        // urn -> it is the SOLE writer of its outputs
	am        map[string]*evolution.AppMap
	ct        map[string]*evolution.AppContract
	amAt      map[string]int // ns -> frame when map/contract were last (re)loaded
	skips     uint64
}

func newFrameSkip(hyp *execution.RuntimeManager, ledger *storage.LedgerEngine) *frameSkip {
	return &frameSkip{
		hyp: hyp, ledger: ledger,
		inputHash: map[string]uint64{}, pheno: map[string]string{}, ranges: map[string][][2]uint32{},
		exclusive: map[string]bool{},
		am:        map[string]*evolution.AppMap{}, ct: map[string]*evolution.AppContract{}, amAt: map[string]int{},
	}
}

// ensureMap (re)loads the app map + contract for a namespace at most ~once per second.
func (fs *frameSkip) ensureMap(ns string, frame int) {
	if fs.am[ns] == nil || frame-fs.amAt[ns] > 30 {
		if m := evolution.LoadAppMap(fs.ledger, ns); m != nil {
			fs.am[ns] = m
		}
		fs.ct[ns] = evolution.LoadContract(fs.ledger, ns)
		fs.amAt[ns] = frame
	}
}

// buildInfo (re)derives a cell's read-set and sole-writer flag when its phenotype
// changed. Must be called with the app map loaded (ensureMap).
func (fs *frameSkip) buildInfo(ns, urn, phenoHash string) {
	if fs.pheno[urn] == phenoHash {
		return
	}
	fs.ranges[urn] = fs.readSetRanges(ns, urn)
	fs.exclusive[urn] = fs.writesExclusively(ns, urn)
	fs.pheno[urn] = phenoHash
	delete(fs.inputHash, urn)
}

// shouldRun reports whether to tick this cell now (and refreshes its cached info). It
// returns false only for a deterministic, sole-writer cell with a non-empty read-set
// whose bytes are identical to its last run.
func (fs *frameSkip) shouldRun(ns, urn, phenoHash string, bytecode []byte, frame int) bool {
	fs.ensureMap(ns, frame)
	fs.buildInfo(ns, urn, phenoHash)
	if !fs.hyp.CellIsDeterministic(phenoHash, bytecode) {
		return true // reads a clock/random/model — output isn't a function of memory
	}
	if !fs.exclusive[urn] {
		return true // another cell also writes this cell's outputs — a skip would let
		// them clobber its frozen result, so it must re-run every frame
	}
	ranges := fs.ranges[urn]
	if len(ranges) == 0 {
		return true // unknown or empty read-set — never skip (conservative)
	}
	h, ok := fs.hashRanges(ranges)
	if !ok {
		return true
	}
	if prev, seen := fs.inputHash[urn]; seen && prev == h {
		fs.skips++
		return false
	}
	fs.inputHash[urn] = h
	return true
}

// buildMasks resolves a component's DENY-BY-DEFAULT read/write masks to absolute byte
// ranges over the contract: poison = every field it did NOT explicitly declare reading,
// revert = every field it did NOT explicitly declare writing. Only what a cell EXPLICITLY
// declares is permitted; everything else is hidden (reads) or undone (writes). "HMI
// input" is a declared read of the input window, not a contract field, so it is never a
// poison target. Returns nil,nil if there are no contract fields to govern.
func buildMasks(comp *evolution.ComponentMap, ct *evolution.AppContract) (poison, revert [][2]uint32) {
	if comp == nil || ct == nil {
		return nil, nil
	}
	readable := map[string]bool{}
	for _, f := range comp.DeclaredReads { // EXPLICIT declarations only
		readable[strings.ToLower(f)] = true
	}
	writable := map[string]bool{}
	for _, f := range comp.DeclaredWrites {
		writable[strings.ToLower(f)] = true
	}
	for _, f := range ct.Fields {
		off, n, ok := ct.FieldRange(f.Name)
		if !ok || n <= 0 {
			continue
		}
		name := strings.ToLower(f.Name)
		r := [2]uint32{uint32(off), uint32(n)}
		if !writable[name] {
			revert = append(revert, r)
			// Poison ONLY a field the cell can neither read nor write. A field it may
			// write is left alone: zeroing it and then not rewriting it that tick would
			// leak a 0 (poison isn't restored for writable fields since they're not in
			// revert). This keeps poison ⊆ revert, so every hidden field is restored.
			if !readable[name] {
				poison = append(poison, r)
			}
		}
	}
	return poison, revert
}

// refreshMasks registers a deny-by-default read/write mask for EVERY application cell
// with the runtime, so enforcement is uniform across all live execution paths (frame
// tick, async tick, render-frame, and nested dispatch). Cells absent from an app map
// (system/library cells) are left unmasked. Best-effort; skips namespaces without a
// contract. denyReads=false leaves the read side open (write-only enforcement).
func refreshMasks(hyp *execution.RuntimeManager, repo *manifest.Repository, ledger *storage.LedgerEngine, registry *evolution.CellRegistry, denyReads bool) {
	maps := map[string]*evolution.AppMap{}
	cts := map[string]*evolution.AppContract{}
	for _, u := range registry.List() {
		ns := appNamespace(u)
		if ns == "" {
			continue
		}
		if _, ok := maps[ns]; !ok {
			maps[ns] = evolution.LoadAppMap(ledger, ns)
			cts[ns] = evolution.LoadContract(ledger, ns)
		}
		m, ct := maps[ns], cts[ns]
		if m == nil || ct == nil {
			continue
		}
		var comp *evolution.ComponentMap
		for i := range m.Components {
			if m.Components[i].Identity == u {
				comp = &m.Components[i]
				break
			}
		}
		if comp == nil {
			continue // not modeled yet — leave unmasked until the map catches up
		}
		poison, revert := buildMasks(comp, ct)
		// A DISPATCHING cell reads whatever its callees read (e.g. a map driver whose
		// leaf reads max_iter) — an unbounded, undeclarable transitive set. Enforce its
		// writes but leave its reads open, so it can't starve a nested cell of an input.
		if !denyReads {
			poison = nil
		} else if desc, err := repo.Load(u); err == nil {
			if bc, perr := repo.Phenotype(desc); perr == nil && hyp.CellImportsDispatch(desc.PhenotypeHash, bc) {
				poison = nil
			}
		}
		hyp.SetCellMask(u, poison, revert)
	}
}

// readSetRanges resolves a cell's declared+verified read-set (contract fields, plus the
// HMI input window) to absolute [offset,len] byte ranges. Returns nil if the cell is
// unknown to the app map.
func (fs *frameSkip) readSetRanges(ns, urn string) [][2]uint32 {
	m := fs.am[ns]
	if m == nil {
		return nil
	}
	var comp *evolution.ComponentMap
	for i := range m.Components {
		if m.Components[i].Identity == urn {
			comp = &m.Components[i]
			break
		}
	}
	if comp == nil {
		return nil
	}
	fields := map[string]bool{}
	for _, f := range comp.DeclaredReads {
		fields[f] = true
	}
	for _, f := range comp.Reads {
		fields[f] = true
	}
	out := [][2]uint32{}
	for f := range fields {
		if strings.EqualFold(f, "HMI input") {
			out = append(out, [2]uint32{hmiInputOffset, hmiInputBytes})
			continue
		}
		if off, n, ok := fs.ct[ns].FieldRange(f); ok && n > 0 {
			out = append(out, [2]uint32{uint32(off), uint32(n)})
		}
	}
	return out
}

// writesExclusively reports whether this cell is the SOLE writer of every field it
// writes. If another cell also writes one of its outputs, skipping this cell would let
// that other writer clobber its frozen result — so it must never be skipped. Sound
// because it over-approximates writers (declared ∪ verified) across the whole app.
func (fs *frameSkip) writesExclusively(ns, urn string) bool {
	m := fs.am[ns]
	if m == nil {
		return false // unknown app structure — be safe, don't skip
	}
	writers := map[string]map[string]bool{} // field -> set of writer URNs
	var mine []string
	for i := range m.Components {
		c := &m.Components[i]
		for _, f := range append(append([]string{}, c.DeclaredWrites...), c.Writes...) {
			if writers[f] == nil {
				writers[f] = map[string]bool{}
			}
			writers[f][c.Identity] = true
			if c.Identity == urn {
				mine = append(mine, f)
			}
		}
	}
	if len(mine) == 0 {
		return false // writes nothing we can see — nothing to protect, but also no
		// benefit; treat as non-exclusive so we don't skip on an empty write picture
	}
	for _, f := range mine {
		if len(writers[f]) > 1 {
			return false
		}
	}
	return true
}

// hashRanges FNV-hashes the live bytes of a cell's read-set; ok=false if a range can't
// be read (forcing a run rather than a stale skip).
func (fs *frameSkip) hashRanges(ranges [][2]uint32) (uint64, bool) {
	h := fnv.New64a()
	var tmp [4]byte
	for _, r := range ranges {
		binary.LittleEndian.PutUint32(tmp[:], r[0]) // include offset so ranges can't alias
		h.Write(tmp[:])
		b, ok := fs.hyp.PeekBytes(r[0], r[1])
		if !ok {
			return 0, false
		}
		h.Write(b)
	}
	return h.Sum64(), true
}

const (
	hmiInputOffset = 0x50000
	hmiInputBytes  = 0x1000
)

func runFrameLoop(ctx context.Context, hyp *execution.RuntimeManager, repo *manifest.Repository, registry *evolution.CellRegistry, canvasSrv *integration.CanvasServer, ledger *storage.LedgerEngine, budget *frameBudget, fps int) {
	ticker := time.NewTicker(time.Second / time.Duration(fps))
	defer ticker.Stop()
	seeded := map[string]bool{} // apps booted into a valid initial state this session
	skip := newFrameSkip(hyp, ledger)
	memoOn := os.Getenv("HDM_MEMO") != "0" // frame-level memoization on by default
	frame := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// The frame is the app's unit of time: advance the guest clock
			// (chronos.tick()) every frame so a time-driven cell animates at frame
			// rate rather than at the (much slower) evolution heartbeat.
			hyp.AdvanceTick()
			frame++
			if frame%300 == 0 && skip.skips > 0 {
				log.Printf("[FRAME] memoization skipped %d cell-ticks (inputs unchanged)", skip.skips)
			}
			ns := appNamespace(canvasSrv.Active())
			if ns == "" {
				continue // no app focused — nothing to animate
			}
			// Boot the app's live state once, from its own accepted scenario seeds,
			// so the simulation starts from a valid (moving) condition.
			if !seeded[ns] {
				if n := seedLiveState(hyp, ledger, registry, ns); n > 0 {
					seeded[ns] = true
					log.Printf("[FRAME] %s: seeded %d live shared-state field(s) from accepted scenarios", ns, n)
				}
			}
			for _, u := range registry.List() {
				if appNamespace(u) != ns {
					continue
				}
				desc, err := repo.Load(u)
				if err != nil {
					continue
				}
				if appgen.IsUISubsystem(desc.Semantics.FunctionalIntent) {
					continue // renderer draws on canvas poll, not here
				}
				bc, err := repo.Phenotype(desc)
				if err != nil {
					continue
				}
				if hyp.IsAsyncCell(desc.PhenotypeHash, bc) {
					continue // async (cognitive-engine) cell — the async worker ticks it
				}
				// MEMOIZATION: skip a deterministic cell whose declared inputs are
				// unchanged since it last ran — its outputs already sit in shared memory.
				if memoOn && !skip.shouldRun(ns, u, desc.PhenotypeHash, bc, frame) {
					continue
				}
				// Advance live state; ignore per-frame errors (a trapping cell mid-build
				// shouldn't kill the loop) — it will be fixed by evolution. Measure the
				// per-frame cost so an over-budget cell can be optimized to fit. The cell's
				// enforced mask (if any) is applied inside execTrampoline automatically.
				t0 := time.Now()
				_, _, _, _ = hyp.TickAppCell("urn:hdm:sys:frame", u, desc.PhenotypeHash, bc)
				budget.record(u, float64(time.Since(t0).Nanoseconds()))
			}
		}
	}
}

// runAsyncLoop ticks the focused app's ASYNC cells — those importing the cognitive
// engine, whose run-tick can take seconds. It runs OFF the frame thread: a tick
// holds the runtime lock only for the cell's brief guest compute and yields it
// during the model round-trip (invokeReasoningUnlocked), so the 30 FPS frame loop
// never stalls. Async cells write live shared state directly; keeping their writes
// to their declared Writes ports (which sync cells don't write) avoids clobber.
// One worker ⇒ async cells are ticked one at a time, naturally paced by the model.
func runAsyncLoop(ctx context.Context, hyp *execution.RuntimeManager, repo *manifest.Repository, registry *evolution.CellRegistry, canvasSrv *integration.CanvasServer) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ns := appNamespace(canvasSrv.Active())
			if ns == "" {
				continue
			}
			for _, u := range registry.List() {
				if appNamespace(u) != ns {
					continue
				}
				desc, err := repo.Load(u)
				if err != nil {
					continue
				}
				if appgen.IsUISubsystem(desc.Semantics.FunctionalIntent) {
					continue
				}
				bc, err := repo.Phenotype(desc)
				if err != nil {
					continue
				}
				if !hyp.IsAsyncCell(desc.PhenotypeHash, bc) {
					continue // sync cell — the frame loop ticks it
				}
				// Blocks this worker across the model call (fine); the frame loop keeps
				// rendering because the lock is released during that call.
				_, _, _, _ = hyp.TickAppCell("urn:hdm:sys:async", u, desc.PhenotypeHash, bc)
			}
		}
	}
}

func main() {
	// Setup clean signal catching for a zero-leak shutdown sequence.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 1. Bring LedgerEngine online. The DB path is overridable (HDM_DB) so runs
	//    can be isolated; it persists all state, so a restart resumes where it
	//    left off.
	dbPath := os.Getenv("HDM_DB")
	if dbPath == "" {
		dbPath = "hdm.db"
	}
	ledger, err := storage.NewLedgerEngine(dbPath)
	if err != nil {
		log.Fatalf("Ledger initialization failed: %v", err)
	}
	log.Printf("[LEDGER] %s", dbPath)
	defer ledger.Close()
	// Seed the growable knowledge base (documents + compile-checked worked examples)
	// that retrieval injects into synthesis. Idempotent, so it is safe every boot.
	evolution.SeedKnowledge(ledger)

	// 2. Wire the cognitive engine. Two backends: the local OpenAI-compatible MLX
	//    server (default) or the Google Gemini API. Selecting Gemini needs no
	//    rebuild: set GEMINI_API_KEY (or drop the key in .gemini_api_key), or set
	//    HDM_LLM_PROVIDER=gemini. Model/endpoint stay overridable via
	//    HDM_LLM_MODEL / HDM_LLM_URL.
	modelClient := buildModelClient()
	// The router feeds the orchestrator/grower and token accounting; the base client
	// stays for the RuntimeManager's vision path. With no HDM_LLM_MODEL_<TYPE>
	// overrides the router resolves every type to the base — identical to today.
	router := buildModelRouter(modelClient, evolution.LoadPolicy(ledger).ModelBindings)
	// Free the local models HDM loaded when it exits (opt-in) — helpful when several
	// types bind different models and the box is memory-constrained. Uses a fresh
	// context since the run context is already cancelled by the time this defer runs.
	if os.Getenv("HDM_UNLOAD_ON_EXIT") != "" {
		defer func() {
			uctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			router.UnloadAll(uctx)
			log.Printf("[COGNITION] unloaded model(s) on exit (HDM_UNLOAD_ON_EXIT)")
		}()
	}

	// 3. Bring RuntimeManager online with the ledger + cognitive engine wired
	//    into the kernel host interfaces (block-storage, cognitive-engine,
	//    compiler-service, cell-logger).
	hypervisor, err := execution.NewRuntimeManager(ctx, ledger, modelClient)
	if err != nil {
		log.Fatalf("Hypervisor initialization failed: %v", err)
	}
	// Route the multimodal vision path to the vision-typed client (the base client
	// when no HDM_LLM_MODEL_VISION override is set, so this is a no-op by default).
	hypervisor.SetVisionClient(router.For(string(inference.ModelVision)))
	// Apply the ledger policy's evolvable model settings (cost tiers + any bindings).
	applyModelPolicy(ledger, router, hypervisor)
	defer hypervisor.Close(ctx)

	// 4. Bring CompilerService online.
	sieve := compiler.NewCompilerService()

	// 4. Compile bootstrap-optimizer.wat.
	watBytes, err := os.ReadFile("bootstrap-optimizer.wat")
	if err != nil {
		log.Fatalf("Failed to read bootstrap-optimizer.wat: %v", err)
	}

	artifact, err := sieve.CompileGenotype(string(watBytes))
	if err != nil || !artifact.SyntaxPassed {
		log.Fatalf("Compiler failure on bootstrap: %v", err)
	}

	// Seed the optimizer as a node descriptor linking its genotype (WAT
	// source) and phenotype (compiled bytecode), and point the reference at it
	// only if it is not already live (idempotent bootstrap).
	urn := "urn:hdm:sys:optimizer"
	repo := manifest.NewRepository(ledger)
	if needsSeed(ledger, repo, urn) {
		sem := manifest.SemanticManifest{
			FunctionalIntent: "self-optimizing annealing tick for the HDM cluster",
			OutputInvariants: []string{"run-tick returns 1 on success"},
			DomainTags:       []string{"optimizer"},
			EffectfulImports: []manifest.ImportRef{
				{Module: "hdm:kernel/cognitive-engine", Name: "invoke-reasoning"},
			},
		}
		descHash, _, err := repo.PutCell(urn, string(watBytes), artifact.Bytecode, sem, 0)
		if err != nil {
			log.Fatalf("Failed to seed optimizer descriptor: %v", err)
		}
		if err := repo.SeedRef(urn, descHash); err != nil {
			log.Fatalf("Failed to point optimizer reference: %v", err)
		}
	}

	// Seed the demo cells (idempotent): "wasteful" gives the loop fuel headroom
	// to optimize; "branchy" gives the Discovery-Invariant compactor diverse
	// inputs (branches + a fault path). HDM_SYS_ONLY skips them so the evolution
	// loop reaches the sys:* primitives directly (they are deprioritized behind any
	// non-sys cell) — used to exercise/observe sys-cell self-improvement in isolation.
	demoURN := "urn:hdm:demo:wasteful"
	branchyURN := "urn:hdm:demo:branchy"
	if os.Getenv("HDM_SYS_ONLY") == "" {
		if err := seedCell(ledger, repo, sieve, demoURN, wastefulCell, manifest.SemanticManifest{
			FunctionalIntent: "demo cell with redundant work; run-tick always returns 1",
			OutputInvariants: []string{"run-tick returns 1"},
			DomainTags:       []string{"demo", "wasteful"},
		}); err != nil {
			log.Fatalf("Failed to seed wasteful demo cell: %v", err)
		}
		if err := seedCell(ledger, repo, sieve, branchyURN, branchyCell, manifest.SemanticManifest{
			FunctionalIntent: "demo cell with input-dependent branches and a fault path",
			OutputInvariants: []string{"run-tick returns the input byte; byte 0 traps"},
			DomainTags:       []string{"demo", "branchy"},
		}); err != nil {
			log.Fatalf("Failed to seed branchy demo cell: %v", err)
		}
	}

	// Seed the functional-combinator library (idempotent): map/fold/filter/iterate/
	// scan/zip as system cells that apply a passed-in FUNCTION CELL across data via
	// cell-dispatch, so an app writes a small imperative leaf and composes it with a
	// correct, reusable combinator instead of hand-writing a whole numeric kernel.
	combinators := append(execution.SystemCombinators(), execution.StreamCombinators()...)
	for _, c := range combinators {
		if err := seedCell(ledger, repo, sieve, c.URN, c.WAT, manifest.SemanticManifest{
			FunctionalIntent: c.Intent,
			DomainTags:       []string{"sys", "combinator"},
		}); err != nil {
			log.Fatalf("Failed to seed combinator %s: %v", c.URN, err)
		}
	}

	// 5. Bring the evolutionary orchestrator online, with co-mutation tracking
	//    recording isolation passes.
	orchestrator := evolution.NewOrchestrator(ledger, router)
	orchestrator.Gravity = codependency.NewTracker(ledger)
	// Route the evolution-loop sieve by the target cell's kind (render → vision,
	// compute/leaf → code). Injected to avoid an evolution→appgen import cycle.
	orchestrator.SieveModelType = func(urn string) inference.ModelType {
		return appgen.ModelTypeForCell(ledger, urn)
	}
	tapeStore := evolution.NewTapeStore(ledger)
	orchestrator.Tapes = tapeStore
	// Activity broker: the orchestrator, grower, and scheduler push phase/event
	// signals here; the edge console polls /status to show what the loop is doing
	// and, when idle, why it is waiting.
	activity := status.New()
	orchestrator.Activity = activity
	// Feed the live data-flow log: every reasoning round-trip reports what
	// actually flowed (purpose, prompt/response sizes, tokens, duration).
	router.SetObserve(func(purpose string, promptBytes, respBytes, tokens int, ms int64) {
		activity.Flow("infer", purpose, fmt.Sprintf("%s→%s · %d tok · %dms",
			humanBytes(promptBytes), humanBytes(respBytes), tokens, ms))
	})
	resolve := func(u string) ([]byte, bool) {
		d, err := repo.Load(u)
		if err != nil {
			return nil, false
		}
		bc, err := repo.Phenotype(d)
		if err != nil {
			return nil, false
		}
		return bc, true
	}
	janitor := evolution.NewJanitor(tapeStore, "run-tick", evolution.DefaultPayloadOffset, evolution.DefaultStateWindow, resolve)
	initialCells := []string{urn}
	if os.Getenv("HDM_SYS_ONLY") == "" {
		initialCells = append(initialCells, demoURN, branchyURN)
	}
	registry := evolution.NewCellRegistry(initialCells...)
	for _, u := range registry.List() {
		activity.SetCell(u, status.CellLive)
	}
	grower := appgen.NewGrower(ledger, router)
	grower.Activity = activity

	// Resume: re-enroll application subsystems grown in previous sessions so the
	// loop picks up where it left off (the ledger persists them; the in-memory
	// registry does not survive a restart).
	bootUICell := rehydrateApps(ledger, repo, registry, activity)

	// Natural-Language Target Axiom Compiler: a requirement supplied via
	// HDM_TARGET is compiled into a structured target and its importance is
	// propagated into cell saliency.
	axiomCompiler := axiom.NewCompiler(ledger, modelClient)

	// Live observer dashboard (HDM_TUI=1 for the full-screen ANSI view).
	dash := tui.New(os.Getenv("HDM_TUI") != "", tui.StateSource{
		Ledger: ledger, Repo: repo, Tapes: tapeStore, Gravity: orchestrator.Gravity,
		Axiom: axiomCompiler, Cells: registry.List,
	})
	// Tee the log into an in-memory ring buffer so the inspect API's /log endpoint
	// can serve the tail read-only. The primary sink stays stderr (or the TUI
	// dashboard when HDM_TUI is set).
	logBuf := newLogRing(1000)
	var logSink io.Writer = os.Stderr
	if os.Getenv("HDM_TUI") != "" {
		logSink = dash // route [EVOLVE]/[JANITOR]/[AXIOM]/… into the dashboard
	}
	log.SetOutput(io.MultiWriter(logSink, logBuf))
	dash.Start(ctx)

	// Monadic edge services: load the router + UI cells and start the always-on
	// localhost Edge Ingress Gateway.
	const routerURN, uiURN, lifeURN = "urn:hdm:cells:network:router", "urn:hdm:ui:checkout", "urn:hdm:ui:life"
	for urn, wat := range map[string]string{routerURN: routerCell, uiURN: uiCell, lifeURN: lifeCell} {
		art, cErr := sieve.CompileGenotype(wat)
		if cErr != nil || !art.SyntaxPassed {
			log.Fatalf("compile edge cell %s: %v", urn, cErr)
		}
		if err := hypervisor.LoadCell(urn, art.Bytecode); err != nil {
			log.Fatalf("load edge cell %s: %v", urn, err)
		}
	}
	// Surface the renderable UI cells in the console cell list.
	registry.Add(uiURN, lifeURN)
	activity.SetCell(uiURN, status.CellLive)
	activity.SetCell(lifeURN, status.CellLive)

	// Activate the WAT substrate: enrol the sys combinators in evolution behind a
	// behavior-pinning acceptance (which dispatches a fixture leaf through the sandbox
	// resolver). They then evolve toward LOWER FUEL — a fused/faster map — while any
	// mutation that changes their behavior is rejected, and the winners are epoch-frozen.
	// sys cells are application-deprioritized, so this happens in idle time. HDM_EVOLVE_SYS=0
	// disables it.
	if os.Getenv("HDM_EVOLVE_SYS") != "0" {
		for _, leaf := range appgen.CombinatorTestLeaves() {
			_ = seedCell(ledger, repo, sieve, leaf.URN, leaf.WAT, manifest.SemanticManifest{
				FunctionalIntent: "combinator verification leaf", DomainTags: []string{"sys", "test"},
			})
		}
		for urn, suite := range appgen.EvolvableCombinators(evolution.DefaultPayloadOffset) {
			if err := evolution.SaveAcceptance(ledger, urn, suite); err == nil {
				registry.Add(urn)
				log.Printf("[SYS] %s enrolled in evolution (%d acceptance scenarios) — evolvable toward lower fuel", urn, len(suite.Scenarios))
			}
		}
		// Private-page primitives (dict/list/set) enroll through the SAME acceptance gate a
		// runtime self-extension would use (appgen.PromotePrimitive): a primitive is admitted
		// only if it compiles AND fully passes its behavior-pinning acceptance. Then it evolves
		// toward lower fuel like the combinators, with the suite as its fitness anchor.
		for _, prim := range []struct {
			urn, wat, intent string
			suite            *evolution.AcceptanceSuite
		}{
			{execution.SysDictURN, execution.SysDictWAT, "associative map primitive over a private page", appgen.DictAcceptance(evolution.DefaultPayloadOffset)},
			{execution.SysListURN, execution.SysListWAT, "growable list primitive over a private page", appgen.ListAcceptance(evolution.DefaultPayloadOffset)},
			{execution.SysSetURN, execution.SysSetWAT, "membership set primitive over a private page", appgen.SetAcceptance(evolution.DefaultPayloadOffset)},
		} {
			_, ok, passed, total, reason := appgen.PromotePrimitive(ctx, prim.wat, prim.suite, evolution.DefaultPayloadOffset, evolution.DefaultStateWindow)
			if !ok {
				log.Printf("[SYS] %s REJECTED (not enrolled): %s", prim.urn, reason)
				continue
			}
			_ = seedCell(ledger, repo, sieve, prim.urn, prim.wat, manifest.SemanticManifest{
				FunctionalIntent: prim.intent, DomainTags: []string{"sys", "primitive"},
			})
			if err := evolution.SaveAcceptance(ledger, prim.urn, prim.suite); err == nil {
				registry.Add(prim.urn)
				log.Printf("[SYS] %s enrolled in evolution (gated %d/%d acceptance scenarios) — evolvable toward lower fuel", prim.urn, passed, total)
			}
		}
	}
	// HDM_HOTPATH seeds the crafted expensive+cacheable demo cell for proving the structural
	// optimizer: an irreducible per-input sum (plateaus on local optimization) that a cache
	// makes free on repeats (an amortized win). Enrolled + pre-flagged structural so a frame
	// on it is offered the data-structure toolkit. Drive it with /structural?urn=.
	if os.Getenv("HDM_HOTPATH") != "" {
		// The leaf hotpath dispatches to (the non-inlinable cost) — seeded so the resolver
		// resolves it during acceptance/gauntlet scoring. Not enrolled (not an evolution target).
		if err := seedCell(ledger, repo, sieve, appgen.HotleafURN, appgen.HotleafWAT, manifest.SemanticManifest{
			FunctionalIntent: "echoes its argument; dispatched by demo:hotpath", DomainTags: []string{"demo", "hotleaf"},
		}); err != nil {
			log.Printf("[HOTPATH] hotleaf seed failed: %v", err)
		}
		if err := seedCell(ledger, repo, sieve, appgen.HotpathURN, appgen.HotpathWAT, manifest.SemanticManifest{
			FunctionalIntent: "expensive per-input sum via K dispatches; result deterministic in the input (cacheable)",
			DomainTags:       []string{"demo", "hotpath"},
		}); err != nil {
			log.Printf("[HOTPATH] seed failed: %v", err)
		} else {
			_ = evolution.SaveAcceptance(ledger, appgen.HotpathURN, appgen.HotpathAcceptance(evolution.DefaultPayloadOffset))
			registry.Add(appgen.HotpathURN)
			log.Printf("[HOTPATH] %s seeded — drive with /structural?urn=%s", appgen.HotpathURN, appgen.HotpathURN)
		}
	}
	// HDM_QUEUE seeds the FIFO probe (demo:fifo) + publishes the sys:queue ABI as system
	// guidance — the human supplies the interface ("we need a FIFO queue"), the system builds
	// and integrates it. With the code-size term + the sys-dispatch refund, offloading the
	// inline FIFO to a minted sys:queue is a net win. Drive with /structural?urn=urn:hdm:demo:fifo.
	if os.Getenv("HDM_QUEUE") != "" {
		if err := seedCell(ledger, repo, sieve, appgen.FifoURN, appgen.FifoWAT, manifest.SemanticManifest{
			FunctionalIntent: "order-dependent FIFO fold; holds the buffer inline (offload candidate)",
			DomainTags:       []string{"demo", "fifo"},
		}); err != nil {
			log.Printf("[QUEUE] seed failed: %v", err)
		} else {
			_ = evolution.SaveAcceptance(ledger, appgen.FifoURN, appgen.FifoAcceptance(evolution.DefaultPayloadOffset))
			registry.Add(appgen.FifoURN)
			sg := evolution.LoadGuidance(ledger, evolution.SystemGuidanceKey)
			if _, isNew := sg.Add(appgen.SysQueueGuidance); isNew {
				_ = evolution.SaveGuidance(ledger, evolution.SystemGuidanceKey, sg)
			}
			log.Printf("[QUEUE] %s seeded + sys:queue guidance published — drive with /structural?urn=%s", appgen.FifoURN, appgen.FifoURN)
		}
	}
	gateway := integration.NewProductionGateway(hypervisor, routerURN)
	canvas := integration.NewCanvasUiEngine(hypervisor)
	canvasSrv := integration.NewCanvasServer(canvas, lifeURN) // default view: Game of Life
	if bootUICell != "" {
		canvasSrv.SetActive(bootUICell) // focus a boot-time HDM_APP's UI cell
	}
	const edgeAddr = "127.0.0.1:8420"
	// Frame-budget tracker: shared by the frame loop (writer), the /perf endpoint,
	// and the enforce-budget fixpoint pass. Created here so the Services closures
	// can read it.
	fps := resolveFPS()
	budget := newFrameBudget(fps)
	integration.Serve(ctx, edgeAddr, integration.Services{
		Gateway: gateway,
		Canvas:  canvasSrv,
		Build:   integration.NewBuildServer(grower, registry, canvasSrv),
		Input:   integration.NewInputServer(hypervisor),
		Cells:   registry.List,
		Status:  activity.Snapshot,
		Flow:    activity.Flow,
		Inspect: func(urn string) any { return inspectCell(ctx, ledger, orchestrator, urn) },
		Objective: func() string {
			ns := appNamespace(canvasSrv.Active())
			if ns == "" {
				return ""
			}
			if env := appgen.LoadEnvelope(ledger, ns); env != nil {
				return env.Objective
			}
			return ""
		},
		AppMap: func() string {
			ns := appNamespace(canvasSrv.Active())
			if ns == "" {
				return ""
			}
			if m := evolution.LoadAppMap(ledger, ns); m != nil {
				return m.Render()
			}
			return ""
		},
		Plan: func() string {
			ns := appNamespace(canvasSrv.Active())
			if ns == "" {
				return ""
			}
			// The panel shows the SYSTEM design (overview + choreography + the
			// component roster). Each component's detailed plan is scoped to that
			// cell in the inspector, so the panel is not one confusing blob.
			if p := evolution.LoadPlan(ledger, ns); p != nil {
				return p.RenderSystem()
			}
			return ""
		},
		Walk: func() string {
			ns := appNamespace(canvasSrv.Active())
			if ns == "" {
				return ""
			}
			if t := evolution.LoadGoalTree(ledger, ns); t != nil {
				// Reflect live gauntlet state (done / building) on a loaded copy — the
				// persisted tree stays structural; display is computed fresh.
				t.ApplyLiveStatus(func(cell string) (int, int, bool) {
					p, tot, err := orchestrator.ScoreCell(context.Background(), cell)
					return p, tot, err == nil
				})
				return t.Render()
			}
			return ""
		},
		Log: func() []string { return logBuf.Lines() },
		State: func() any {
			ns := appNamespace(canvasSrv.Active())
			if ns == "" {
				return []any{}
			}
			c := evolution.LoadContract(ledger, ns)
			if c == nil {
				return []any{}
			}
			type fieldVal struct {
				Name   string `json:"name"`
				Offset string `json:"offset"`
				Type   string `json:"type,omitempty"`
				Value  uint32 `json:"value"`
			}
			out := make([]fieldVal, 0, len(c.Fields))
			for _, f := range c.Fields {
				v, _ := hypervisor.PeekU32(uint32(f.Offset))
				out = append(out, fieldVal{
					Name: f.Name, Offset: fmt.Sprintf("0x%X", f.Offset), Type: f.Type, Value: v,
				})
			}
			return out
		},
		Perf: func() any {
			// Per-cell frame cost vs its share of the frame budget, so over-budget
			// (animation-stuttering) cells are visible.
			counts, cellNS := syncAppCells(registry, repo, hypervisor)
			type perfRow struct {
				URN      string  `json:"urn"`
				AvgMs    float64 `json:"avgMs"`
				BudgetMs float64 `json:"budgetMs"`
				Over     bool    `json:"over"`
			}
			rows := []perfRow{}
			for u, avg := range budget.tracked() {
				ns := cellNS[u]
				budMs := budget.perCellBudgetNS(counts[ns]) / 1e6
				rows = append(rows, perfRow{
					URN: u, AvgMs: avg / 1e6, BudgetMs: budMs, Over: avg > budget.perCellBudgetNS(counts[ns]),
				})
			}
			// Memoization effectiveness: with a static input a map/fold is all hits.
			hits, misses := hypervisor.MemoStats()
			rate := 0.0
			if tot := hits + misses; tot > 0 {
				rate = float64(hits) / float64(tot)
			}
			// Model routing: the per-type bindings + tier-weighted cognitive spend, so
			// the mixture-of-models is visible (which model serves each role, and where
			// the cost is going).
			bindings := map[string]string{}
			for _, b := range router.Bindings() {
				bindings[b.Type] = b.Model
			}
			byType := map[string]uint64{}
			for t, n := range router.TokensByType() {
				byType[string(t)] = n
			}
			models := map[string]any{
				"bindings":     bindings,
				"totalTokens":  router.TotalTokens(),
				"weightedCost": router.WeightedTokens(),
			}
			if len(byType) > 0 {
				models["tokensByType"] = byType
			}
			return map[string]any{
				"cells":  rows,
				"memo":   map[string]any{"hits": hits, "misses": misses, "hitRate": rate},
				"models": models,
			}
		},
		Vision: func(question string) (string, error) {
			// Refresh the frame ring with a few current frames of the active UI cell,
			// then ask the multimodal model.
			active := canvasSrv.Active()
			for i := 0; i < 3; i++ {
				_, _ = canvas.ExtractActiveFrame(active)
			}
			const visionSys = "You evaluate a rendered application frame. Look at the image(s) and answer the question concisely and honestly, based only on what is visible."
			return hypervisor.VisionJudge(context.Background(), visionSys, question, 3)
		},
		Mask: func(on bool) string {
			// Runtime A/B toggle: flip enforcement on the SAME app without a re-grow.
			dr := os.Getenv("HDM_MASK_READS") == "1"
			hypervisor.SetMasksEnabled(on)
			if on {
				refreshMasks(hypervisor, repo, ledger, registry, dr)
			}
			return fmt.Sprintf("masks enabled=%v (reads-denied=%v)", on, dr)
		},
		Feedback: func(commentary string) any {
			// Operator commentary → routed (soft) into system/app guidance. It is injected
			// into synthesis on each cell's next build; the adversarial feedback critic
			// enforces the same statements and re-opens cells that violate them.
			ns := appNamespace(canvasSrv.Active())
			routed, err := grower.RouteFeedback(context.Background(), ns, commentary)
			if err != nil {
				return map[string]string{"error": err.Error()}
			}
			log.Printf("[FEEDBACK] routed %d guidance item(s) for %s", len(routed), ns)
			return map[string]any{"routed": routed}
		},
		Guidance: func() any {
			out := map[string]any{"system": evolution.LoadGuidance(ledger, evolution.SystemGuidanceKey).Entries}
			if ns := appNamespace(canvasSrv.Active()); ns != "" {
				out["app"] = evolution.LoadGuidance(ledger, evolution.AppGuidanceKey(ns)).Entries
				out["appNamespace"] = ns
			}
			return out
		},
		OptimizePrompt: func(name, feedback string) any {
			// The system tuning its own prompts: LLM rewrite + adversarial validation.
			revised, adopted, reason, err := grower.OptimizePromptByName(context.Background(), name, feedback)
			if err != nil {
				return map[string]string{"error": err.Error()}
			}
			log.Printf("[PROMPT] optimize %q: adopted=%v — %s", name, adopted, reason)
			return map[string]any{"name": name, "adopted": adopted, "reason": reason, "revised": revised}
		},
		Prompts: func() any {
			return map[string]any{
				"refinable":  grower.RefinablePrompts(),
				"overridden": evolution.ListPromptOverrides(ledger),
			}
		},
		Verify: func() any {
			thr := evolution.LoadCheckThresholds(ledger)
			return map[string]any{
				"thresholds": thr,
				"gatePasses": evolution.MetaAcceptanceGate(thr),
				"note":       "visual grader thresholds are evolvable but every change is gated by a fixed meta-acceptance corpus — the grader can tighten, never be weakened by the app it grades",
			}
		},
		Promote: func(name string) any {
			// The system's own acceptance gate for cutting a version: benchmark, then epoch
			// iff improved. Long-running (grows apps) → run in the background; watch the log
			// ([BENCH]/[PROMOTE]) and /benchmark for the outcome.
			if evolutionPaused.Load() {
				return map[string]string{"error": "a benchmark is already running"}
			}
			go tryPromoteEpoch(context.Background(), grower, orchestrator, router, ledger, name)
			return map[string]any{"started": name, "note": "running system benchmark (evolves the app suite); watch the log and /benchmark for the verdict"}
		},
		Benchmark: func() any {
			var suite []string
			for _, a := range benchmarkSuite() {
				suite = append(suite, a.Name)
			}
			return map[string]any{"baseline": evolution.LoadBenchmarkBaseline(ledger), "running": evolutionPaused.Load(), "suite": suite}
		},
		Structural: func(urn string) any {
			// Direct driver for the structural optimizer: flag the cell, run one mutation
			// frame (its synthesis is offered the data-structure toolkit), and report whether
			// it committed a refactor and how cost changed. Bypasses friction selection so the
			// optimizer can be exercised on a chosen cell. Pause the heartbeat's evolution loop
			// for the duration so its concurrent commits can't shift the manifest root out from
			// under this frame's optimistic-concurrency commit (an MVCC conflict).
			evolutionPaused.Store(true)
			defer evolutionPaused.Store(false)
			orchestrator.SetStructural(urn, true)
			fr, err := orchestrator.RunFrame(context.Background(), urn)
			if err != nil {
				return map[string]string{"error": err.Error()}
			}
			out := map[string]any{"urn": urn, "committed": fr.Committed, "reason": fr.Reason, "attempted": fr.Attempted}
			if fr.Verdict != nil {
				out["baselineFuel"] = fr.Verdict.BaselineFuel
				out["candidateFuel"] = fr.Verdict.CandidateFuel
			}
			if fr.Sieve != nil {
				out["candidateWAT"] = fr.Sieve.WAT // the model's proposed refactor, for inspection
			}
			if fr.MintedPrimitive != "" {
				out["mintedPrimitive"] = fr.MintedPrimitive
			}
			if fr.Committed {
				log.Printf("[STRUCT] %s: %s", urn, fr.Reason)
			}
			return out
		},
		BenchmarkReset: func() any {
			if err := evolution.ResetBenchmarkBaseline(ledger); err != nil {
				return map[string]string{"error": err.Error()}
			}
			log.Printf("[BENCH] baseline reset — next promotion establishes a fresh bar")
			return map[string]any{"reset": true, "note": "baseline cleared; next /promote establishes a fresh baseline"}
		},
		Policy: func(feedback string) any {
			if strings.TrimSpace(feedback) == "" {
				return map[string]any{"policy": evolution.LoadPolicy(ledger), "overridden": evolution.PolicyOverridden(ledger), "objective": evolution.SystemObjective}
			}
			applied, changed, note, err := grower.OptimizePolicy(context.Background(), feedback)
			if err != nil {
				return map[string]string{"error": err.Error()}
			}
			applyPolicy(ledger) // take effect immediately
			log.Printf("[POLICY] tuned (changed=%v): %s", changed, note)
			return map[string]any{"policy": applied, "changed": changed, "note": note}
		},
		Epoch: func(action, name string) any {
			// Checkpoint / restore the evolvable SYSTEM baseline (sys cells + prompts +
			// guidance) so a better system can become a new starting point.
			switch action {
			case "save":
				ep, err := grower.SaveEpoch(name)
				if err != nil {
					return map[string]string{"error": err.Error()}
				}
				log.Printf("[EPOCH] saved %q (%d prompts, %d sys cells)", name, len(ep.Prompts), len(ep.SysCells))
				return map[string]any{"saved": name, "prompts": len(ep.Prompts), "sysCells": len(ep.SysCells), "guidance": ep.Guidance != ""}
			case "restore":
				msg, err := grower.RestoreEpoch(name)
				if err != nil {
					return map[string]string{"error": err.Error()}
				}
				log.Printf("[EPOCH] %s", msg)
				return map[string]string{"restored": name, "detail": msg}
			default:
				return map[string]any{"epochs": grower.ListEpochs()}
			}
		},
	})
	log.Printf("[EDGE] console http://%s/  (ingress /ingress · canvas /canvas · build /build · input /input)", edgeAddr)

	// Growth Pipeline: scaffold an application from a natural-language objective in
	// HDM_APP. Run it in the BACKGROUND (after the edge server is up) — growth
	// makes many model calls, and blocking boot on them would leave the console
	// and every tick frozen until it finished. Subsystems are enrolled as each
	// comes online; the evolutionary loop co-evolves them all in parallel, and the
	// canvas auto-focuses the app's UI cell once it exists.
	if obj := os.Getenv("HDM_APP"); obj != "" {
		go func() {
			hypervisor.BeginGrowth() // bootstrap optimizer yields the model to growth
			defer hypervisor.EndGrowth()
			enroll := func(urn string) {
				registry.Add(urn)
				activity.SetCell(urn, status.CellNew)
			}
			// Retry until it lands: GrowConcurrent is idempotent (CompileEnvelope
			// refines an existing envelope; scaffolding skips already-live cells),
			// so if the model server is down at boot we resume scaffolding once it
			// returns rather than permanently failing to grow the app.
			for {
				env, created, ui, err := grower.GrowConcurrent(ctx, obj, enroll)
				if err == nil {
					log.Printf("[APP] %s: scaffolded %d subsystems (co-evolving)", env.ApplicationNamespace, len(created))
					if ui != "" {
						canvasSrv.SetActive(ui)
					}
					return
				}
				log.Printf("[APP] growth attempt failed (retrying in 30s): %v", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(30 * time.Second):
				}
			}
		}()
	}

	if req := os.Getenv("HDM_TARGET"); req != "" {
		if tgt, err := axiomCompiler.Compile(ctx, req); err != nil {
			log.Printf("[AXIOM] compile failed: %v", err)
		} else {
			log.Printf("[AXIOM] target %s: %q saliency=%.2f tags=%v", tgt.ID, tgt.Intent, tgt.Saliency, tgt.DomainTags)
			if err := axiomCompiler.ApplyTo(repo, registry.List()); err != nil {
				log.Printf("[AXIOM] apply failed: %v", err)
			}
		}
	}

	// 6. Scheduling Loop — production heartbeat spaced longer than a model
	//    round-trip; an out-of-band evolutionary mutation frame fires every
	//    evolutionCadence ticks.
	heartbeat := resolveHeartbeat()
	log.Printf("HDM Gen 0 online — evolution heartbeat %s (HDM_HEARTBEAT) · app frame loop %d FPS (HDM_FPS)", heartbeat, fps)
	// App frame loop: advance the focused app's live simulation at display rate,
	// decoupled from the model-paced evolution heartbeat below. A companion async
	// worker ticks cognitive-engine cells off the frame thread so a model call never
	// stalls a frame.
	// Deny-by-default read/write masks: a cell may only touch shared-state fields it
	// EXPLICITLY declared, enforced in execTrampoline on every live path. A clean
	// same-app A/B (toggling masks on a fixed, converged Mandelbrot via /mask) confirmed
	// full read+write enforcement keeps the compute correct — the flips-to-0 seen in
	// fresh grows were grow variance + evolution churn during BUILDING, not the masks;
	// acceptance/scoring runs unmasked in a shadow sandbox, so evolution converges
	// regardless of transient live-state masking. HDM_MASK=0 disables all enforcement;
	// HDM_MASK_READS=0 keeps writes enforced but leaves reads open.
	denyReads := os.Getenv("HDM_MASK_READS") != "0"
	hypervisor.SetMasksEnabled(os.Getenv("HDM_MASK") != "0")
	refreshMasks(hypervisor, repo, ledger, registry, denyReads)
	applyPolicy(ledger) // tunable judgement calls (retries, critic cadences) from the policy
	// Seed the ONE goal as system guidance so every cell build — and thus every evolvable
	// substrate — pulls toward correct + efficient, not a local proxy.
	sysGuide := evolution.LoadGuidance(ledger, evolution.SystemGuidanceKey)
	if _, isNew := sysGuide.Add(evolution.SystemObjective); isNew {
		_ = evolution.SaveGuidance(ledger, evolution.SystemGuidanceKey, sysGuide)
	}
	// Trusted-evolvable verification: tighten the visual grader to the strictest thresholds
	// the FIXED meta-acceptance corpus allows — the check evolves stricter but can never be
	// weakened past the trust anchor (SaveCheckThresholds gates every change).
	if thr, changed := evolution.TightenCheck(ledger); changed {
		log.Printf("[VERIFY] visual grader tightened to MinColors=%d MinBrightSpread=%d (meta-acceptance gated)", thr.MinColors, thr.MinBrightSpread)
	}
	go runFrameLoop(ctx, hypervisor, repo, registry, canvasSrv, ledger, budget, fps)
	go runAsyncLoop(ctx, hypervisor, repo, registry, canvasSrv)
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()

	tick := 0
	gated := false   // cognitive-engine gate state (log on change)
	idleWait := 0    // fixpoint frames waited since the last re-evaluation
	idleBackoff := 1 // fixpoint frames between re-evaluations (exponential)
	// Per-cell count of frame-budget re-opens, so a cell that can't get leaner is
	// optimized a bounded number of times rather than churning forever.
	budgetReopens := map[string]int{}
	structuralPlateau := map[string]int{} // consecutive failed local optimizations per cell
	orphanReauthors := map[string]int{}   // acceptance re-author attempts per 0-suite cell
	// Per-cell fingerprint of the last boundary-decision inputs evaluated, so the boundary
	// evolver doesn't re-ask the model about an unchanged stalled cell (bounds token cost).
	boundaryEvaluated := map[string]string{}
	var policyTune policyTuneState // metrics-driven policy self-tuning cooldown + rollback state
	var structDetect time.Time     // recurring-pattern (shared-primitive) detector cooldown
	// Sys visual critic state: the last genotype hash critiqued per focused cell (so
	// it re-critiques when the renderer changes) and the last critique time (throttle).
	visualCritiqued := map[string]string{}
	var lastVisualCritique time.Time
	codeCritiqued := ""
	var lastCodeCritique time.Time
	if v := os.Getenv("HDM_VISION_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			visionCritiqueInterval = d
		}
	}
	// Persistent, root-keyed convergence: load each known cell's friction from
	// the ledger (resume across restarts) and persist updates as they change.
	friction := map[string]evolution.FrictionState{}
	for _, u := range registry.List() {
		friction[u] = evolution.LoadFriction(ledger, u)
	}
	persistFriction := func(urn string, fs evolution.FrictionState) {
		friction[urn] = fs
		if err := evolution.SaveFriction(ledger, urn, fs); err != nil {
			log.Printf("[WARN] persist friction %s: %v", urn, err)
		}
	}
	for {
		select {
		case <-ctx.Done():
			log.Printf("shutdown signal received; locking ledger states")
			return
		case <-ticker.C:
			tick++
			if evolutionPaused.Load() {
				continue // a system benchmark is running — don't race the orchestrator
			}
			// NOTE: the guest time base chronos.tick() is advanced by the APP FRAME
			// LOOP (30 FPS), not here — the frame is the app's unit of time, so a
			// tick()-driven cell animates at frame rate. This heartbeat drives only
			// evolution/bootstrap cadence (its own local `tick`).
			nextFrame := ((tick / evolutionCadence) + 1) * evolutionCadence
			activity.Ticks(tick, nextFrame)

			// Production heartbeat: resolve the active node descriptor, load its
			// phenotype, and execute it on the live manifest root.
			desc, err := repo.Load(urn)
			if err != nil {
				log.Printf("[WARN] descriptor resolution skipped tick: %v", err)
				continue
			}
			bytecode, err := repo.Phenotype(desc)
			if err != nil {
				log.Printf("[ERROR] phenotype resolution: %v", err)
				continue
			}
			if err := hypervisor.LoadCell(urn, bytecode); err != nil {
				log.Printf("[ERROR] cell load: %v", err)
				continue
			}
			// Bootstrap yield: the Gen-0 optimizer is the lowest-priority model
			// consumer. While the application is still being built — growth in
			// flight, or any app cell not yet settled — gate its reasoning so the
			// single model serves growth/evolution instead. When the app settles,
			// the fixpoint logic below governs the gate (idle-time optimization).
			if !hypervisor.GrowthActive() {
				r := orchestrator.ManifestRoot()
				for _, u := range registry.List() {
					if appNamespace(u) != "" && !friction[u].Settled(r) {
						hypervisor.SetReasoningGate(true)
						break
					}
				}
			}
			activity.Phase("ticking", "executing bootstrap cell "+urn+" (may call cognitive engine)", urn)
			t0 := time.Now()
			res, fuel, tokens, err := hypervisor.ExecuteTrampoline("urn:hdm:sys:host", urn, "run-tick", 0, 0)
			wallNS := uint64(time.Since(t0).Nanoseconds())
			if err != nil {
				log.Printf("[ERROR] trampoline: %v", err)
				continue
			}
			// The cell's own cost excludes time spent blocked in the cognitive-
			// engine host call (external I/O wait, not the cell's computation), so
			// H reflects the cell — matching what the optimizer actually grades —
			// instead of the ~10s model round-trip.
			guestNS := wallNS
			if hostNS := hypervisor.LastReasoningNanos(); hostNS < guestNS {
				guestNS -= hostNS
			}
			h := telemetry.CalculateHamiltonian(telemetry.SystemMetrics{
				WasmFuel: fuel, LatencyNS: guestNS, TokenMilliCents: uint32(tokens), SaliencyScore: desc.Saliency,
			})
			dash.Tick(tick, shortHash(desc.PhenotypeHash), res, fuel, tokens, float64(guestNS)/1e6, h)
			activity.Flow("tick", shortHash(desc.PhenotypeHash),
				fmt.Sprintf("tick %d → %d · fuel %d · %d tok · H %.4f", tick, res, fuel, tokens, h))

			// Sys VISUAL CRITIC: every heartbeat, give the focused app's renderer a
			// (throttled, re-fires on genotype change) look — regardless of build
			// state, so vision provides feedback even on a still-building app that
			// never reaches a fixpoint. Runs in this goroutine, so its ledger/friction
			// writes stay serialized with the rest of the loop. A re-open resets idle.
			if reopened := visualCritic(ctx, grower, repo, hypervisor, canvas, canvasSrv, orchestrator.ManifestRoot(), persistFriction, activity, ledger, visualCritiqued, &lastVisualCritique); reopened > 0 {
				idleWait, idleBackoff = 0, 1
				hypervisor.SetReasoningGate(false)
			}
			// The general (non-visual) adversarial arm: judge the focused app's code
			// against the operator's criteria and re-open any cell that violates one.
			if reopened := codeCritic(ctx, grower, orchestrator, repo, canvasSrv, orchestrator.ManifestRoot(), persistFriction, activity, ledger, &codeCritiqued, &lastCodeCritique); reopened > 0 {
				idleWait, idleBackoff = 0, 1
				hypervisor.SetReasoningGate(false)
			}

			// Out-of-band evolutionary mutation frame, targeting the highest-
			// friction cell among those not yet settled at the current root. Only a
			// global fixpoint (every cell converged at the same root) idles the loop.
			if tick%evolutionCadence == 0 {
				root := orchestrator.ManifestRoot()
				// Depth-first walk: order the candidate frontier by each app's goal
				// tree so a fractured parent's children finish before the next sibling
				// subsystem, turning the breadth-first sweep into a focused DFS.
				candidates := walkOrder(applicationFirst(activeCandidates(registry.List(), friction, root)), ledger)
				target := chooseTarget(ctx, orchestrator, candidates)
				if target == "" {
					// Global fixpoint. Periodically re-run the steady-state passes —
					// retry stalled cells / re-test, extend complete cells' specs
					// (curriculum), challenge architecture, consolidate — because the
					// model is stochastic: a later attempt may propose work an earlier
					// one didn't. Re-attempts back off exponentially so a genuinely
					// idle system doesn't hammer the model, but they NEVER stop, so the
					// system keeps trying to improve rather than dying at the first
					// fixpoint. The bootstrap engine stays gated between attempts
					// (its per-tick reasoning is the real token sink); the passes call
					// the model directly.
					idleWait++
					if idleWait < idleBackoff {
						hypervisor.SetReasoningGate(true)
						activity.Phase("converged", fmt.Sprintf("idle — next re-evaluation in %d frame(s)", idleBackoff-idleWait), "")
					} else {
						idleWait = 0
						// Re-derive the application maps from reality BEFORE the
						// steady-state passes, so the curriculum, the architecture
						// critic, and fracture all reason about the system as it now
						// actually stands (verified behavior + data-flow gaps), and
						// keep each app's DESIGN PLAN in sync with the architecture
						// (fracture children / new subsystems get designed to fit).
						refreshAppMaps(ctx, grower, registry)
						refreshPlans(ctx, grower, registry)
						// Re-register each cell's deny-by-default mask against the freshly
						// derived ports, so enforcement tracks the current architecture.
						refreshMasks(hypervisor, repo, ledger, registry, denyReads)
						applyPolicy(ledger)                                // pick up any policy tune since last cycle
						applyModelPolicy(ledger, router, hypervisor)       // evolvable model cost tiers + bindings
						// Self-tune policy from real outcomes toward the objective (throttled,
						// rolls back a tune that hurt correctness).
						autoTunePolicy(ctx, grower, ledger, systemMetrics(ctx, registry, orchestrator, friction, budget, root, router), &policyTune)
						// Recurring-pattern detector: propose a shared data-structure primitive
						// for cells that repeat the same shape, autonomously (no operator).
						autoDetectStructure(ctx, grower, orchestrator, registry, repo, ledger, &structDetect)
						// The system refining its own prompts: any prompt whose outputs have
						// repeatedly been invalid gets auto-refined (adversarially validated).
						grower.AutoOptimizePrompts(ctx)
						progressed := ensureContracts(ctx, grower, registry) > 0 ||
							recoverOrphanedCells(ctx, grower, orchestrator, registry, repo, ledger, hypervisor, orphanReauthors, activity) > 0 ||
							retryStalled(ctx, orchestrator, registry.List(), friction, root, persistFriction, activity) > 0 ||
							evolveBoundaries(ctx, grower, orchestrator, registry, friction, root, persistFriction, activity, boundaryEvaluated) > 0 ||
							fractureStalled(ctx, grower, orchestrator, registry, friction, root, activity, ledger) > 0 ||
							expandCompleteSuites(ctx, grower, orchestrator, registry, root, persistFriction, activity) > 0 ||
							challengeArchitectures(ctx, grower, orchestrator, registry, activity, ledger) > 0 ||
							judgeMotions(ctx, grower, orchestrator, registry, root, persistFriction, activity, ledger) > 0 ||
							enforceFrameBudget(ctx, orchestrator, registry, repo, hypervisor, budget, root, persistFriction, activity, budgetReopens) > 0 ||
							consolidateComponents(ctx, orchestrator, registry, activity) > 0
						if progressed {
							idleBackoff = 1 // work found — re-evaluate eagerly again
							hypervisor.SetReasoningGate(false)
							gated = false
							log.Printf("[EVOLVE] fixpoint re-evaluation produced work — resuming")
						} else {
							if idleBackoff < maxIdleBackoff {
								idleBackoff *= 2 // nothing new — wait longer before the next try
							}
							hypervisor.SetReasoningGate(true)
							if !gated {
								gated = true
								log.Printf("[EVOLVE] global fixpoint — idling; cognitive engine gated (re-evaluating every %d frames)", idleBackoff)
							}
							activity.Phase("converged", "optimized, complete & consolidated — idle", "")
						}
					}
				} else {
					idleWait, idleBackoff = 0, 1 // active work: reset the idle backoff
					hypervisor.SetReasoningGate(false)
					if gated {
						log.Printf("[EVOLVE] work available — cognitive engine ungated")
						gated = false
					}
					activity.Phase("evolving", "mutation frame on "+target, target)
					fr := runEvolutionFrame(ctx, orchestrator, target, candidates, activity)
					// Convergence is recorded here; complete cells get their specs
					// extended (curriculum) at the fixpoint by expandCompleteSuites.
					noteConvergence(fr, friction, root, persistFriction, activity)
					// Cost-driven structural escalation: a complete cell whose LOCAL
					// optimization has plateaued while still expensive is offered the
					// data-structure toolkit on its next synthesis.
					noteStructuralEscalation(orchestrator, target, fr, structuralPlateau)
					// A structural refactor that MINTED a new primitive and committed has
					// witnessed that primitive correct (the consumer reproduced its tapes
					// using it) — enroll it so it persists and can be reused/evolved.
					if fr != nil && fr.Committed && fr.MintedPrimitive != "" {
						registry.Add(fr.MintedPrimitive)
						log.Printf("[STRUCT] minted primitive %s — witnessed correct by %s; enrolled for reuse", fr.MintedPrimitive, target)
					}
					// A cell that just changed behavior changes the SYSTEM: fold its
					// new verified reads/writes back into the application map, so the
					// next cell is synthesized against reality (this is the "merge
					// components in as they are built" step).
					if fr != nil && fr.Committed {
						if ns := appNamespace(target); ns != "" {
							if err := grower.RefreshAppMap(ctx, ns); err != nil {
								log.Printf("[MAP] refresh %s failed: %v", ns, err)
							}
						}
					}
				}
			}

			// Tape Compaction Janitor sweep, then CAS garbage collection
			// of blocks orphaned by pruning.
			if tick%janitorCadence == 0 {
				runJanitor(ctx, janitor, repo, registry.List())
				if scanned, deleted, err := gc.Collect(ledger); err != nil {
					log.Printf("[GC] error: %v", err)
				} else if deleted > 0 {
					log.Printf("[GC] swept %d of %d blocks", deleted, scanned)
				}
				// Pull the current Canvas UI vector frame out-of-band.
				if frame, err := canvas.ExtractActiveFrame(uiURN); err == nil {
					log.Printf("[CANVAS] %s: %d-byte vector frame", uiURN, len(frame))
				}
			}

			// Settle back to idle with a countdown to the next mutation frame, so
			// the console shows why nothing is happening between frames.
			activity.Phase("idle", fmt.Sprintf("next evolution frame in %d tick(s)", nextFrame-tick), "")
		}
	}
}

// runEvolutionFrame selects the highest-friction cell among the candidates and
// executes one mutation frame against it, reporting the outcome. It never aborts
// the scheduler: expected conditions (cognitive engine offline, rejected
// candidate) are logged and the loop continues.
// inspectCell assembles the /cell payload for the console's cell inspector: the
// cell's acceptance tests + behavioral scenarios, the app's shared-state contract
// (the constraints it must coordinate through), its live pass/total, and its
// convergence state (stalled/converged). Read-only; safe to call per request.
func inspectCell(ctx context.Context, ledger *storage.LedgerEngine, orch *evolution.Orchestrator, urn string) any {
	type scenView struct {
		Name   string `json:"name"`
		Entry  string `json:"entry,omitempty"`
		Seed   string `json:"seed,omitempty"`
		Expect string `json:"expect,omitempty"`
		Pass   bool   `json:"pass"`
	}
	out := map[string]any{"urn": urn}
	if k := appgen.CellKindOf(ledger, urn); k != "" {
		out["kind"] = k // the cell's declared type (compute|render|input|leaf|compose)
	}

	suite, _ := evolution.LoadAcceptance(ledger, urn)
	if suite != nil {
		if len(suite.Tests) > 0 {
			out["tests"] = suite.Tests
		}
		// Per-scenario pass/fail: shows WHICH coordination check fails, not just the
		// aggregate score — the observability we needed to diagnose bad checks.
		pass := map[string]bool{}
		if flags, err := orch.ScenarioFlags(ctx, urn); err == nil {
			for _, f := range flags {
				pass[f.Name] = f.Pass
			}
		}
		scs := make([]scenView, 0, len(suite.Scenarios))
		for _, s := range suite.Scenarios {
			entry := s.Entry
			if entry == "" {
				entry = "run-tick"
			}
			expect := ""
			if b, err := json.Marshal(s.Expect); err == nil {
				expect = string(b)
			}
			seed := ""
			if len(s.Seed) > 0 {
				if b, err := json.Marshal(s.Seed); err == nil {
					seed = string(b)
				}
			}
			scs = append(scs, scenView{Name: s.Name, Entry: entry, Seed: seed, Expect: expect, Pass: pass[s.Name]})
		}
		if len(scs) > 0 {
			out["scenarios"] = scs
		}
	}

	if c := evolution.LoadContract(ledger, evolution.AppNamespaceOf(urn)); c != nil {
		fields := make([]map[string]any, 0, len(c.Fields))
		for _, f := range c.Fields {
			fields = append(fields, map[string]any{
				"name": f.Name, "type": f.Type, "desc": f.Desc,
				"offset": fmt.Sprintf("0x%X", f.Offset),
			})
		}
		out["contract"] = map[string]any{"fields": fields}
	}

	// This cell's OWN design plan (not the whole app's), so the inspector shows
	// exactly what THIS component is meant to implement.
	if plan := evolution.LoadPlan(ledger, evolution.AppNamespaceOf(urn)); plan != nil {
		if cp := plan.Component(urn); cp != nil {
			out["plan"] = map[string]any{
				"purpose": cp.Purpose, "reads": cp.Reads, "writes": cp.Writes,
				"steps": cp.Steps, "interactions": cp.Interactions,
				"invariants": cp.Invariants, "notes": cp.Notes,
			}
		}
	}

	passed, total, scoreErr := orch.ScoreCell(ctx, urn)
	if scoreErr == nil {
		out["passed"], out["total"] = passed, total
	}

	// State is grounded in the LIVE score (see fractureStalled): a cell failing
	// its checks is "stalled" if parked (out of active building) or "building" if
	// still active, regardless of a possibly-stale Incomplete flag.
	fs := evolution.LoadFriction(ledger, urn)
	incomplete := scoreErr == nil && total > 0 && passed < total
	switch {
	case incomplete && fs.Converged:
		out["state"] = "stalled"
	case incomplete:
		out["state"] = "building"
	case fs.Converged:
		out["state"] = "converged"
	default:
		out["state"] = "active"
	}
	return out
}

func runEvolutionFrame(ctx context.Context, orchestrator *evolution.Orchestrator, target string, peers []string, activity *status.Broker) *evolution.FrameResult {
	// Route through the topology planner: it may choose fission, fusion, or
	// ordinary genotype refinement based on the target and its peers.
	fr, err := orchestrator.RunTopologyFrame(ctx, target, peers)
	if err != nil {
		log.Printf("[EVOLVE] frame error on %s: %v", target, err)
		activity.Phase("waiting", "frame error on "+target+": "+err.Error(), target)
		return nil
	}
	// fuelDelta renders the baseline->candidate fuel move, so a "held" line makes
	// clear whether the candidate was actually leaner (or identical => converged).
	fuelDelta := ""
	if fr.Verdict != nil {
		fuelDelta = fmt.Sprintf(", fuel %d->%d", fr.Verdict.BaselineFuel, fr.Verdict.CandidateFuel)
	}
	switch {
	case fr.Committed:
		lat := ""
		if fr.Verdict != nil {
			lat = fmt.Sprintf(", lat_p99 %dns->%dns", fr.Verdict.BaselineLatencyP99NS, fr.Verdict.CandidateLatencyP99NS)
		}
		log.Printf("[EVOLVE:%s] hot-swap committed for %s — %s (corpus %d/%d%s%s); root=%s…",
			fr.Kind, fr.TargetURN, fr.Reason, fr.CorpusKept, fr.CorpusConsidered, fuelDelta, lat, shortHash(fr.NewRoot))
		// The commit event was already emitted by the orchestrator.
	case fr.Attempted:
		// A candidate that PRESERVED behavior but didn't lower energy is a HOLD (a valid,
		// equivalent cell — just not an improvement), not a rejection. Only a behavioral or
		// acceptance regression is a true reject. Surfacing the difference stops an equivalent
		// candidate from reading as a failure.
		if fr.Verdict != nil && fr.Verdict.OutputMatch {
			log.Printf("[EVOLVE:%s] candidate held for %s (equivalent, no energy gain) — %s (corpus %d/%d%s)",
				fr.Kind, fr.TargetURN, fr.Reason, fr.CorpusKept, fr.CorpusConsidered, fuelDelta)
			activity.Event("hold", fr.TargetURN, fr.Reason+fuelDelta)
		} else {
			log.Printf("[EVOLVE:%s] candidate rejected for %s — %s (corpus %d/%d%s)",
				fr.Kind, fr.TargetURN, fr.Reason, fr.CorpusKept, fr.CorpusConsidered, fuelDelta)
			activity.Event("reject", fr.TargetURN, fr.Reason+fuelDelta)
		}
	default:
		log.Printf("[EVOLVE:%s] held for %s — %s", fr.Kind, fr.TargetURN, fr.Reason)
		activity.Event("hold", fr.TargetURN, fr.Reason)
	}
	return fr
}

// maybeExpandSuite is the curriculum step: a cell that converged only because it
// already passes ALL its acceptance checks gets its spec extended — new
// requirement-faithful checks are proposed and adversarially validated. If any
// survive, the cell is un-converged so the loop resumes building toward them.
// Cells that converged while still failing checks (the model is stuck) or that
// have no suite are left retired.
// expandCompleteSuites is the fixpoint curriculum step: for each grown
// application cell that is COMPLETE (passes its spec, including cells left with
// no spec), propose harder / fresh requirement-faithful checks via ExpandSuite.
// This is how a render-frame renderer — which never gets a normal mutation frame
// (not run-tick-probeable) — is handed harder draw-stream scenarios to build
// toward, and how a cell whose spec was emptied by re-evaluation is backfilled
// so it never becomes a purposeless no-op. Any expansion re-opens the cell
// (incomplete) to build against the new checks. Returns the number expanded.
// fullyGreenApps returns the set of application namespaces whose every cell passes
// its full acceptance suite. Such an app is WORKING and must be left working —
// the curriculum and architecture challenge are frozen for it until a new
// constraint (an edited objective) re-opens it. Without this, spec expansion piles
// harder checks onto a passing app, it regresses, and fractures — churning a
// finished app instead of leaving it alone.
func fullyGreenApps(ctx context.Context, orch *evolution.Orchestrator, registry *evolution.CellRegistry) map[string]bool {
	apps := map[string][]string{}
	for _, u := range registry.List() {
		if ns := appNamespace(u); ns != "" {
			apps[ns] = append(apps[ns], u)
		}
	}
	green := map[string]bool{}
	for ns, cells := range apps {
		g := true
		for _, u := range cells {
			p, t, err := orch.ScoreCell(ctx, u)
			if err != nil || t == 0 || p < t {
				g = false
				break
			}
		}
		green[ns] = g
	}
	return green
}

func expandCompleteSuites(ctx context.Context, grower *appgen.Grower, orch *evolution.Orchestrator, registry *evolution.CellRegistry, root string, persist func(string, evolution.FrictionState), activity *status.Broker) int {
	green := fullyGreenApps(ctx, orch, registry)
	n := 0
	for _, u := range registry.List() {
		ns := appNamespace(u)
		if ns == "" {
			continue // only grown app cells carry an evolving spec
		}
		if green[ns] {
			continue // FREEZE: a fully-green app stays working; no curriculum churn
		}
		passed, total, err := orch.ScoreCell(ctx, u)
		if err != nil || (total > 0 && passed < total) {
			continue // still building — leave it
		}
		added, xerr := grower.ExpandSuite(ctx, u)
		if xerr != nil {
			log.Printf("[EVOLVE] suite expansion for %s failed: %v", u, xerr)
			continue
		}
		if added > 0 {
			persist(u, evolution.FrictionState{Root: root}) // re-open to build the new checks
			activity.Event("create", u, fmt.Sprintf("spec extended +%d checks — new behavior to build", added))
			log.Printf("[EVOLVE] %s spec extended (+%d faithful checks) — re-opened", u, added)
			n++
		}
	}
	return n
}

// runJanitor prunes each cell's stored regression tape repository, logging how
// much was compacted away.
func runJanitor(ctx context.Context, janitor *evolution.Janitor, repo *manifest.Repository, urns []string) {
	for _, u := range urns {
		desc, err := repo.Load(u)
		if err != nil {
			continue
		}
		phenotype, err := repo.Phenotype(desc)
		if err != nil {
			continue
		}
		before, after, err := janitor.Compact(ctx, u, phenotype)
		if err != nil {
			log.Printf("[JANITOR] %s: %v", u, err)
			continue
		}
		if before > 0 {
			log.Printf("[JANITOR] %s: pruned %d -> %d tapes", u, before, after)
		}
	}
}

// needsSeed reports whether a cell URN must be (re)seeded: either it has no
// reference, or its reference resolves to something that is not a valid node
// descriptor (e.g. a database written by a pre-descriptor schema, where the ref
// pointed straight at phenotype bytecode). Self-heals incompatible ledgers.
func needsSeed(ledger *storage.LedgerEngine, repo *manifest.Repository, urn string) bool {
	if _, err := ledger.GetRef(urn); err != nil {
		return true
	}
	_, err := repo.Load(urn)
	return err != nil
}

// seedCell compiles a cell's WAT and points its URN at a fresh node descriptor,
// but only if it is not already live and valid (idempotent, self-healing).
func seedCell(ledger *storage.LedgerEngine, repo *manifest.Repository, sieve *compiler.CompilerService, urn, wat string, sem manifest.SemanticManifest) error {
	if !needsSeed(ledger, repo, urn) {
		return nil // already live and valid
	}
	art, err := sieve.CompileGenotype(wat)
	if err != nil || !art.SyntaxPassed {
		return fmt.Errorf("compile %s: %v", urn, err)
	}
	descHash, _, err := repo.PutCell(urn, wat, art.Bytecode, sem, 0)
	if err != nil {
		return err
	}
	return repo.SeedRef(urn, descHash)
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
