// Package stdlib is the system's STANDARD LIBRARY as source files, mapped into the
// executable with go:embed rather than hand-written as inline Go string literals. Each
// file is a real macro-WAT / WAT source with a small `;; key: value` metadata header;
// the loaders below parse the header and return the body, so the rest of the system
// seeds itself from these files instead of from strings scattered across packages.
//
//	prologue/*.macro  — the hygienic macros every cell may call as primitives
//	examples/*.wat|.macro — worked seed cells for the knowledge base
package stdlib

import (
	"embed"
	"sort"
	"strings"
)

//go:embed prologue/*.macro
var prologueFS embed.FS

//go:embed examples/*.wat examples/*.macro
var exampleFS embed.FS

//go:embed cells
var cellFS embed.FS

// Cell returns the source of a named standard cell (its file basename without
// extension, e.g. "map", "stream-fold"), and whether it exists. These are the live
// system cells (combinators, collections, streams, dict, demo cells) that seed the
// runtime — moved here from inline Go string literals.
func Cell(name string) (string, bool) {
	for _, ext := range []string{".wat", ".macro"} {
		if raw, err := cellFS.ReadFile("cells/" + name + ext); err == nil {
			_, body := splitHeaders(string(raw))
			return body, true
		}
	}
	return "", false
}

// MustCell returns a named standard cell's source or panics — for package-level var
// initializers, where a missing embedded file is a build-time programmer error.
func MustCell(name string) string {
	if s, ok := Cell(name); ok {
		return s
	}
	panic("stdlib: unknown standard cell " + name)
}

// Macro is one prologue macro source: the (defmacro …) body and its one-line doc.
type Macro struct {
	Src string
	Doc string
}

// Example is a worked seed cell for the knowledge base: its declared metadata and its
// source body. IsMacro is true for a macro-WAT (.macro) cell, false for raw WAT (.wat).
type Example struct {
	Kind      string
	Entry     string
	Semantics string
	Reads     []string
	Writes    []string
	Tags      []string
	Src       string
	IsMacro   bool
}

// Prologue returns the standard prologue macros, in filename order.
func Prologue() []Macro {
	names := readDirSorted(prologueFS, "prologue")
	out := make([]Macro, 0, len(names))
	for _, name := range names {
		raw, err := prologueFS.ReadFile("prologue/" + name)
		if err != nil {
			continue
		}
		hdr, body := splitHeaders(string(raw))
		out = append(out, Macro{Src: body, Doc: hdr["doc"]})
	}
	return out
}

// Examples returns the worked seed cells, in filename order.
func Examples() []Example {
	names := readDirSorted(exampleFS, "examples")
	out := make([]Example, 0, len(names))
	for _, name := range names {
		raw, err := exampleFS.ReadFile("examples/" + name)
		if err != nil {
			continue
		}
		hdr, body := splitHeaders(string(raw))
		out = append(out, Example{
			Kind:      hdr["kind"],
			Entry:     hdr["entry"],
			Semantics: hdr["semantics"],
			Reads:     splitCSV(hdr["reads"]),
			Writes:    splitCSV(hdr["writes"]),
			Tags:      splitCSV(hdr["tags"]),
			Src:       body,
			IsMacro:   strings.HasSuffix(name, ".macro"),
		})
	}
	return out
}

// readDirSorted lists the (non-directory) entries of dir in lexical order — filename
// prefixes (10-, 20-, …) therefore control the presentation order.
func readDirSorted(fsys embed.FS, dir string) []string {
	ents, err := fsys.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// splitHeaders peels a leading block of `;; key: value` comment lines off a source
// file and returns the parsed headers plus the remaining body (trimmed). Header keys
// are lower-cased. Blank lines within the leading block are skipped; the body begins
// at the first non-comment, non-blank line, so no `;;` metadata reaches the assembler.
func splitHeaders(raw string) (map[string]string, string) {
	hdr := map[string]string{}
	lines := strings.Split(raw, "\n")
	i := 0
	for i < len(lines) {
		t := strings.TrimSpace(lines[i])
		if t == "" {
			i++
			continue
		}
		if !strings.HasPrefix(t, ";;") {
			break
		}
		kv := strings.TrimSpace(strings.TrimPrefix(t, ";;"))
		if idx := strings.Index(kv, ":"); idx >= 0 {
			hdr[strings.ToLower(strings.TrimSpace(kv[:idx]))] = strings.TrimSpace(kv[idx+1:])
		}
		i++
	}
	return hdr, strings.TrimSpace(strings.Join(lines[i:], "\n"))
}

// splitCSV parses a "a, b, c" header value into a trimmed, non-empty slice.
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
