package execution

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/inference"
	"github.com/mdcfrancis/flow/storage"
)

// The self-hosted parser, driven as LIVE cells through the runtime (not a test
// sandbox): raw text → lexer cell → tokens → shift-reduce cell → result. Proves the
// operational RunSelfHostParse path the boot self-check and /parse endpoint use.
func TestRunSelfHostParse(t *testing.T) {
	ctx := context.Background()
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)

	cases := []struct {
		text string
		want int32
	}{
		{"34+", 7},
		{"34+2*", 14},
		{"82/", 4},
		{"5 1 2 + 4 * + 3 -", 14},
		{"9 3 /", 3},
		{"7 2 - 5 *", 25},
	}
	for _, c := range cases {
		got, err := rm.RunSelfHostParse(c.text)
		if err != nil {
			t.Fatalf("RunSelfHostParse(%q): %v", c.text, err)
		}
		if got != c.want {
			t.Errorf("RunSelfHostParse(%q) = %d, want %d", c.text, got, c.want)
		}
	}
}
