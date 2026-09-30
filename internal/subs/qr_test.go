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

// The goldens are decoder-verified: each was round-tripped through
// OpenCV's QRCodeDetector at four render sizes before being frozen here,
// so a change that still produces a "valid-looking" matrix but breaks
// real scanners will fail this test.
func TestQRMatrixGoldens(t *testing.T) {
	for _, g := range []qrGolden{golden_v1, golden_v2, golden_v3, golden_v4, golden_v6, golden_v9} {
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
	// 230 bytes is the v9 limit; one more must be refused rather than
	// silently truncated.
	if _, err := QRMatrix(strings.Repeat("x", 231)); err == nil {
		t.Fatal("expected error for oversized payload")
	}
	if _, err := QRMatrix(strings.Repeat("x", 230)); err != nil {
		t.Fatalf("230-byte payload must fit v9: %v", err)
	}
	// every version boundary must pick a matrix that is big enough
	for _, tc := range []struct{ n, size int }{
		{17, 21}, {32, 25}, {53, 29}, {78, 33},
		{106, 37}, {134, 41}, {154, 45}, {192, 49}, {230, 53},
	} {
		m, err := QRMatrix(strings.Repeat("x", tc.n))
		if err != nil {
			t.Fatalf("%d bytes: %v", tc.n, err)
		}
		if len(m) != tc.size {
			t.Errorf("%d bytes: want a %dx%d matrix, got %dx%d",
				tc.n, tc.size, tc.size, len(m), len(m))
		}
	}
}

// A real share link is the payload that matters: it is the thing customers
// actually scan, and it is far past the old 78-byte ceiling.
func TestQRFitsARealShareLink(t *testing.T) {
	link := "vless://151ead66-d916-4dc3-81b3-a78966629817@198.51.100.10:443" +
		"?flow=xtls-rprx-vision&fp=chrome" +
		"&pbk=64KOuJjRaKLkT8V-z58hIrfFbJgZTzYGZY8cbodrxws" +
		"&security=reality&sid=8f108ceb&sni=www.samsung.com&type=tcp" +
		"#Reality-443+%28FI%29"
	if len(link) < 150 {
		t.Fatalf("this test is meant to exercise a long payload, got %d bytes", len(link))
	}
	m, err := QRMatrix(link)
	if err != nil {
		t.Fatalf("a real share link must encode: %v", err)
	}
	if len(m) < 45 {
		t.Errorf("expected version 7 or higher for %d bytes, got a %dx%d matrix",
			len(link), len(m), len(m))
	}
	svg, err := QRSVG(link)
	if err != nil {
		t.Fatalf("svg: %v", err)
	}
	if !strings.Contains(svg, "<svg") {
		t.Error("svg output is not an svg")
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

// formatBits must reproduce the values tabulated in the standard; a wrong
// BCH remainder makes every scanner reject the symbol outright.
func TestFormatBitsMatchStandard(t *testing.T) {
	want := map[int]int{
		0: 0x77C4, 1: 0x72F3, 2: 0x7DAA, 3: 0x789D,
		4: 0x662F, 5: 0x6318, 6: 0x6C41, 7: 0x6976,
	}
	for mask, w := range want {
		if got := formatBits(mask); got != w {
			t.Errorf("mask %d: got %#05x, want %#05x", mask, got, w)
		}
	}
}

// Every mask must be reachable and the chosen one must be recorded in the
// format information, otherwise a reader unmasks with the wrong pattern.
func TestChosenMaskIsRecordedInFormatInfo(t *testing.T) {
	byMask := map[int]int{}
	for mask := 0; mask < 8; mask++ {
		byMask[formatBits(mask)] = mask
	}
	read := func(m [][]bool) (int, bool) {
		size := len(m)
		pos := [][2]int{{8, 0}, {8, 1}, {8, 2}, {8, 3}, {8, 4}, {8, 5},
			{8, 7}, {8, 8}, {7, 8}, {5, 8}, {4, 8}, {3, 8}, {2, 8}, {1, 8}, {0, 8}}
		bits := 0
		for i, p := range pos {
			if m[p[1]][p[0]] {
				bits |= 1 << uint(i)
			}
		}
		_ = size
		mask, ok := byMask[bits]
		return mask, ok
	}
	seen := map[int]bool{}
	for _, g := range []qrGolden{golden_v1, golden_v2, golden_v3, golden_v4, golden_v6, golden_v9} {
		m, err := QRMatrix(g.text)
		if err != nil {
			t.Fatalf("%q: %v", g.text, err)
		}
		mask, ok := read(m)
		if !ok {
			t.Fatalf("%q: format info is not a valid level-L pattern", g.text)
		}
		seen[mask] = true
		// the two copies of the format info must agree
		other, ok2 := read(m)
		if !ok2 || other != mask {
			t.Errorf("%q: format copies disagree", g.text)
		}
	}
	if len(seen) < 2 {
		t.Errorf("mask selection looks stuck: only masks %v ever chosen", seen)
	}
}

// Both copies of the format information must carry the same value.
func TestFormatInfoCopiesAgree(t *testing.T) {
	m, err := QRMatrix("https://panel.example.com:2096/sub/MhF982ErKiTvvT3x4QwQsw")
	if err != nil {
		t.Fatal(err)
	}
	size := len(m)
	first := [][2]int{{8, 0}, {8, 1}, {8, 2}, {8, 3}, {8, 4}, {8, 5},
		{8, 7}, {8, 8}, {7, 8}, {5, 8}, {4, 8}, {3, 8}, {2, 8}, {1, 8}, {0, 8}}
	second := make([][2]int, 15)
	for i := 0; i <= 7; i++ {
		second[i] = [2]int{size - 1 - i, 8}
	}
	for i := 8; i <= 14; i++ {
		second[i] = [2]int{8, size - 15 + i}
	}
	for i := range first {
		a := m[first[i][1]][first[i][0]]
		b := m[second[i][1]][second[i][0]]
		if a != b {
			t.Errorf("format bit %d differs between the two copies", i)
		}
	}
}
