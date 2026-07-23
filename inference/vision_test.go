package inference

import (
	"context"
	"encoding/base64"
	"io"
	"strings"
	"testing"
)

// TestOpenAIVisionRequest proves the local (OpenAI-compatible) vision request carries
// the question as a text part and each frame as a base64 PNG image_url data URI — the
// shape verified to work against the omlx Gemma-4 server.
func TestOpenAIVisionRequest(t *testing.T) {
	c := NewLocalModelClient("http://localhost:8000", "gemma-vision")
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a}
	req, err := c.openaiVisionRequest(context.Background(), "you are a judge", "what color?", [][]byte{png})
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if req.URL.Path != "/v1/chat/completions" {
		t.Fatalf("path = %q, want /v1/chat/completions", req.URL.Path)
	}
	body, _ := io.ReadAll(req.Body)
	s := string(body)
	for _, want := range []string{
		`"image_url"`,
		"data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
		`"what color?"`,
		`"gemma-vision"`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("request body missing %q\nbody: %s", want, s)
		}
	}
}

// TestInvokeVisionNoImages returns the sentinel rather than hitting the network.
func TestInvokeVisionNoImages(t *testing.T) {
	c := NewLocalModelClient("http://localhost:8000", "m")
	if _, err := c.InvokeVision(context.Background(), "s", "q", nil); err != ErrVisionUnsupported {
		t.Fatalf("no-images should return ErrVisionUnsupported, got %v", err)
	}
}
