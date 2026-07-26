package appgen

import (
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/flux"
)

// A subsystem that declares a scalar capability is seeded as a FORWARDER: it reads
// the capability and writes it to the field it drives. For a pure forwarder this is
// the answer, and it compiles through the capability binding.
func TestNoopFluxForwardsCapability(t *testing.T) {
	layout := flux.Layout{"ball_speed": {Type: flux.TInt, Offset: 0xB0010}}
	sub := Subsystem{
		Identity: "urn:hdm:apps:bounce:input",
		Writes:   []string{"ball_speed"},
		Inputs:   []InputCap{{Kind: "scalar", Name: "speed", Min: 0, Max: 255}},
	}
	src, ok := noopFlux(sub, layout)
	if !ok {
		t.Fatal("noopFlux returned ok=false for a capability subsystem")
	}
	if !strings.Contains(src, "(requires (scalar speed 0 255))") || !strings.Contains(src, "(write (ball_speed speed))") {
		t.Fatalf("expected a capability forwarder, got: %s", src)
	}
	// It compiles once the capability alias is bound (the seed path does this).
	if _, err := flux.Compile("c", src, evolution.BindCapabilities(layout, src)); err != nil {
		t.Fatalf("forwarder did not compile: %v\nsrc: %s", err, src)
	}
}

// capabilityScenarios injects the operator value: seed the resource the capability
// binds to (slider0), assert the driven field equals it.
func TestCapabilityScenariosInject(t *testing.T) {
	c := &evolution.AppContract{Fields: []evolution.ContractField{
		{Name: "ball_speed", Offset: 0xB0010, Type: "i32"},
	}}
	sub := Subsystem{
		Writes: []string{"ball_speed"},
		Inputs: []InputCap{{Kind: "scalar", Name: "speed", Min: 0, Max: 255}},
	}
	scs := capabilityScenarios(sub, c)
	if len(scs) != 1 {
		t.Fatalf("want 1 injection scenario, got %d", len(scs))
	}
	var sv, av uint32
	seeded, asserted := false, false
	for _, s := range scs[0].Seed {
		if s.At == "0x50024" && len(s.U32) == 1 { // hmi_slider0
			seeded, sv = true, s.U32[0]
		}
	}
	for _, r := range scs[0].Expect.Reads {
		if r.At == "0xB0010" && r.Cmp == "eq" && len(r.U32) == 1 { // ball_speed
			asserted, av = true, r.U32[0]
		}
	}
	if !seeded || !asserted {
		t.Fatalf("scenario must seed slider0 and assert ball_speed; got %+v", scs[0])
	}
	if sv != av || sv == 0 {
		t.Fatalf("seeded slider value %d must equal the asserted field value %d and be non-zero", sv, av)
	}
}
