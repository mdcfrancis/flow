package evolution

import (
	"context"
	"testing"

	"github.com/mdcfrancis/flow/flux"
)

// The Flux bridge exposes the full HMI capability surface as read-only fields, so
// a model can respond to input without inventing an offset — the gap that made a
// coder model fail 9× reaching for "HMI_input".
func TestHMICapabilitiesExposed(t *testing.T) {
	want := []string{"hmi_mouse_x", "hmi_mouse_y", "hmi_buttons", "hmi_modifiers",
		"hmi_event_seq", "hmi_event_type", "hmi_event_x", "hmi_event_y", "hmi_key"}
	for _, n := range want {
		f, ok := hmiFields[n]
		if !ok {
			t.Fatalf("HMI field %q not exposed", n)
		}
		if !f.ReadOnly {
			t.Fatalf("HMI field %q must be read-only", n)
		}
	}
	// hmi_key sits at the InputBase (0x50000) + 0x20 key-code slot.
	if hmiFields["hmi_key"].Offset != 0x50020 {
		t.Fatalf("hmi_key offset = 0x%X, want 0x50020", hmiFields["hmi_key"].Offset)
	}
}

// A Flux input cell that reads the HMI key and steers state lowers and runs
// through the operational harness: a seeded 'right-arrow' key advances player_x.
func TestFluxInputCellRespondsToKeyOperationally(t *testing.T) {
	layout := flux.Layout{
		"player_x": {Type: flux.TInt, Offset: 0xB0000},
	}
	for n, f := range hmiFields {
		layout[n] = f
	}
	src := `
	(cell input
	  (reads hmi_key player_x screen_w)
	  (writes player_x)
	  (write (player_x
	    (clamp (if (= hmi_key 39) (+ player_x 4)
	               (if (= hmi_key 37) (- player_x 4) player_x))
	           0 316))))`
	// screen_w isn't in the layout on purpose — prove the checker catches it, then
	// drop it and compile the real cell.
	if _, err := flux.Compile("bad", src, layout); err == nil {
		t.Fatal("expected an unknown-field error for screen_w")
	}

	good := `
	(cell input
	  (reads hmi_key player_x)
	  (writes player_x)
	  (write (player_x
	    (clamp (if (= hmi_key 39) (+ player_x 4)
	               (if (= hmi_key 37) (- player_x 4) player_x))
	           0 316))))`
	bc := compileFluxCell(t, good, layout)

	// Seed player_x=100 and a right-arrow (39) key at the key slot; expect player_x→104.
	scen := []Scenario{{Name: "right_arrow_moves_player", Entry: "run-tick", Steps: 1,
		Seed: []SeedWrite{
			{At: "0xB0000", U32: []uint32{100}}, // player_x
			{At: "0x50020", U32: []uint32{39}},  // hmi_key = right arrow
		},
		Expect: ScenarioExpect{Reads: []SeedWrite{{At: "0xB0000", U32: []uint32{104}}}}}}
	if pass, total := ScenarioScore(context.Background(), bc, scen, DefaultPayloadOffset, DefaultStateWindow, nil); pass != total {
		t.Fatalf("Flux input cell passed only %d/%d operational scenarios", pass, total)
	}
}
