package flux

import "fmt"

// Type is a Flux value type. Every type lowers to a wasm numeric, but the type
// system keeps the domains apart so a keystroke can never be added to a pixel
// (see docs/functional-ir.md §4.3). v1 exercises Int/Bool/Color; Float and Char
// are defined for the type system and lowered in later increments.
type Type int

const (
	TInvalid Type = iota
	TInt          // i32
	TFloat        // f32
	TBool         // i32 0/1 — the result of a comparison; guards `if`
	TColor        // i32 RGBA — a draw color, distinct from Int
	TUnit         // the type of a `write`/`draw` terminal
)

func (t Type) String() string {
	switch t {
	case TInt:
		return "Int"
	case TFloat:
		return "Float"
	case TBool:
		return "Bool"
	case TColor:
		return "Color"
	case TUnit:
		return "Unit"
	default:
		return "?"
	}
}

// Field is one shared-state field: its declared type and absolute offset into
// shared-cluster-memory. In the operational system this comes from the
// AppContract; a test supplies it directly.
type Field struct {
	Type   Type
	Offset uint32
}

// Layout maps each shared-state field name to its type and offset.
type Layout map[string]Field

// CellKind is the entry shape a cell implements.
type CellKind int

const (
	KindCompute CellKind = iota // run-tick   : Record(reads) -> Record(writes)
	KindView                    // render-frame: Record(reads) -> DrawList
)

// Cell is the typed, checked form of a Flux cell — the input to lowering.
type Cell struct {
	Name   string
	Kind   CellKind
	Reads  []string // declared read fields (order preserved)
	Writes []string // declared write fields (compute only)
	Body   Expr     // a chain of Lets ending in a Write (compute) or Draw (view)
}

// Expr is a typed Flux expression. T() is filled by the checker.
type Expr interface {
	T() Type
	pos() string
}

type base struct {
	Typ Type
	Pos string
}

func (b base) T() Type     { return b.Typ }
func (b base) pos() string { return b.Pos }

// Literals.
type IntLit struct {
	base
	V int32
}
type FloatLit struct {
	base
	V float32
}
type BoolLit struct {
	base
	V bool
}
type ColorLit struct {
	base
	V uint32
}

// Var is a reference to a read field or a let binding.
type Var struct {
	base
	Name string
}

// Prim is a primitive application: (+ a b), (< a b), (if c t e), (clamp x lo hi)…
type Prim struct {
	base
	Op   string
	Args []Expr
}

// Let binds names to values, then evaluates Body with them in scope.
type Let struct {
	base
	Names []string
	Vals  []Expr
	Body  Expr
}

// Write is the compute terminal: store each field's value back to the store.
type Write struct {
	base
	Fields []string
	Vals   []Expr
}

// Draw is the view terminal: a list of draw primitives.
type Draw struct {
	base
	Prims []DrawPrim
}

// DrawPrim is one draw record: circle | rect | line, with a trailing Color.
type DrawPrim struct {
	Op   string // circle | rect | line
	Args []Expr // geometry ints, then a Color
	Pos  string
}

// checkError is a positioned, human-readable type/shape error — the message the
// synthesis correction loop feeds back to the model.
type checkError struct {
	pos string
	msg string
}

func (e *checkError) Error() string {
	if e.pos != "" {
		return fmt.Sprintf("%s: %s", e.pos, e.msg)
	}
	return e.msg
}

func errf(pos, format string, a ...any) *checkError {
	return &checkError{pos: pos, msg: fmt.Sprintf(format, a...)}
}
