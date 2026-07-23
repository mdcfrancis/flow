package tapes

import (
	"bytes"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	in := &TransactionFrame{
		MonadicTimestampNS: 0x0123456789ABCDEF,
		InputPayload:       []byte("annealing candidate packet"),
	}
	for i := range in.EntropySeed {
		in.EntropySeed[i] = byte(i + 1)
	}
	for i := range in.ExpectedOutputHash {
		in.ExpectedOutputHash[i] = byte(0xA0 + i)
	}
	for i := range in.ExpectedStateHash {
		in.ExpectedStateHash[i] = byte(0x40 + i)
	}

	blob := in.Marshal()
	if len(blob) != HeaderSize+len(in.InputPayload) {
		t.Fatalf("marshaled length = %d, want %d", len(blob), HeaderSize+len(in.InputPayload))
	}
	// Magic marker is big-endian "HDMT".
	if !bytes.Equal(blob[:4], []byte("HDMT")) {
		t.Fatalf("magic marker bytes = % x, want HDMT", blob[:4])
	}

	out, err := Unmarshal(blob)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.MonadicTimestampNS != in.MonadicTimestampNS ||
		out.EntropySeed != in.EntropySeed ||
		out.ExpectedOutputHash != in.ExpectedOutputHash ||
		out.ExpectedStateHash != in.ExpectedStateHash ||
		!bytes.Equal(out.InputPayload, in.InputPayload) {
		t.Fatalf("round-trip mismatch:\n in=%+v\nout=%+v", in, out)
	}
}

func TestUnmarshalRejectsBadMagic(t *testing.T) {
	blob := (&TransactionFrame{InputPayload: []byte("x")}).Marshal()
	blob[0] = 0x00 // corrupt the magic marker
	if _, err := Unmarshal(blob); err == nil {
		t.Fatal("expected bad-magic error")
	}
}

func TestUnmarshalRejectsSizeMismatch(t *testing.T) {
	blob := (&TransactionFrame{InputPayload: []byte("hello")}).Marshal()
	if _, err := Unmarshal(blob[:len(blob)-2]); err == nil {
		t.Fatal("expected payload-size mismatch error")
	}
	if _, err := Unmarshal(blob[:10]); err == nil {
		t.Fatal("expected too-short error")
	}
}
