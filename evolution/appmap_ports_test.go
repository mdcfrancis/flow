package evolution

import (
	"strings"
	"testing"
)

// The map shows each component's INTENDED interface, and the data flow spans the
// declared ports so intended wiring counts — a field a renderer is DECLARED to
// read is no longer a gap even before it is verified.
func TestRenderUsesDeclaredPorts(t *testing.T) {
	m := &AppMap{
		Namespace: "urn:hdm:apps:si",
		Components: []ComponentMap{
			{Identity: "urn:hdm:apps:si:input", Status: "converged",
				DeclaredReads: []string{"HMI input"}, DeclaredWrites: []string{"player_x"},
				Writes: []string{"player_x"}},
			{Identity: "urn:hdm:apps:si:renderer", Entry: "render-frame", Status: "building",
				DeclaredReads: []string{"player_x"}}, // declared reader, not yet verified
		},
	}
	out := m.Render()
	if !strings.Contains(out, "interface: reads {HMI input} writes {player_x}") {
		t.Fatalf("must render the declared interface:\n%s", out)
	}
	// player_x is written by input and DECLARED-read by renderer → not a gap.
	if !strings.Contains(out, "player_x: written by input; read by renderer") {
		t.Fatalf("declared reader must count in the data flow:\n%s", out)
	}
	if strings.Contains(out, "GAP: player_x") {
		t.Fatalf("a declared reader should close the gap:\n%s", out)
	}
}

// With no consumer at all (declared or verified), the gap is still flagged.
func TestRenderFlagsGapWithNoConsumer(t *testing.T) {
	m := &AppMap{Namespace: "urn:hdm:apps:si", Components: []ComponentMap{
		{Identity: "urn:hdm:apps:si:input", DeclaredWrites: []string{"player_x"}, Writes: []string{"player_x"}},
	}}
	if !strings.Contains(m.Render(), "GAP: player_x") {
		t.Fatalf("an unconsumed field must still be a gap:\n%s", m.Render())
	}
}
