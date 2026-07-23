package evolution

import (
	"encoding/json"
	"fmt"

	"github.com/mdcfrancis/flow/storage"
)

// Trusted-evolvable VERIFICATION. A check's parameters may evolve (get stricter) so verification
// improves over time — but the app being graded must never be able to WEAKEN its own grader. The
// guard is a FIXED meta-acceptance corpus of labeled known-good / known-bad renders: any change
// to the check is adopted ONLY if it still classifies the ENTIRE corpus correctly. The corpus is
// the trust anchor — editable only here, never by an app — so the grader can tighten but not be
// gamed.

// CheckThresholds are the tunable parameters of the visual data-viz check.
type CheckThresholds struct {
	MinColors       int `json:"minColors"`
	MinBrightSpread int `json:"minBrightSpread"`
}

// DefaultCheckThresholds is the hand-tuned baseline (matches AuthorVisualCoverage's floor).
func DefaultCheckThresholds() CheckThresholds {
	return CheckThresholds{MinColors: 8, MinBrightSpread: 110}
}

const checkThresholdsRef = "urn:hdm:system:check-thresholds"

// LoadCheckThresholds returns the stored (gated) thresholds, or the defaults.
func LoadCheckThresholds(ledger *storage.LedgerEngine) CheckThresholds {
	t := DefaultCheckThresholds()
	if ledger == nil {
		return t
	}
	if h, err := ledger.GetRef(checkThresholdsRef); err == nil {
		if raw, rerr := ledger.ReadBlock(h); rerr == nil {
			_ = json.Unmarshal(raw, &t)
		}
	}
	return t
}

// SaveCheckThresholds persists a check change ONLY if it passes the meta-acceptance gate — the
// central trust property: you can never set thresholds that misclassify the fixed corpus.
func SaveCheckThresholds(ledger *storage.LedgerEngine, t CheckThresholds) error {
	if !MetaAcceptanceGate(t) {
		return fmt.Errorf("rejected: thresholds %+v misclassify the trust corpus (a known-good or known-bad case)", t)
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	h, err := ledger.WriteBlock(raw)
	if err != nil {
		return err
	}
	return ledger.UpdateRef(checkThresholdsRef, h)
}

// checkPasses evaluates the visual data-viz check with the given thresholds against a frame.
func checkPasses(t CheckThresholds, frame []DrawRecord) bool {
	return matchDraw(DrawExpect{MinColors: t.MinColors, MinBrightSpread: t.MinBrightSpread}, [][]DrawRecord{frame})
}

// MetaAcceptanceGate reports whether thresholds correctly classify the ENTIRE fixed trust
// corpus (accept every known-good render, reject every known-bad). This is the condition for a
// check change to be adopted.
func MetaAcceptanceGate(t CheckThresholds) bool {
	for _, c := range metaCorpus() {
		if checkPasses(t, c.frame) != c.shouldPass {
			return false
		}
	}
	return true
}

// TightenCheck adopts the STRICTEST gate-passing thresholds the corpus allows — so the grader
// tightens over time to the tightest bound that still accepts every good render (bad renders
// fail even harder). Never loosens below the current values. Returns the adopted thresholds and
// whether they changed.
func TightenCheck(ledger *storage.LedgerEngine) (CheckThresholds, bool) {
	cur := LoadCheckThresholds(ledger)
	// The tightest that still accepts every GOOD render = the minimum stats among good cases.
	minColors, minSpread := 1<<30, 1<<30
	for _, c := range metaCorpus() {
		if !c.shouldPass {
			continue
		}
		nc, sp := frameColorStats(c.frame)
		if nc < minColors {
			minColors = nc
		}
		if sp < minSpread {
			minSpread = sp
		}
	}
	cand := CheckThresholds{MinColors: max(cur.MinColors, minColors), MinBrightSpread: max(cur.MinBrightSpread, minSpread)}
	if cand == cur || !MetaAcceptanceGate(cand) {
		return cur, false
	}
	if err := SaveCheckThresholds(ledger, cand); err != nil {
		return cur, false
	}
	return cand, true
}

// frameColorStats returns the number of distinct colors and the brightness spread of a frame.
func frameColorStats(frame []DrawRecord) (colors, spread int) {
	seen := map[uint32]struct{}{}
	minL, maxL := 255, 0
	for _, r := range frame {
		seen[r.RGBA] = struct{}{}
		l := luma(r.RGBA)
		if l < minL {
			minL = l
		}
		if l > maxL {
			maxL = l
		}
	}
	if len(frame) == 0 {
		return 0, 0
	}
	return len(seen), maxL - minL
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// metaCase is one labeled example in the fixed trust corpus.
type metaCase struct {
	name       string
	frame      []DrawRecord
	shouldPass bool
}

func grayRects(vals ...uint8) []DrawRecord {
	f := make([]DrawRecord, len(vals))
	for i, v := range vals {
		f[i] = DrawRecord{Op: 1, A: int32(i), RGBA: uint32(v)<<24 | uint32(v)<<16 | uint32(v)<<8 | 0xff}
	}
	return f
}

// metaCorpus is the IMMUTABLE trust anchor: known-good and known-bad renders that a valid visual
// check MUST classify correctly. Good cases span the acceptable range (a rich gradient AND a
// minimal-but-acceptable one at the edge, so tightening can't over-fit); bad cases cover the
// ways a render fails (blank, too-few-colors, low-contrast). Editable ONLY here.
func metaCorpus() []metaCase {
	rich := grayRects(0, 17, 34, 51, 68, 85, 102, 119, 136, 153, 170, 187, 204, 221, 238, 255)
	// The least-acceptable good render: 10 distinct colors spanning 130 of brightness. This is
	// the edge — it bounds how far the grader can tighten without rejecting a valid render.
	minimalGood := grayRects(0, 14, 29, 43, 57, 72, 86, 101, 115, 130)
	blank := grayRects(16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16)
	twoColor := grayRects(0, 0, 0, 0, 0, 0, 255, 255, 255, 255, 255, 255)
	lowContrast := grayRects(100, 104, 108, 112, 116, 120, 124, 128, 132, 136, 140, 144)
	return []metaCase{
		{"good_rich_gradient", rich, true},
		{"good_minimal_gradient", minimalGood, true},
		{"bad_blank", blank, false},
		{"bad_two_color", twoColor, false},
		{"bad_low_contrast", lowContrast, false},
	}
}
