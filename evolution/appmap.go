package evolution

// The APPLICATION MAP is the conceptual model of an app that cells are otherwise
// missing. A cell is synthesized in isolation: it sees its own goal, its own
// checks, and the shared-state contract's data layout — but nothing about the
// SYSTEM it belongs to. So a renderer had no way to know that a sibling writes
// player_x for it to draw, and drew a static scene; scenario authors invented
// fields; subsystems overlapped.
//
// AppMap fixes that. It is a living map of the application — every component, its
// role, what it VERIFIABLY reads and writes, what it has been proven to do, and
// (most usefully) the resulting data flow with its GAPS: "player_x is written by
// player-controller and read by NOBODY". It is merged into as cells are built —
// each convergence folds that component's verified behavior back into the map —
// and injected into every synthesis and authoring prompt, so each cell is built
// to fit the system rather than in a vacuum.
//
// Facts are DERIVED, never asserted: reads/writes come from a cell's *passing*
// coordination checks and a scan of its genotype's constant-address memory
// accesses. A short model-written narrative is layered on top for readability.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/mdcfrancis/flow/storage"
)

// HMI input register window. Reads here mean the cell consumes operator input.
const hmiLo, hmiHi = 0x00050000, 0x00051000

// ComponentMap is one subsystem's entry in the application map.
type ComponentMap struct {
	Identity string `json:"identity"`
	Role     string `json:"role"`             // its semantics / functional intent
	Entry    string `json:"entry,omitempty"`  // run-tick | render-frame
	Status   string `json:"status,omitempty"` // planned | building | converged | stalled
	Passed   int    `json:"passed,omitempty"`
	Total    int    `json:"total,omitempty"`
	// DeclaredReads/DeclaredWrites are the component's INTENDED interface from the
	// envelope — the contract fields it is supposed to consume/produce. Writes/
	// Reads are what it VERIFIABLY does (from passing checks + a genotype scan).
	// The two together show whether a cell is wired as intended: a declared write
	// that is not yet verified is unfinished; a component reading nothing it was
	// meant to is the "static renderer" bug made visible.
	DeclaredReads  []string `json:"declaredReads,omitempty"`
	DeclaredWrites []string `json:"declaredWrites,omitempty"`
	Writes         []string `json:"writes,omitempty"`
	Reads          []string `json:"reads,omitempty"`
	// Verified renders the checks this component actually PASSES — what it is
	// proven to do, not what it claims.
	Verified []string `json:"verified,omitempty"`
	// Narrative is a short model-written summary of the component's role, folded
	// in when it converges. Prose only; the facts above are the ground truth.
	Narrative string `json:"narrative,omitempty"`
}

// AppMap is the evolving conceptual model of one application.
type AppMap struct {
	Namespace  string         `json:"namespace"`
	Objective  string         `json:"objective"`
	Components []ComponentMap `json:"components"`
}

func appMapRefURN(namespace string) string { return namespace + ":map" }

// SaveAppMap persists an application's map.
func SaveAppMap(ledger *storage.LedgerEngine, namespace string, m *AppMap) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode app map: %w", err)
	}
	h, err := ledger.WriteBlock(raw)
	if err != nil {
		return fmt.Errorf("persist app map: %w", err)
	}
	return ledger.UpdateRef(appMapRefURN(namespace), h)
}

// LoadAppMap returns an application's map, or nil if none exists.
func LoadAppMap(ledger *storage.LedgerEngine, namespace string) *AppMap {
	h, err := ledger.GetRef(appMapRefURN(namespace))
	if err != nil {
		return nil
	}
	raw, err := ledger.ReadBlock(h)
	if err != nil {
		return nil
	}
	var m AppMap
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return &m
}

// Upsert merges a component into the map (by identity), preserving a narrative
// the refresh did not recompute.
func (m *AppMap) Upsert(c ComponentMap) {
	for i := range m.Components {
		if m.Components[i].Identity == c.Identity {
			if c.Narrative == "" {
				c.Narrative = m.Components[i].Narrative
			}
			m.Components[i] = c
			return
		}
	}
	m.Components = append(m.Components, c)
}

// Remove drops a component (e.g. a parent retired by fracture).
func (m *AppMap) Remove(identity string) {
	out := m.Components[:0]
	for _, c := range m.Components {
		if c.Identity != identity {
			out = append(out, c)
		}
	}
	m.Components = out
}

// shortName is the last URN segment, for compact prompt rendering.
func shortName(urn string) string {
	if i := strings.LastIndex(urn, ":"); i >= 0 {
		return urn[i+1:]
	}
	return urn
}

// Render formats the map for a synthesis/authoring prompt. The DATA FLOW section
// is the point: it shows which shared fields are produced and consumed, and names
// the GAPS — a field written by someone and read by nobody is a missing consumer,
// which is exactly the hint a renderer needs to stop drawing a static scene.
func (m *AppMap) Render() string {
	if m == nil || len(m.Components) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "APPLICATION MAP — %s\n", m.Namespace)
	if m.Objective != "" {
		fmt.Fprintf(&b, "OBJECTIVE: %s\n", m.Objective)
	}
	b.WriteString("\nCOMPONENTS — the system your cell is part of. Build it to FIT this system:\n")
	for _, c := range m.Components {
		status := c.Status
		if c.Total > 0 {
			status = fmt.Sprintf("%s %d/%d", c.Status, c.Passed, c.Total)
		}
		fmt.Fprintf(&b, "- %s (%s) [%s]\n", shortName(c.Identity), orRunTick(c.Entry), status)
		if c.Role != "" {
			fmt.Fprintf(&b, "    role: %s\n", c.Role)
		}
		if c.Narrative != "" {
			fmt.Fprintf(&b, "    summary: %s\n", c.Narrative)
		}
		if len(c.DeclaredReads) > 0 || len(c.DeclaredWrites) > 0 {
			fmt.Fprintf(&b, "    interface: reads {%s} writes {%s}\n",
				strings.Join(c.DeclaredReads, ", "), strings.Join(c.DeclaredWrites, ", "))
		}
		if len(c.Writes) > 0 {
			fmt.Fprintf(&b, "    verified-writes: %s\n", strings.Join(c.Writes, ", "))
		}
		if len(c.Reads) > 0 {
			fmt.Fprintf(&b, "    verified-reads: %s\n", strings.Join(c.Reads, ", "))
		}
		for _, v := range c.Verified {
			fmt.Fprintf(&b, "    verified: %s\n", v)
		}
	}

	// Data flow: producers/consumers per shared field, and the gaps.
	// Producers/consumers span the INTENDED interface (declared ports) unioned with
	// what is VERIFIED, so the flow reflects the architecture even before a wiring
	// is proven, and a genuinely unconsumed field still surfaces as a gap.
	producers := map[string][]string{}
	consumers := map[string][]string{}
	for _, c := range m.Components {
		sn := shortName(c.Identity)
		for _, f := range mergeFields(c.Writes, c.DeclaredWrites) {
			producers[f] = appendUnique(producers[f], sn)
		}
		for _, f := range mergeFields(c.Reads, c.DeclaredReads) {
			consumers[f] = appendUnique(consumers[f], sn)
		}
	}
	fields := map[string]bool{}
	for f := range producers {
		fields[f] = true
	}
	for f := range consumers {
		fields[f] = true
	}
	if len(fields) > 0 {
		names := make([]string, 0, len(fields))
		for f := range fields {
			names = append(names, f)
		}
		sort.Strings(names)
		b.WriteString("\nSHARED-STATE DATA FLOW (who produces / consumes each field):\n")
		var orphans []string
		for _, f := range names {
			p, cns := producers[f], consumers[f]
			fmt.Fprintf(&b, "- %s: written by %s; read by %s\n", f, orNobody(p), orNobody(cns))
			if len(p) > 0 && len(cns) == 0 && f != "HMI input" {
				orphans = append(orphans, f)
			}
		}
		if len(orphans) > 0 {
			fmt.Fprintf(&b, "\nGAP: %s written but read by NOBODY — a consumer is missing. If your cell\n"+
				"is the view/consumer for this state, READ these fields and act on them (e.g. draw the\n"+
				"sprite at the position they hold) instead of using hardcoded values.\n",
				strings.Join(orphans, ", "))
		}
	}
	return b.String()
}

func orRunTick(e string) string {
	if e == "" {
		return "run-tick"
	}
	return e
}

func orNobody(v []string) string {
	if len(v) == 0 {
		return "NOBODY"
	}
	return strings.Join(v, ", ")
}

func appendUnique(v []string, s string) []string {
	for _, x := range v {
		if x == s {
			return v
		}
	}
	return append(v, s)
}

// --- fact derivation ---------------------------------------------------------

// FieldAtOffset names the contract field covering an offset string, "HMI input"
// for the input register, or "" if it maps to neither.
func FieldAtOffset(c *AppContract, at string) string {
	off, err := strconv.ParseInt(strings.TrimSpace(at), 0, 64)
	if err != nil {
		return ""
	}
	if off >= hmiLo && off < hmiHi {
		return "HMI input"
	}
	return fieldAtInt(c, int(off))
}

// fieldAtInt names the contract field whose extent covers off (arrays span
// 4*count bytes from their base).
func fieldAtInt(c *AppContract, off int) string {
	if c == nil {
		return ""
	}
	for _, f := range c.Fields {
		if off >= f.Offset && off < f.Offset+4*typeWords(f.Type) {
			return f.Name
		}
	}
	return ""
}

// typeWords is how many i32 slots a contract type occupies ("i32" => 1,
// "i32[24]" => 24).
func typeWords(t string) int {
	i, j := strings.IndexByte(t, '['), strings.IndexByte(t, ']')
	if i < 0 || j <= i+1 {
		return 1
	}
	n, err := strconv.Atoi(strings.TrimSpace(t[i+1 : j]))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// ScanMemoryAccess statically derives which contract fields (and the HMI input
// register) a genotype loads from and stores to, by finding constant addresses
// adjacent to i32.load / i32.store in either s-expression or linear WAT form.
// Heuristic but code-derived — it reports what the cell actually touches.
func ScanMemoryAccess(wat string, c *AppContract) (reads, writes []string) {
	toks := tokenizeWAT(wat)
	rs, ws := map[string]bool{}, map[string]bool{}

	// s-expression form — the address is the op's FIRST child: `(op (i32.const N)`.
	// The parens matter: without them this is indistinguishable from the linear
	// form, where the const that follows the op belongs to the NEXT instruction.
	childConst := func(i int) string {
		if i+3 < len(toks) && toks[i+1] == "(" && toks[i+2] == "i32.const" {
			return FieldAtOffset(c, toks[i+3])
		}
		return ""
	}
	// linear/stack form — scan back for the nearest constant that names a field,
	// skipping the value operand (`i32.const ADDR; i32.const VAL; i32.store`) but
	// stopping at the previous memory op so we never borrow its address.
	backField := func(i int) string {
		for j := i - 1; j >= 0 && j > i-40; j-- {
			if strings.HasPrefix(toks[j], "i32.store") || strings.HasPrefix(toks[j], "i32.load") {
				return ""
			}
			if toks[j] == "i32.const" && j+1 < len(toks) {
				if f := FieldAtOffset(c, toks[j+1]); f != "" {
					return f
				}
			}
		}
		return ""
	}

	for i, t := range toks {
		switch {
		case strings.HasPrefix(t, "i32.store"):
			if f := childConst(i); f != "" {
				ws[f] = true
			} else if f := backField(i); f != "" {
				ws[f] = true
			}
		case strings.HasPrefix(t, "i32.load"):
			if f := childConst(i); f != "" {
				rs[f] = true
			} else if i >= 2 && toks[i-2] == "i32.const" {
				if f := FieldAtOffset(c, toks[i-1]); f != "" { // linear: const then load
					rs[f] = true
				}
			}
		}
	}
	return sortedKeys(rs), sortedKeys(ws)
}

// tokenizeWAT splits WAT into tokens, KEEPING parens (they distinguish the
// s-expression form from the linear/stack form) and dropping `;;` line comments
// (whose text could otherwise be mistaken for constants).
func tokenizeWAT(wat string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(wat); i++ {
		ch := wat[i]
		if ch == ';' && i+1 < len(wat) && wat[i+1] == ';' { // line comment
			flush()
			for i < len(wat) && wat[i] != '\n' {
				i++
			}
			continue
		}
		switch ch {
		case '(', ')':
			flush()
			out = append(out, string(ch))
		case ' ', '\t', '\n', '\r':
			flush()
		default:
			cur.WriteByte(ch)
		}
	}
	flush()
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// DeriveFromChecks reads a component's VERIFIED facts off its passing checks: a
// passing `reads` postcondition on a field proves the cell WRITES it; a passing
// scenario that seeds a field proves the cell CONSUMES it. Returns the field
// names plus a plain-language list of what the cell is proven to do.
func DeriveFromChecks(suite *AcceptanceSuite, passed []bool, c *AppContract) (reads, writes, verified []string) {
	if suite == nil {
		return nil, nil, nil
	}
	rs, ws := map[string]bool{}, map[string]bool{}
	for i, sc := range suite.Scenarios {
		if i >= len(passed) || !passed[i] {
			continue
		}
		verified = append(verified, sc.Name+": "+describeScenario(sc))
		for _, w := range sc.Expect.Reads { // it made this field hold a value ⇒ it writes it
			if f := FieldAtOffset(c, w.At); f != "" {
				ws[f] = true
			}
		}
		for _, s := range sc.Seed { // it responded to this seeded state ⇒ it reads it
			if f := FieldAtOffset(c, s.At); f != "" {
				rs[f] = true
			}
		}
	}
	return sortedKeys(rs), sortedKeys(ws), verified
}

// mergeFields unions two field-name lists, preserving sorted order.
func mergeFields(a, b []string) []string {
	set := map[string]bool{}
	for _, v := range a {
		set[v] = true
	}
	for _, v := range b {
		set[v] = true
	}
	return sortedKeys(set)
}
