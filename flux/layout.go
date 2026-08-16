package flux

// Type is a shared-state field's element type. The macro surface only needs to tell
// an f32 field (native float math) from everything else (i32); the richer set is kept
// so a contract can be authored with intent (a Color is not a plain Int) and so the
// LayoutFromContract mapping is lossless.
type Type int

const (
	TInvalid Type = iota
	TInt          // i32
	TFloat        // f32
	TBool         // i32 0/1
	TColor        // i32 RGBA
	TUnit         // a terminal (no value)
	TBuffer       // a bounded array in shared memory: a base offset + length (element type in EType)
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
	case TBuffer:
		return "Buffer"
	default:
		return "?"
	}
}

// Field is one shared-state field: its declared element type and absolute offset into
// shared-cluster-memory. In the operational system this comes from the AppContract
// (app state) or the fixed hardware capabilities (HMI input); a test supplies it
// directly. The macro surface reads Type (f32 vs i32) and Offset; ReadOnly/Len carry
// contract intent (a host-written capability register, an array's element count).
type Field struct {
	Type     Type
	Offset   uint32
	ReadOnly bool   // a host-written capability field (e.g. the HMI input registers)
	Len      uint32 // element count for a TBuffer field; zero for scalars
	// EType is a TBuffer's ELEMENT type — TFloat for an f32[] array, otherwise i32
	// (the zero value TInvalid is read as i32). It lets atidx/setidx pick f32.load/
	// f32.store for a float array, so a particle system's positions can live in a
	// contract array as native floats, not just i32. Unused for scalar fields.
	EType Type
	// Stride is the byte gap between consecutive elements of a TBuffer field. Zero means
	// a DENSE array (stride 4). A non-zero stride makes the field a STRIDED VIEW into an
	// interleaved record buffer: particle_x, particle_y, … all point into one "particle"
	// buffer with Stride = bytes-per-record and Offset = base + this field's byte offset,
	// so (atidx particle_x $i) reads base + i*Stride + xOffset. This is how a collection
	// entity's fields address one array-of-structs — the layout matches what the map hands
	// the leaf (a pointer to one record), with no new macro and no renderer change.
	Stride uint32
	// Elem marks an ELEMENT field of a combinator LEAF: its Offset is relative to the
	// leaf's ARG POINTER (param 0 = a pointer to one element the combinator handed it),
	// not an absolute shared-memory offset. So (get x)/(set x) on a leaf read/write this
	// particle's x at argPtr+Offset, letting a per-element leaf author macro-WAT over its
	// own element instead of the whole global array.
	Elem bool
}

// Layout maps each shared-state field name to its type and offset — the ground truth
// the macro surface expands (get/set/atidx/…) against.
type Layout map[string]Field
