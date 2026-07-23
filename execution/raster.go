package execution

// The rasterizer turns a cell's vector draw stream into a small RGB bitmap so the
// multimodal model can SEE the rendered frame — the vision path. The
// content's bounding box is normalized into a fixed output size, so any app's
// coordinate choices produce a usable image.

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// RasterizeFrame paints a 24-byte draw stream into a PNG of outW×outH. Returns nil
// (no image) if the stream is empty. Records are decoded inline (op, a, b, c, d,
// rgba); primitive in op's low byte: 1 rect, 2 line, 3 circle (center a,b radius c),
// anything else treated as a filled rect. rgba is 0xRRGGBBAA.
func RasterizeFrame(stream []byte, outW, outH int) ([]byte, error) {
	type rec struct {
		prim         int
		a, b, c, d   int
		r, g, bl, al uint8
	}
	var recs []rec
	minX, minY := math.MaxInt32, math.MaxInt32
	maxX, maxY := math.MinInt32, math.MinInt32
	span := func(x, y int) {
		if x < minX {
			minX = x
		}
		if y < minY {
			minY = y
		}
		if x > maxX {
			maxX = x
		}
		if y > maxY {
			maxY = y
		}
	}
	for o := 0; o+24 <= len(stream); o += 24 {
		op := binary.LittleEndian.Uint32(stream[o:])
		if op == 0 {
			break
		}
		a := int(int32(binary.LittleEndian.Uint32(stream[o+4:])))
		b := int(int32(binary.LittleEndian.Uint32(stream[o+8:])))
		c := int(int32(binary.LittleEndian.Uint32(stream[o+12:])))
		d := int(int32(binary.LittleEndian.Uint32(stream[o+16:])))
		col := binary.LittleEndian.Uint32(stream[o+20:])
		prim := int(op & 0xff)
		recs = append(recs, rec{
			prim: prim, a: a, b: b, c: c, d: d,
			r: uint8(col >> 24), g: uint8(col >> 16), bl: uint8(col >> 8), al: uint8(col),
		})
		switch prim {
		case 3: // circle: center (a,b), radius c
			span(a-c, b-c)
			span(a+c, b+c)
		default:
			span(a, b)
			span(c, d)
		}
	}
	if len(recs) == 0 || maxX < minX {
		return nil, nil
	}
	// Normalize the content bounding box into the output, preserving aspect.
	bw, bh := maxX-minX, maxY-minY
	if bw <= 0 {
		bw = 1
	}
	if bh <= 0 {
		bh = 1
	}
	scale := math.Min(float64(outW-1)/float64(bw), float64(outH-1)/float64(bh))
	tx := func(x int) int { return int(float64(x-minX) * scale) }
	ty := func(y int) int { return int(float64(y-minY) * scale) }

	img := image.NewRGBA(image.Rect(0, 0, outW, outH))
	// Dark background so an empty/near-empty render is visibly empty to the model.
	for i := range img.Pix {
		img.Pix[i] = 0
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 0xff // opaque
	}
	set := func(x, y int, cc color.RGBA) {
		if x >= 0 && x < outW && y >= 0 && y < outH {
			img.SetRGBA(x, y, cc)
		}
	}
	for _, r := range recs {
		cc := color.RGBA{R: r.r, G: r.g, B: r.bl, A: 0xff}
		switch r.prim {
		case 2: // line
			drawLine(set, tx(r.a), ty(r.b), tx(r.c), ty(r.d), cc)
		case 3: // filled circle
			cx, cy, rad := tx(r.a), ty(r.b), int(float64(r.c)*scale)
			fillCircle(set, cx, cy, rad, cc)
		default: // filled rect (also covers the common background/tile case)
			x0, y0, x1, y1 := tx(r.a), ty(r.b), tx(r.c), ty(r.d)
			if x1 < x0 {
				x0, x1 = x1, x0
			}
			if y1 < y0 {
				y0, y1 = y1, y0
			}
			for y := y0; y <= y1; y++ {
				for x := x0; x <= x1; x++ {
					set(x, y, cc)
				}
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func drawLine(set func(int, int, color.RGBA), x0, y0, x1, y1 int, c color.RGBA) {
	dx, dy := abs(x1-x0), -abs(y1-y0)
	sx, sy := sign(x1-x0), sign(y1-y0)
	err := dx + dy
	for {
		set(x0, y0, c)
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func fillCircle(set func(int, int, color.RGBA), cx, cy, rad int, c color.RGBA) {
	if rad < 1 {
		set(cx, cy, c)
		return
	}
	for y := -rad; y <= rad; y++ {
		for x := -rad; x <= rad; x++ {
			if x*x+y*y <= rad*rad {
				set(cx+x, cy+y, c)
			}
		}
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func sign(x int) int {
	switch {
	case x > 0:
		return 1
	case x < 0:
		return -1
	default:
		return 0
	}
}
