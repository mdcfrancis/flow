package flux

// Compile parses, type-checks, and lowers Flux source to WAT text against a
// shared-state field Layout. It is the single entry point the operational system
// calls: the model authors Flux, and this returns WAT the existing HDM assembler
// accepts — so the genome/runtime/verification path downstream is unchanged.
//
// Any error is positioned and human-readable (a syntax error from the reader or a
// type/shape error from the checker), suitable to feed straight back into the
// synthesis correction loop.
func Compile(filename, src string, layout Layout) (string, error) {
	f, err := Parse(filename, src)
	if err != nil {
		return "", err
	}
	cell, err := Check(f, layout)
	if err != nil {
		return "", err
	}
	return Lower(cell, layout)
}
