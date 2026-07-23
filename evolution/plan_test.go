package evolution

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/storage"
)

func TestPlanRoundTripAndComponent(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()

	if LoadPlan(le, "urn:hdm:apps:x") != nil {
		t.Fatal("missing plan should be nil")
	}
	p := &AppPlan{
		Namespace: "urn:hdm:apps:x", Objective: "move a dot",
		Overview:     "input writes player_x; view draws it there",
		Choreography: []string{"input updates player_x", "view draws player at player_x"},
		Components: []ComponentPlan{
			{Identity: "urn:hdm:apps:x:input", Purpose: "move the player",
				Reads: []string{"HMI input"}, Writes: []string{"player_x"},
				Steps:        []string{"read HMI key", "on 'd', player_x += 5; clamp [0,300]", "write player_x"},
				Interactions: []string{"writes player_x, read by view"}},
		},
	}
	if err := SavePlan(le, "urn:hdm:apps:x", p); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := LoadPlan(le, "urn:hdm:apps:x")
	if got == nil || len(got.Components) != 1 {
		t.Fatalf("roundtrip = %+v", got)
	}
	// Upsert preserves notes; Remove drops.
	got.Components[0].Notes = "refined once"
	got.Upsert(ComponentPlan{Identity: "urn:hdm:apps:x:input", Purpose: "still moves"})
	if got.Component("urn:hdm:apps:x:input").Notes != "refined once" {
		t.Error("Upsert must preserve Notes when the new plan omits them")
	}
	got.Remove("urn:hdm:apps:x:input")
	if got.Component("urn:hdm:apps:x:input") != nil {
		t.Error("Remove must drop the component plan")
	}
}

func TestComponentPlanRenderLeadsWithDesign(t *testing.T) {
	cp := &ComponentPlan{Identity: "urn:hdm:apps:x:input", Purpose: "move the player",
		Reads: []string{"HMI input"}, Writes: []string{"player_x"},
		Steps: []string{"read HMI key", "on 'd', player_x += 5"}}
	out := cp.Render()
	if !strings.Contains(out, "IMPLEMENT THIS DESIGN") {
		t.Fatalf("must frame as the design to implement:\n%s", out)
	}
	if !strings.Contains(out, "1. read HMI key") || !strings.Contains(out, "2. on 'd', player_x += 5") {
		t.Fatalf("must render ordered algorithm steps:\n%s", out)
	}
	if !strings.Contains(out, "Outputs (write these shared fields): player_x") {
		t.Fatalf("must render the write ports:\n%s", out)
	}
}
