package subs

import (
	"fmt"
	"strings"
)

/*
Minimal QR encoder — enough for the customer status page:
  - byte mode, error-correction level L
  - versions 1–4 (up to 78 data bytes; every valid sub URL fits v4)
  - one block per version at level L ⇒ no interleaving needed
  - fixed mask 0 (the format info tells the reader which mask is used;
    readers handle any valid mask, we keep the encoder small)
*/

// qrSpec is one line of the QR standard's EC table for level L.
type qrSpec struct {
	version, size, dataCW, ecCW int
	align                       []int // alignment pattern centers
}

var qrSpecs = []qrSpec{
	{version: 1, size: 21, dataCW: 19, ecCW: 7, align: nil},
	{version: 2, size: 25, dataCW: 34, ecCW: 10, align: []int{6, 18}},
	{version: 3, size: 29, dataCW: 55, ecCW: 15, align: []int{6, 22}},
	{version: 4, size: 33, dataCW: 80, ecCW: 20, align: []int{6, 26}},
}

// formatBitsL0 is the 15-bit format info for level L + mask 0.
const formatBitsL0 = "111011111000100"

// QRMatrix renders text into a QR module matrix (true = dark module).
// The matrix is size×size; it never errors for text up to 78 bytes.
func QRMatrix(text string) ([][]bool, error) {
	data := []byte(text)
	var spec *qrSpec
	for i := range qrSpecs {
		s := &qrSpecs[i]
		max := s.dataCW - 2 // mode nibble + length byte
		if len(data) <= max {
			spec = s
			break
		}
	}
	if spec == nil {
		return nil, fmt.Errorf("qr: text too long (%d bytes, max %d)", len(data), qrSpecs[3].dataCW-2)
	}

	codewords := qrData(spec, data)
	codewords = append(codewords, rsEncode(codewords, spec.ecCW)...)

	size := spec.size
	modules := make([][]bool, size)
	function := make([][]bool, size)
	for i := range modules {
		modules[i] = make([]bool, size)
		function[i] = make([]bool, size)
	}
	set := func(x, y int, dark bool) {
		if x >= 0 && x < size && y >= 0 && y < size {
			modules[y][x] = dark
			function[y][x] = true
		}
	}

	// function patterns -------------------------------------------------
	finder := func(px, py int) {
		for dy := -1; dy <= 7; dy++ {
			for dx := -1; dx <= 7; dx++ {
				dark := dy >= 0 && dy <= 6 && dx >= 0 && dx <= 6 &&
					(dx == 0 || dx == 6 || dy == 0 || dy == 6 ||
						(dx >= 2 && dx <= 4 && dy >= 2 && dy <= 4))
				set(px+dx, py+dy, dark)
			}
		}
	}
	finder(0, 0)
	finder(size-7, 0)
	finder(0, size-7)

	// timing
	for i := 0; i < size; i++ {
		if !function[6][i] {
			set(i, 6, i%2 == 0)
		}
		if !function[i][6] {
			set(6, i, i%2 == 0)
		}
	}

	// alignment (skip any center already covered by a finder area)
	for _, ay := range spec.align {
		for _, ax := range spec.align {
			if function[ay][ax] {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					set(ax+dx, ay+dy, dx == 0 && dy == 0 || maxAbs(dx, dy) == 2)
				}
			}
		}
	}

	// format info (two copies)
	setFormat(modules, function, size)
	// dark module
	set(8, size-8, true)

	// data placement: zig-zag, right to left columns ---------------------
	bits := make([]bool, 0, len(codewords)*8)
	for _, cw := range codewords {
		for i := 7; i >= 0; i-- {
			bits = append(bits, (cw>>i)&1 == 1)
		}
	}
	bit := 0
	up := true // first pair goes bottom→top
	for right := size - 1; right >= 1; right -= 2 {
		if right == 6 { // skip the vertical timing column
			right = 5
		}
		rows := make([]int, 0, size)
		if up {
			for y := size - 1; y >= 0; y-- {
				rows = append(rows, y)
			}
		} else {
			for y := 0; y < size; y++ {
				rows = append(rows, y)
			}
		}
		up = !up
		for _, y := range rows {
			for dx := 0; dx <= 1; dx++ {
				x := right - dx
				if function[y][x] {
					continue
				}
				b := false
				if bit < len(bits) {
					b = bits[bit]
					bit++
				}
				// mask 0: flip when (row+col) is even
				if (x+y)%2 == 0 {
					b = !b
				}
				modules[y][x] = b
			}
		}
	}
	return modules, nil
}

// qrData lays out mode + length + payload + terminator + padding.
func qrData(spec *qrSpec, data []byte) []byte {
	bits := []bool{}
	push := func(v, n int) {
		for i := n - 1; i >= 0; i-- {
			bits = append(bits, (v>>i)&1 == 1)
		}
	}
	push(0b0100, 4)          // byte mode
	push(len(data), 8)       // length (versions ≤ 9: 8 bits)
	for _, b := range data { // payload
		push(int(b), 8)
	}
	// terminator: up to 4 zero bits, not exceeding capacity
	term := spec.dataCW*8 - len(bits)
	if term > 4 {
		term = 4
	}
	push(0, term)
	// byte-align
	if r := len(bits) % 8; r != 0 {
		push(0, 8-r)
	}
	out := make([]byte, 0, spec.dataCW)
	for i := 0; i < len(bits); i += 8 {
		var b byte
		for j := 0; j < 8; j++ {
			b <<= 1
			if bits[i+j] {
				b |= 1
			}
		}
		out = append(out, b)
	}
	// alternating pad bytes
	padStart := len(out)
	for i := padStart; i < spec.dataCW; i++ {
		if (i-padStart)%2 == 0 {
			out = append(out, 0xEC)
		} else {
			out = append(out, 0x11)
		}
	}
	return out
}

// setFormat writes the fixed level-L/mask-0 format info string.
// Bit i of the value (LSB-first, as placed) corresponds to string
// position 14-i of the MSB-first constant.
func setFormat(modules, function [][]bool, size int) {
	fb := formatBitsL0
	put := func(x, y int, i int) {
		modules[y][x] = fb[len(fb)-1-i] == '1'
		function[y][x] = true
	}
	// copy 1 — around the top-left finder
	for i := 0; i <= 5; i++ {
		put(8, i, i)
	}
	put(8, 7, 6)
	put(8, 8, 7)
	put(7, 8, 8)
	for i := 9; i <= 14; i++ {
		put(14-i, 8, i)
	}
	// copy 2 — split along the other two finders
	for i := 0; i <= 7; i++ {
		put(size-1-i, 8, i)
	}
	for i := 8; i <= 14; i++ {
		put(8, size-15+i, i)
	}
}

/* ── Reed-Solomon over GF(2^8), primitive 0x11D ─────────────────────── */

var gfExp, gfLog = func() ([512]byte, [256]byte) {
	var exp [512]byte
	var log [256]byte
	x := 1
	for i := 0; i < 255; i++ {
		exp[i] = byte(x)
		log[x] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11D
		}
	}
	for i := 255; i < 512; i++ {
		exp[i] = exp[i-255]
	}
	return exp, log
}()

func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[int(gfLog[a])+int(gfLog[b])]
}

func rsGenPoly(deg int) []byte {
	poly := []byte{1}
	for i := 0; i < deg; i++ {
		next := make([]byte, len(poly)+1)
		for j, coef := range poly {
			next[j] ^= coef                    // x * poly (descending coefficients)
			next[j+1] ^= gfMul(coef, gfExp[i]) // − α^i * poly  (⊕ in GF(2))
		}
		poly = next
	}
	return poly
}

// rsEncode is textbook message/polynomial remainder.
func rsEncode(data []byte, ecLen int) []byte {
	gen := rsGenPoly(ecLen)
	res := make([]byte, len(data)+ecLen)
	copy(res, data)
	for i := 0; i < len(data); i++ {
		coef := res[i]
		if coef == 0 {
			continue
		}
		for j := 0; j < len(gen); j++ {
			res[i+j] ^= gfMul(gen[j], coef)
		}
	}
	return res[len(data):]
}

func maxAbs(a, b int) int {
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	if a > b {
		return a
	}
	return b
}

/* ── SVG ────────────────────────────────────────────────────────────── */

// QRSVG renders the matrix as a compact SVG (white background + quiet zone).
func QRSVG(text string) (string, error) {
	m, err := QRMatrix(text)
	if err != nil {
		return "", err
	}
	n := len(m)
	quiet := 4
	total := n + 2*quiet
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d">`, total, total)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/><path d="`, total, total)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if m[y][x] {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+quiet, y+quiet)
			}
		}
	}
	b.WriteString(`" fill="#000"/></svg>`)
	return b.String(), nil
}
