package appgen

import (
	"testing"

	"github.com/mdcfrancis/flow/evolution"
)

// The input-source implication: a cell that reads an HMI register only changes its
// output when driven, so its directional scenarios must SEED the inputs it reads
// (else a correct forwarder produces no change and fails). An autonomous cell that
// reads no HMI seeds none.
func TestDirectionalScenariosSeedsHMIForInputSource(t *testing.T) {
	c := &evolution.AppContract{Fields: []evolution.ContractField{
		{Name: "ball_speed", Offset: 0xB0010, Type: "i32", Init: 5},
		{Name: "score", Offset: 0xB0020, Type: "i32", Init: 1},
	}}

	// Input cell: reads hmi_slider0 + hmi_event_seq, writes ball_speed.
	in := directionalScenarios(c, []string{"ball_speed"}, []string{"hmi_slider0", "hmi_event_seq"})
	if len(in) != 1 {
		t.Fatalf("want 1 scenario, got %d", len(in))
	}
	seededSlider, seededSeq := false, false
	for _, s := range in[0].Seed {
		switch s.At {
		case "0x50024": // hmi_slider0
			seededSlider = len(s.U32) == 1 && s.U32[0] != 0
		case "0x50010": // hmi_event_seq
			seededSeq = len(s.U32) == 1 && s.U32[0] != 0
		}
	}
	if !seededSlider || !seededSeq {
		t.Fatalf("input-source scenario must seed its hmi reads non-zero; seeds=%+v", in[0].Seed)
	}

	// Autonomous cell: writes a non-velocity field, reads only state — seeds no HMI.
	auto := directionalScenarios(c, []string{"score"}, []string{"ball_x"})
	if len(auto) != 1 {
		t.Fatalf("autonomous cell should get 1 scenario, got %d", len(auto))
	}
	for _, s := range auto[0].Seed {
		if s.At == "0x50024" {
			t.Error("an autonomous cell must not seed HMI inputs")
		}
	}
}
