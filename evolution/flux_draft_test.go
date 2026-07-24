package evolution

import "testing"

// The DFS iteration base: a working draft is stored and shown for refinement, and
// UNWIND (ClearFluxDraft) drops it so synthesis restarts from the last commit.
func TestFluxDraftStoreAndUnwind(t *testing.T) {
	le := kbLedger(t)
	urn := "urn:hdm:apps:x:physics"
	if LoadFluxDraft(le, urn) != "" {
		t.Fatal("expected no draft initially")
	}
	const draft = "(cell physics (reads ball_x) (writes ball_x) (write (ball_x (+ ball_x 1))))"
	if err := SaveFluxDraft(le, urn, draft); err != nil {
		t.Fatalf("save: %v", err)
	}
	if LoadFluxDraft(le, urn) != draft {
		t.Fatal("draft not stored/loaded")
	}
	// A newer draft replaces the older (iterate forward).
	const draft2 = "(cell physics (reads ball_x ball_vx) (writes ball_x) (write (ball_x (+ ball_x ball_vx))))"
	_ = SaveFluxDraft(le, urn, draft2)
	if LoadFluxDraft(le, urn) != draft2 {
		t.Fatal("draft not updated to the newer iteration")
	}
	// Unwind: the draft is dropped → next synthesis restarts from the commit.
	ClearFluxDraft(le, urn)
	if LoadFluxDraft(le, urn) != "" {
		t.Fatal("unwind did not clear the draft")
	}
	// Empty saves are no-ops (never clobber a good draft with nothing).
	_ = SaveFluxDraft(le, urn, draft)
	_ = SaveFluxDraft(le, urn, "   ")
	if LoadFluxDraft(le, urn) != draft {
		t.Fatal("an empty save must not overwrite the working draft")
	}
}
