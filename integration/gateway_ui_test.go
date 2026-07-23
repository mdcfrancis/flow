package integration

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/execution"
	"github.com/mdcfrancis/flow/storage"
)

const (
	routerURN = "urn:hdm:cells:network:router"
	uiURN     = "urn:hdm:ui:checkout"
	routerWAT = `(module
	  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
	  (func (export "route-packet") (param $ptr i32) (param $len i32) (result i32) local.get $len))`
	uiWAT = `(module
	  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
	  (func (export "render-frame") (param $base i32) (param $cap i32) (result i32)
	    local.get $base i32.const 1146307408 i32.store
	    local.get $base i32.const 4 i32.add i32.const 42 i32.store
	    i32.const 8))`
)

func setupRM(t *testing.T) *execution.RuntimeManager {
	t.Helper()
	ctx := context.Background()
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	t.Cleanup(func() { le.Close() })
	rm, err := execution.NewRuntimeManager(ctx, le, nil)
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	t.Cleanup(func() { rm.Close(ctx) })
	cs := compiler.NewCompilerService()
	for urn, wat := range map[string]string{routerURN: routerWAT, uiURN: uiWAT} {
		art, err := cs.CompileGenotype(wat)
		if err != nil || !art.SyntaxPassed {
			t.Fatalf("compile %s: %v", urn, err)
		}
		if err := rm.LoadCell(urn, art.Bytecode); err != nil {
			t.Fatalf("load %s: %v", urn, err)
		}
	}
	return rm
}

func TestGatewayRoutesPacket(t *testing.T) {
	gw := NewProductionGateway(setupRM(t), routerURN)

	body := []byte("hello-edge-packet") // 17 bytes
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/ingress", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// The router echoes the packet length as its kernel signal.
	if !strings.Contains(rec.Body.String(), "Kernel Signal: 17") {
		t.Fatalf("unexpected response: %q", rec.Body.String())
	}

	// Non-POST is rejected.
	rec2 := httptest.NewRecorder()
	gw.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/ingress", nil))
	if rec2.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", rec2.Code)
	}
}

func TestCanvasServerPageAndFrame(t *testing.T) {
	srv := NewCanvasServer(NewCanvasUiEngine(setupRM(t)), uiURN)

	// The page is self-contained HTML with a <canvas>.
	rec := httptest.NewRecorder()
	srv.Page(rec, httptest.NewRequest(http.MethodGet, "/canvas", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<canvas") {
		t.Fatalf("canvas page missing <canvas>: code=%d", rec.Code)
	}

	// The frame endpoint returns the raw vector stream (8 bytes for the test UI).
	rec2 := httptest.NewRecorder()
	srv.Frame(rec2, httptest.NewRequest(http.MethodGet, "/canvas/frame", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("frame code = %d", rec2.Code)
	}
	if ct := rec2.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("frame content-type = %q", ct)
	}
	if rec2.Body.Len() != 8 {
		t.Fatalf("frame = %d bytes, want 8 (test UI cell)", rec2.Body.Len())
	}
}

func TestCanvasExtractsFrame(t *testing.T) {
	cue := NewCanvasUiEngine(setupRM(t))
	frame, err := cue.ExtractActiveFrame(uiURN)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(frame) != 8 {
		t.Fatalf("frame = %d bytes, want 8", len(frame))
	}
	if binary.LittleEndian.Uint32(frame[:4]) != 1146307408 {
		t.Fatalf("marker word = %d, want 1146307408", binary.LittleEndian.Uint32(frame[:4]))
	}
	if binary.LittleEndian.Uint32(frame[4:]) != 42 {
		t.Fatalf("primitive = %d, want 42", binary.LittleEndian.Uint32(frame[4:]))
	}
}
