package tray

import "math"

// dbusPixmap matches org.kde.StatusNotifierItem IconPixmap a(iiay).
// Data is network byte order (big-endian) ARGB32.
type dbusPixmap struct {
	Width  int32
	Height int32
	Data   []byte
}

type rgbaColor struct {
	r, g, b, a float64
}

func distCapsule(px, py, x1, y1, x2, y2, radius float64) float64 {
	dx := x2 - x1
	dy := y2 - y1
	lenSq := dx*dx + dy*dy
	t := 0.0
	if lenSq > 0 {
		t = ((px-x1)*dx + (py-y1)*dy) / lenSq
		if t < 0 {
			t = 0
		} else if t > 1 {
			t = 1
		}
	}
	cx := x1 + t*dx
	cy := y1 + t*dy
	return math.Hypot(px-cx, py-cy) - radius
}

// distSparkle approximates a crisp 4-point AI star centered at (cx, cy) with outer radius r.
func distSparkle(px, py, cx, cy, r float64) float64 {
	ax := math.Abs(px - cx)
	ay := math.Abs(py - cy)
	// Superellipse |x/r|^0.55 + |y/r|^0.55 <= 1 gives a sleek 4-point sparkle
	v := math.Pow(ax/r, 0.55) + math.Pow(ay/r, 0.55)
	return (v - 1.0) * (r * 0.45)
}

func blendOver(dst rgbaColor, src rgbaColor) rgbaColor {
	outA := src.a + dst.a*(1.0-src.a)
	if outA <= 0 {
		return rgbaColor{}
	}
	return rgbaColor{
		r: (src.r*src.a + dst.r*dst.a*(1.0-src.a)) / outA,
		g: (src.g*src.a + dst.g*dst.a*(1.0-src.a)) / outA,
		b: (src.b*src.a + dst.b*dst.a*(1.0-src.a)) / outA,
		a: outA,
	}
}

func aaCoverage(d float64) float64 {
	c := 0.5 - d
	if c < 0 {
		return 0
	}
	if c > 1 {
		return 1
	}
	return c
}

func renderTrayPixmap(active bool) []dbusPixmap {
	const size = 48
	data := make([]byte, size*size*4)

	white := rgbaColor{244, 244, 245, 1}
	green := rgbaColor{52, 211, 153, 1}
	muted := rgbaColor{161, 161, 170, 0.58}
	coral := rgbaColor{248, 113, 113, 1}

	type bar struct {
		x, y1, y2, r float64
		col          rgbaColor
	}

	bars := []bar{
		{x: 10.25, y1: 19.25, y2: 28.75, r: 2.25, col: white},
		{x: 18.25, y1: 13.25, y2: 34.75, r: 2.25, col: white},
		{x: 26.25, y1: 16.25, y2: 31.75, r: 2.25, col: white},
		{x: 34.25, y1: 22.25, y2: 29.75, r: 2.25, col: green},
	}
	if !active {
		for i := range bars {
			bars[i].col = muted
		}
	}

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			px := float64(x) + 0.5
			py := float64(y) + 0.5

			var pixel rgbaColor
			for _, b := range bars {
				d := distCapsule(px, py, b.x, b.y1, b.x, b.y2, b.r)
				if cov := aaCoverage(d); cov > 0 {
					c := b.col
					c.a *= cov
					pixel = blendOver(pixel, c)
				}
			}

			if active {
				d := distSparkle(px, py, 36.0, 12.0, 7.0)
				if cov := aaCoverage(d); cov > 0 {
					c := green
					c.a *= cov
					pixel = blendOver(pixel, c)
				}
			} else {
				d := distCapsule(px, py, 9.5, 38.5, 38.5, 9.5, 1.8)
				if cov := aaCoverage(d); cov > 0 {
					c := coral
					c.a *= cov
					pixel = blendOver(pixel, c)
				}
			}

			off := (y*size + x) * 4
			a := uint8(math.Round(pixel.a * 255))
			// Network byte order ARGB32 (non-premultiplied or premultiplied)
			data[off+0] = a
			data[off+1] = uint8(math.Round(pixel.r * pixel.a))
			data[off+2] = uint8(math.Round(pixel.g * pixel.a))
			data[off+3] = uint8(math.Round(pixel.b * pixel.a))
		}
	}

	return []dbusPixmap{{
		Width:  size,
		Height: size,
		Data:   data,
	}}
}
