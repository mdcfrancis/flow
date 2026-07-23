package execution

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/storage"
)

func newRM(t *testing.T) (*RuntimeManager, context.Context) {
	t.Helper()
	ctx := context.Background()
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	t.Cleanup(func() { le.Close() })
	rm, err := NewRuntimeManager(ctx, le, nil)
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	t.Cleanup(func() { rm.Close(ctx) })
	return rm, ctx
}

// TestInputRegisterContinuousAndDiscrete verifies the HMI register semantics:
// mouse moves refresh the continuous zone but never bump the discrete-event
// sequence, while clicks/keys latch the discrete slot under a fresh sequence.
func TestInputRegisterContinuousAndDiscrete(t *testing.T) {
	rm, _ := newRM(t)

	// A move updates position but leaves the discrete latch untouched.
	if err := rm.WriteInputEvent(InputEvent{Type: EvMove, X: 12, Y: 34, Buttons: 0}); err != nil {
		t.Fatalf("move: %v", err)
	}
	if got := int32(rm.readU32(InMouseX)); got != 12 {
		t.Fatalf("mouseX = %d, want 12", got)
	}
	if got := int32(rm.readU32(InMouseY)); got != 34 {
		t.Fatalf("mouseY = %d, want 34", got)
	}
	if seq := rm.readU32(InEventSeq); seq != 0 {
		t.Fatalf("move bumped eventSeq to %d, want 0", seq)
	}

	// A click latches the discrete slot and advances the sequence.
	if err := rm.WriteInputEvent(InputEvent{Type: EvClick, X: 100, Y: 80, Buttons: 1}); err != nil {
		t.Fatalf("click: %v", err)
	}
	if seq := rm.readU32(InEventSeq); seq != 1 {
		t.Fatalf("click eventSeq = %d, want 1", seq)
	}
	if et := rm.readU32(InEventType); et != EvClick {
		t.Fatalf("eventType = %d, want %d", et, EvClick)
	}
	if x, y := int32(rm.readU32(InEventX)), int32(rm.readU32(InEventY)); x != 100 || y != 80 {
		t.Fatalf("event coords = (%d,%d), want (100,80)", x, y)
	}

	// A subsequent move must NOT clobber the unconsumed click.
	if err := rm.WriteInputEvent(InputEvent{Type: EvMove, X: 5, Y: 5}); err != nil {
		t.Fatalf("move 2: %v", err)
	}
	if seq := rm.readU32(InEventSeq); seq != 1 {
		t.Fatalf("move clobbered discrete seq: %d, want 1", seq)
	}
	if et := rm.readU32(InEventType); et != EvClick {
		t.Fatalf("move clobbered eventType: %d, want %d", et, EvClick)
	}

	// A keydown advances the sequence again and records the key code.
	if err := rm.WriteInputEvent(InputEvent{Type: EvKeyDown, Key: 65}); err != nil {
		t.Fatalf("keydown: %v", err)
	}
	if seq := rm.readU32(InEventSeq); seq != 2 {
		t.Fatalf("keydown eventSeq = %d, want 2", seq)
	}
	if k := rm.readU32(InEventKey); k != 65 {
		t.Fatalf("keyCode = %d, want 65", k)
	}
}
