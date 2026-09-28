package subs

import (
	"strings"
	"testing"
)

type qrGolden struct {
	text string
	rows []string
}

// matricesToText renders the module matrix the same way the goldens are
// stored: one string per row, '1' = dark.
func matrixEquals(m [][]bool, g qrGolden) bool {
	if len(m) != len(g.rows) {
		return false
	}
	for y, row := range m {
		var b strings.Builder
		for _, dark := range row {
			if dark {
				b.WriteByte('1')
			} else {
				b.WriteByte('0')
			}
		}
		if b.String() != g.rows[y] {
			return false
		}
	}
	return true
}

// Cross-checked against the reference implementation segno (mask 0, EC-L).
func TestQRMatrixGoldens(t *testing.T) {
	for _, g := range []qrGolden{golden_v1, golden_v2, golden_v3, golden_v4} {
		m, err := QRMatrix(g.text)
		if err != nil {
			t.Fatalf("%q: %v", g.text, err)
		}
		if !matrixEquals(m, g) {
			var firstDiff string
			for y := range m {
				var b strings.Builder
				for _, dark := range m[y] {
					if dark {
						b.WriteByte('1')
					} else {
						b.WriteByte('0')
					}
				}
				if b.String() != g.rows[y] {
					firstDiff = b.String()
					break
				}
			}
			t.Fatalf("%q: matrix mismatch at row: %s", g.text, firstDiff)
		}
	}
}

func TestQRTooLong(t *testing.T) {
	if _, err := QRMatrix(strings.Repeat("x", 200)); err == nil {
		t.Fatal("expected error for oversized payload")
	}
	// max payload (78 bytes for v4) must succeed
	if _, err := QRMatrix(strings.Repeat("x", 78)); err != nil {
		t.Fatalf("78-byte payload must fit v4: %v", err)
	}
}

func TestQRSVGWellFormed(t *testing.T) {
	svg, err := QRSVG("https://x.y/sub/tok")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<svg`, `viewBox`, `<path d="`, `</svg>`} {
		if !strings.Contains(svg, want) {
			t.Fatalf("missing %q in svg", want)
		}
	}
}
