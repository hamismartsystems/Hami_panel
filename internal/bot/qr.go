package bot

import (
	"bytes"
	"image"
	"image/color"
	"image/png"

	"github.com/hamismartsystems/hami_panel/internal/subs"
)

// qrPNG renders a payload as a PNG the way a phone camera likes it:
// generous quiet zone, chunky modules, pure black on pure white. The
// matrix comes from the panel's own encoder, so nothing is sent to a
// third-party QR service.
func qrPNG(payload string) ([]byte, error) {
	m, err := subs.QRMatrix(payload)
	if err != nil {
		return nil, err
	}
	const (
		scale = 10
		quiet = 4
	)
	side := (len(m) + quiet*2) * scale
	img := image.NewGray(image.Rect(0, 0, side, side))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	for y, row := range m {
		for x, dark := range row {
			if !dark {
				continue
			}
			x0, y0 := (x+quiet)*scale, (y+quiet)*scale
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					img.SetGray(x0+dx, y0+dy, color.Gray{Y: 0})
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
