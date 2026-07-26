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
// Valid/Syntax are nil and the shared S-expr Flux checker is used; a variant with a
// different SURFACE (e.g. Forth) supplies its own Valid (parses+type-checks to the
// IR) and Syntax (parses only) so the rates are measured in that surface, not s-expr.
type Variant struct {
	Name     string
	Grammar  func(Layout, CellKind) string
	Valid    func(src string, layout Layout) bool // nil → parse + type-check s-expr
	Syntax   func(src string, layout Layout) bool // nil → parse s-expr
	Preamble string                               // surface description prepended to the task (the "atomic building blocks" the model composes)
}

// Generator produces one completion for a prompt under a grammar constraint,
// returning the program text and its completion-token count. Injected so the
// scoreboard is testable without a live model.
type Generator interface {
	Generate(prompt, grammar string) (src string, tokens int, err error)
}

// Score is a variant's measured fitness on the benchmark.
type Score struct {
	Variant    string
	Samples    int     // total generations attempted
	SyntaxRate float64 // fraction that PARSE — what the grammar guarantees by construction
	ValidRate  float64 // fraction that parse + TYPE-CHECK — a usable cell (grammar does not guarantee types)
	// The gap SyntaxRate−ValidRate is the type-error rate the grammar admits — a
	// language-evolution target: making a class of type errors ungrammatical would
	// close it (docs/language-evolution.md).
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
		syntaxFn := v.Syntax
		if syntaxFn == nil {
			syntaxFn = func(src string, _ Layout) bool { return parses(src) }
		}
		var totalSyntax, totalValid, totalSamples, tokenN int
		var tokenSum, canonAcc float64
		var canonTasks int
		for _, task := range tasks {
			g := v.Grammar(task.Layout, task.Kind)
			var validSrcs []string
			prompt := task.Prompt
			if v.Preamble != "" {
				prompt = v.Preamble + "\n\n" + task.Prompt
			}
			for i := 0; i < samples; i++ {
				totalSamples++
				src, toks, err := gen.Generate(prompt, g)
				if err != nil {
					continue
				}
				if syntaxFn(src, task.Layout) {
					totalSyntax++
				}
				if !validFn(src, task.Layout) {
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
			s.SyntaxRate = float64(totalSyntax) / float64(totalSamples)
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

// parses reports whether the source is syntactically well-formed Flux — what a
// grammar-constrained decode guarantees by construction, independent of types.
func parses(src string) bool {
	_, err := Parse("m", src)
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

// SurfaceVariants are the two SURFACES over the same IR — the S-expression baseline
// and the concatenative Forth surface — to be A/B'd for LLM efficiency
// (docs/flux-surface-ir.md). Forth supplies its own Valid/Syntax because its source
// is a word stream, not an S-expression; both lower through the identical invariant,
// so anything either emits is behavior-safe by BehaviorHash.
func SurfaceVariants() []Variant {
	return []Variant{
		SExprVariant(),
		ForthVariant("forth", forthMaxDepth),
	}
}

// SExprVariant is the S-expression baseline surface.
func SExprVariant() Variant {
	return Variant{Name: "sexpr", Grammar: GBNF, Preamble: sexprPreamble}
}

// ForthVariant builds a Forth scoreboard variant at a given stack-depth bound — the
// lever under test: a tighter bound forbids deeper inline expressions, forcing more
// intermediates into the `=:` prologue (shallower, more inspectable stacks), which we
// measure against validity (docs/flux-surface-ir.md).
func ForthVariant(name string, maxDepth int) Variant {
	return Variant{
		Name:     name,
		Grammar:  func(l Layout, k CellKind) string { return gbnfForth(l, k, maxDepth) },
		Valid:    func(src string, l Layout) bool { _, err := (Forth{}).Read("m", src, l); return err == nil },
		Syntax:   func(src string, l Layout) bool { _, err := forthToSexpr(src, l); return err == nil },
		Preamble: forthPreamble,
	}
}

const sexprPreamble = `FLUX (S-EXPRESSION) — write (cell c (reads …) (writes …) BODY).
BODY = (let ([t0 expr] …) (write (field expr) …))  or, for a view, (draw (circle cx cy r #xRRGGBBAA) …).
Operators are PREFIX: (+ a b) (- a b) (clamp x lo hi) (if cond then else) (< a b) (or a b) (neg x).
Example:  (cell c (reads ball_x vel_x) (writes ball_x) (write (ball_x (+ ball_x vel_x))))`

const forthPreamble = `FLUX (FORTH) — write a cell as a POSTFIX word stream over a stack. Operands come
BEFORE the operator, which pops them and pushes its result. Word stack-effects
( inputs -- output ):
    +  ( a b -- a+b )    -  ( a b -- a-b )    *  ( a b -- a*b )    /  ( a b -- a/b )    mod ( a b -- a%b )
    <  ( a b -- flag )   <= > >= = !=  (same shape)     and or ( a b -- flag )     not neg abs ( a -- x )
    min max ( a b -- x )     clamp ( x lo hi -- x' )     ?  ( cond then else -- picked )   [both branches evaluated]
    =: NAME   ( v -- )  binds the top value to a local NAME (t0..t7)
    -> FIELD  ( v -- )  writes the top value to a shared field
    circle    ( cx cy r color -- )   also rect/line ( x y w h color -- )   emit a draw (view cells)
RULES:
1. Keep every stack SHALLOW. The moment a value is reused or an expression is more than ~2 words deep, bind it: ` + "`" + `… =: t0` + "`" + ` then use ` + "`" + `t0` + "`" + `.
2. Reads and writes are inferred — do NOT declare them.
3. Compute the shared updates with ` + "`" + `-> field` + "`" + `; a view draws with circle/rect/line.
Example (note how each intermediate is named so no stack is deep):
    ball_x vel_x + =: t0
    ball_y vel_y + =: t1
    t0 0 screen_w 1 - clamp -> ball_x
    t1 0 screen_h 1 - clamp -> ball_y`

// FluxV1Variants are the language variants measured so far: the baseline grammar
// (with reads/writes clauses), the terse (clause-less, derived) candidate — the first
// proposed evolution, a measured negative — and the canonical candidate, which
// removes write-pair ordering to attack the low-canonicality weak axis
// (docs/grammar-constrained-flux.md, docs/language-evolution.md).
func FluxV1Variants() []Variant {
	return []Variant{
		{Name: "baseline(clauses)", Grammar: GBNF},
		{Name: "terse(no-clauses)", Grammar: GBNFTerse},
		{Name: "canonical(fixed-writes)", Grammar: GBNFCanonical},
	}
}
