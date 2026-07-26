package flux

// SCOREBOARD is the cheap micro-tier of language-evolution exploration
// (docs/language-evolution.md, docs/lineage.md): before paying for a whole-stack
// epoch, A/B candidate language variants on a fixed benchmark of canonical authoring
// tasks and score them on how well the MODEL generates each — the LLM-optimal
// axes of docs/self-hosting-flux.md §0.1: syntax-valid rate, token cost, and
// canonicality (does the model converge on one program, or scatter). Correctness of
// a specific cell is the epoch gate's job; here we measure the language's
// generability. Most candidates die at this tier; only a winner goes to the gate.

import (
	"sort"
	"strings"
)

// Fitness weights. Valid-by-construction is the floor (GBNF pins it near 1), then we
// reward canonicality and penalize tokens. Explicit so the tradeoff is legible and
// tunable — this is the objective the language is being optimized against.
const (
	fitnessCanonicalityWeight = 0.5
	fitnessTokenWeight        = 0.002 // per completion token (≈0.2 for a 100-token cell)
)

// Task is one canonical authoring benchmark: an objective the model must express as
// a cell of a given kind over a given layout.
type Task struct {
	Name   string
	Prompt string
	Kind   CellKind
	Layout Layout
}

// Variant is a candidate language: it produces the grammar that constrains
// generation for a task and validates a generated program. For a grammar-only change
// Valid is nil and the shared Flux checker is used; a variant that alters the
// surface or semantics supplies its own validator.
type Variant struct {
	Name    string
	Grammar func(Layout, CellKind) string
	Valid   func(src string, layout Layout) bool
}

// Generator produces one completion for a prompt under a grammar constraint,
// returning the program text and its completion-token count. Injected so the
// scoreboard is testable without a live model.
type Generator interface {
	Generate(prompt, grammar string) (src string, tokens int, err error)
}

// Score is a variant's measured fitness on the benchmark.
type Score struct {
	Variant      string
	Samples      int     // total generations attempted
	ValidRate    float64 // fraction that parse + type-check
	MeanTokens   float64 // completion tokens, mean over VALID samples (cost of a usable cell)
	Canonicality float64 // per-task fraction of valid samples equal to the modal program, averaged
}

// Fitness collapses the axes into one comparable number (higher is better): the
// valid rate is the floor, canonicality is rewarded, tokens penalized.
func (s Score) Fitness() float64 {
	return s.ValidRate + fitnessCanonicalityWeight*s.Canonicality - fitnessTokenWeight*s.MeanTokens
}

// RunScoreboard generates each task under each variant `samples` times and returns
// the variants ranked by Fitness (best first). A variant's grammar constrains
// generation; its validator decides which completions count. Generation errors are
// counted as (invalid) samples so a flaky variant is penalized, not silently
// dropped.
func RunScoreboard(gen Generator, tasks []Task, variants []Variant, samples int) []Score {
	if samples < 1 {
		samples = 1
	}
	board := make([]Score, 0, len(variants))
	for _, v := range variants {
		validFn := v.Valid
		if validFn == nil {
			validFn = defaultValid
		}
		var totalValid, totalSamples, tokenN int
		var tokenSum, canonAcc float64
		var canonTasks int
		for _, task := range tasks {
			g := v.Grammar(task.Layout, task.Kind)
			var validSrcs []string
			for i := 0; i < samples; i++ {
				totalSamples++
				src, toks, err := gen.Generate(task.Prompt, g)
				if err != nil || !validFn(src, task.Layout) {
					continue
				}
				totalValid++
				validSrcs = append(validSrcs, src)
				tokenSum += float64(toks)
				tokenN++
			}
			if len(validSrcs) > 0 {
				canonAcc += modalFraction(validSrcs)
				canonTasks++
			}
		}
		s := Score{Variant: v.Name, Samples: totalSamples}
		if totalSamples > 0 {
			s.ValidRate = float64(totalValid) / float64(totalSamples)
		}
		if tokenN > 0 {
			s.MeanTokens = tokenSum / float64(tokenN)
		}
		if canonTasks > 0 {
			s.Canonicality = canonAcc / float64(canonTasks)
		}
		board = append(board, s)
	}
	sort.SliceStable(board, func(i, j int) bool { return board[i].Fitness() > board[j].Fitness() })
	return board
}

// defaultValid is the shared Flux acceptance for a generated program: it parses and
// type-checks against the task's layout. (Behavioral correctness is the epoch gate's
// job; here validity means "a well-formed, well-typed cell in the language.")
func defaultValid(src string, layout Layout) bool {
	f, err := Parse("m", src)
	if err != nil {
		return false
	}
	_, err = Check(f, layout)
	return err == nil
}

// modalFraction is the canonicality of a set of generations: the fraction that are
// the single most common program (whitespace-normalized). 1.0 means the model always
// wrote the same program; 1/N means every sample differed. A more canonical language
// concentrates the model's probability mass and so generates more reliably — an axis
// a human language designer would never optimize, but the right one here.
func modalFraction(srcs []string) float64 {
	if len(srcs) == 0 {
		return 0
	}
	counts := map[string]int{}
	best := 0
	for _, s := range srcs {
		k := strings.Join(strings.Fields(s), " ")
		counts[k]++
		if counts[k] > best {
			best = counts[k]
		}
	}
	return float64(best) / float64(len(srcs))
}

// DefaultBenchmark is the canonical set of authoring tasks the scoreboard measures a
// language on — the same physics / input / view shapes the whole system is grown
// around, over a representative ball layout. Reused by the live measurement and any
// exploration tool so variants are always compared on the same ground.
func DefaultBenchmark() []Task {
	layout := Layout{
		"ball_x": {Type: TInt, Offset: 0xB0000}, "ball_y": {Type: TInt, Offset: 0xB0004},
		"vel_x": {Type: TInt, Offset: 0xB0008}, "vel_y": {Type: TInt, Offset: 0xB000C},
		"screen_w": {Type: TInt, Offset: 0xB0010}, "screen_h": {Type: TInt, Offset: 0xB0014},
	}
	return []Task{
		{Name: "physics", Kind: KindCompute, Layout: layout,
			Prompt: "Move the ball: add each velocity to each position, and reflect the velocity at the walls."},
		{Name: "input", Kind: KindCompute, Layout: layout,
			Prompt: "Advance the ball horizontally: set ball_x to ball_x plus vel_x."},
		{Name: "view", Kind: KindView, Layout: layout,
			Prompt: "Draw the ball as a circle at its position."},
	}
}

// FluxV1Variants are the two language variants measured so far: the baseline grammar
// (with reads/writes clauses) and the terse (clause-less, derived) candidate — the
// first proposed language evolution (docs/grammar-constrained-flux.md).
func FluxV1Variants() []Variant {
	return []Variant{
		{Name: "baseline(clauses)", Grammar: GBNF},
		{Name: "terse(no-clauses)", Grammar: GBNFTerse},
	}
}
