package api

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"sync"
)

var (
	iconOnce sync.Once
	iconPNG  []byte
)

// touchIcon draws the 180x180 Home Screen icon iOS asks for (it does not use
// SVG icons): ebony ground, five ivory keys and three black ones.
func touchIcon() []byte {
	iconOnce.Do(func() {
		const n = 180
		img := image.NewRGBA(image.Rect(0, 0, n, n))
		fill := func(x0, y0, x1, y1 int, c color.RGBA) {
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					img.SetRGBA(x, y, c)
				}
			}
		}
		ebony := color.RGBA{0x1c, 0x19, 0x17, 0xff}
		ivory := color.RGBA{0xf4, 0xee, 0xe1, 0xff}
		gold := color.RGBA{0xc8, 0x9b, 0x4a, 0xff}
		fill(0, 0, n, n, ebony)
		fill(24, 36, n-24, 40, gold) // the key slip
		left, top, w, h, gap := 26, 46, 24, 100, 2
		for i := 0; i < 5; i++ {
			x := left + i*(w+gap)
			fill(x, top, x+w, top+h, ivory)
		}
		for _, i := range []int{1, 2, 4} { // black keys sit between whites
			x := left + i*(w+gap) - 8
			fill(x, top, x+15, top+60, ebony)
		}
		var buf bytes.Buffer
		png.Encode(&buf, img)
		iconPNG = buf.Bytes()
	})
	return iconPNG
}
