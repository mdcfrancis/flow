// Package flux is the front end of the Flux functional IR — the small, strict,
// typed, S-expression language HDM cells are authored in, which a deterministic
// lowerer turns into valid WAT (see docs/functional-ir.md).
//
// This file is the READER: concrete Flux syntax -> a generic S-expression tree.
// The grammar is expressed as Go struct tags and driven by participle, so we
// maintain a grammar rather than a hand-written parser, and get a lexer plus
// position-tracked, human-readable syntax errors for free (those messages feed
// the synthesis correction loop). The typed AST, type checker, and WAT lowerer
// build on this tree.
package flux

import (
	"github.com/alecthomas/participle/v2"
	"github.com/alecthomas/participle/v2/lexer"
)

// The grammar lives in `parser:"…"` struct tags (participle's go-vet-safe keyed
// form). participle reads the value; go vet sees a conventional key:"value" tag.

// File is a parsed Flux source unit: a sequence of top-level forms (cells).
type File struct {
	Forms []*List `parser:"@@*"`
}

// Sexp is one S-expression: an atom or a parenthesized list.
type Sexp struct {
	Atom *Atom `parser:"  @@"`
	List *List `parser:"| @@"`
}

// List is a parenthesized form. "(" and "[" (and their closers) are
// interchangeable delimiters, per the Scheme convention that `let` uses brackets.
type List struct {
	Pos   lexer.Position
	Items []*Sexp `parser:"LParen @@* RParen"`
}

// Atom is a leaf token. Exactly one field is non-nil; which one records the
// lexical kind, so the typed-AST pass can build a typed literal (Int/Float/Char/
// Color) or a name (Symbol — a field read, let binding, or primitive operator).
type Atom struct {
	Pos    lexer.Position
	Float  *string `parser:"  @Float"`
	Int    *string `parser:"| @Int"`
	Char   *string `parser:"| @Char"`
	Color  *string `parser:"| @Color"`
	Symbol *string `parser:"| @Ident"`
}

// The Flux lexer. Order matters: numbers and the color/char literals are matched
// before Ident so a leading '-' on a number is not mistaken for the subtraction
// operator, and '#x…'/'…' are not split. Ident deliberately admits operator
// characters (+ - * / < > = ! ? and '-' for names like char->int), so operators
// are ordinary symbols in head position — there is no special operator token.
var fluxLexer = lexer.MustSimple([]lexer.SimpleRule{
	{Name: "Comment", Pattern: `;[^\n]*`},
	{Name: "Whitespace", Pattern: `\s+`},
	{Name: "Color", Pattern: `#x[0-9A-Fa-f]+`},
	{Name: "Char", Pattern: `'(\\.|[^'])'`},
	{Name: "Float", Pattern: `-?\d+\.\d+`},
	{Name: "Int", Pattern: `-?\d+`},
	{Name: "LParen", Pattern: `[(\[]`},
	{Name: "RParen", Pattern: `[)\]]`},
	{Name: "Ident", Pattern: `[a-zA-Z_+*/<>=!?-][a-zA-Z0-9_+*/<>=!?-]*`},
})

var parser = participle.MustBuild[File](
	participle.Lexer(fluxLexer),
	participle.Elide("Whitespace", "Comment"),
	participle.UseLookahead(2),
)

// Parse reads Flux source into its top-level forms. A syntax error carries a
// filename:line:col position from participle.
func Parse(filename, src string) (*File, error) {
	return parser.ParseString(filename, src)
}

// Head returns the leading symbol of a list — e.g. "cell", "reads", "write" — or
// "" if the list is empty or does not lead with a symbol.
func (l *List) Head() string {
	if l == nil || len(l.Items) == 0 || l.Items[0].Atom == nil || l.Items[0].Atom.Symbol == nil {
		return ""
	}
	return *l.Items[0].Atom.Symbol
}

// Sub returns the first child list with the given head (e.g. the "reads" clause
// of a cell), or nil.
func (l *List) Sub(head string) *List {
	if l == nil {
		return nil
	}
	for _, it := range l.Items {
		if it.List != nil && it.List.Head() == head {
			return it.List
		}
	}
	return nil
}
