package render

// Box blur used by the tab renderer's drop shadow and permission halo. Kept
// out of tab.go because it is generic image plumbing with no knowledge of tab
// geometry.

import (
	"image"
	"image/color"
)

// boxBlur runs `passes` box-blur passes of the given radius over a
// premultiplied RGBA image.
func boxBlur(src *image.RGBA, r, passes int) *image.RGBA {
	cur := src
	for i := 0; i < passes; i++ {
		cur = boxBlurOnce(cur, r)
	}
	return cur
}

func boxBlurOnce(src *image.RGBA, r int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	// Separable: horizontal pass into tmp, vertical pass into dst. O(n*r)
	// rather than the O(n*r^2) a naive 2D kernel would cost.
	tmp := image.NewRGBA(b)
	dst := image.NewRGBA(b)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sr, sg, sb, sa, n int
			for d := -r; d <= r; d++ {
				px := x + d
				if px < 0 || px >= w {
					continue
				}
				c := src.RGBAAt(px, y)
				sr += int(c.R)
				sg += int(c.G)
				sb += int(c.B)
				sa += int(c.A)
				n++
			}
			tmp.SetRGBA(x, y, color.RGBA{uint8(sr / n), uint8(sg / n), uint8(sb / n), uint8(sa / n)})
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sr, sg, sb, sa, n int
			for d := -r; d <= r; d++ {
				py := y + d
				if py < 0 || py >= h {
					continue
				}
				c := tmp.RGBAAt(x, py)
				sr += int(c.R)
				sg += int(c.G)
				sb += int(c.B)
				sa += int(c.A)
				n++
			}
			dst.SetRGBA(x, y, color.RGBA{uint8(sr / n), uint8(sg / n), uint8(sb / n), uint8(sa / n)})
		}
	}
	return dst
}
