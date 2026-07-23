package execution

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"testing"
)

func rec(op, a, b, c, d int, rgba uint32) []byte {
	buf := make([]byte, 24)
	binary.LittleEndian.PutUint32(buf[0:], uint32(op))
	binary.LittleEndian.PutUint32(buf[4:], uint32(int32(a)))
	binary.LittleEndian.PutUint32(buf[8:], uint32(int32(b)))
	binary.LittleEndian.PutUint32(buf[12:], uint32(int32(c)))
	binary.LittleEndian.PutUint32(buf[16:], uint32(int32(d)))
	binary.LittleEndian.PutUint32(buf[20:], rgba)
	return buf
}

func TestRasterizeFrameProducesPNG(t *testing.T) {
	// a background rect + a small colored rect
	var stream []byte
	stream = append(stream, rec(1, 0, 0, 100, 100, 0x101010ff)...) // dark bg
	stream = append(stream, rec(1, 40, 40, 60, 60, 0xff0000ff)...) // red square
	out, err := RasterizeFrame(stream, 96, 96)
	if err != nil {
		t.Fatalf("rasterize: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected a PNG, got none")
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode PNG: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != 96 || b.Dy() != 96 {
		t.Errorf("size = %dx%d, want 96x96", b.Dx(), b.Dy())
	}
	// somewhere near the center there should be red pixels (the inner square)
	red := 0
	for y := 0; y < 96; y++ {
		for x := 0; x < 96; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r>>8 > 200 && g>>8 < 60 && bl>>8 < 60 {
				red++
			}
		}
	}
	if red == 0 {
		t.Error("expected red pixels from the inner square")
	}
}

func TestRasterizeEmptyStream(t *testing.T) {
	out, err := RasterizeFrame(nil, 64, 64)
	if err != nil || out != nil {
		t.Errorf("empty stream should yield (nil,nil), got %d bytes err=%v", len(out), err)
	}
}
