package integration

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/execution"
)

// The /slider endpoint decodes {index,value}, writes the HMI slider register, and
// enforces method + index bounds.
func TestSliderServer(t *testing.T) {
	rm := setupRM(t)
	ss := NewSliderServer(rm)

	// A valid POST latches the register.
	rec := httptest.NewRecorder()
	ss.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/slider", strings.NewReader(`{"index":2,"value":77}`)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("valid POST status = %d, want 204", rec.Code)
	}
	if v, ok := rm.PeekU32(uint32(execution.InSlider0 + 2*4)); !ok || int32(v) != 77 {
		t.Fatalf("slider 2 register = %d (ok=%v), want 77", int32(v), ok)
	}

	// GET is rejected.
	rec = httptest.NewRecorder()
	ss.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/slider", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", rec.Code)
	}

	// An out-of-range index is a 400 (SetSlider rejects it).
	rec = httptest.NewRecorder()
	ss.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/slider", strings.NewReader(`{"index":99,"value":1}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("out-of-range index status = %d, want 400", rec.Code)
	}
}
