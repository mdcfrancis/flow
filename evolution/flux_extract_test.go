package evolution

import "testing"

func TestExtractForthStripsProseAndThink(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ball_x ball_vx + -> ball_x", "ball_x ball_vx + -> ball_x"}, // clean passes through
		{"Now the code: ball_x ball_vx + -> ball_x", "the code: ball_x ball_vx + -> ball_x"}, // leading uppercase stripped (recovers after "Now")
		{"Now ball_x ball_vx + -> ball_x", "ball_x ball_vx + -> ball_x"},                    // single prose word → recovered
		{"```forth\nball_x ball_vx + -> ball_x\n```", "ball_x ball_vx + -> ball_x"},         // fenced authoritative
		{"<think>let me reason</think>ball_x 1 + -> ball_x", "ball_x 1 + -> ball_x"}, // think block removed
		// KNOWN LIMIT: only leading UPPERCASE-led prose is stripped; lowercase prose
		// ("me write this.") survives extraction and is rejected by the compiler/repair
		// loop instead. This targets the observed failures (Now/Let/The/I), not all prose.
		{"Let me write this.", "me write this."},
	}
	for _, c := range cases {
		if got := extractForth(c.in); got != c.want {
			t.Errorf("extractForth(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
