package subs

import (
	"fmt"
	"strings"
)

/*
Minimal QR encoder — enough for the customer status page:
  - byte mode, error-correction level L
  - versions 1–9 (up to 230 data bytes; a sub URL fits v4, a full
    vless/Reality share link fits v8)
  - versions 1–5 are a single RS block; 6–9 are two equal blocks, so the
    codewords are interleaved the way the standard requires
  - v10 is the first version whose blocks have unequal sizes, hence the cut
  - all eight masks are tried and the one with the lowest penalty score
    wins, exactly as the standard prescribes; a fixed mask produces codes
    that real scanners refuse on longer payloads
*/

// qrSpec is one line of the QR standard's error-correction table for
// level L. From version 10 the blocks are no longer all the same size:
// the codewords are split into two groups, the second holding one more
// data codeword per block than the first.
type qrSpec struct {
	version    int
	ecPerBlock int
	g1Blocks   int // blocks in the first group
	g1Data     int // data codewords in each of them
	g2Blocks   int // blocks in the second group, often zero
	g2Data     int // always g1Data + 1 when present
}

func (q qrSpec) size() int   { return q.version*4 + 17 }
func (q qrSpec) blocks() int { return q.g1Blocks + q.g2Blocks }
func (q qrSpec) dataCW() int { return q.g1Blocks*q.g1Data + q.g2Blocks*q.g2Data }
func (q qrSpec) ecCW() int   { return q.blocks() * q.ecPerBlock }

// countBits is the width of the character count field in byte mode. It
// widens at version 10, which costs a whole extra byte of payload.
func (q qrSpec) countBits() int {
	if q.version >= 10 {
		return 16
	}
	return 8
}

// maxBytes is how much payload actually fits: the data capacity less the
// mode nibble and the character count.
func (q qrSpec) maxBytes() int {
	return (q.dataCW()*8 - 4 - q.countBits()) / 8
}

// Versions 1 to 15 at level L. Fifteen holds 521 bytes of payload, which
// is far more than any share link, and stopping there keeps the table
// short enough to check by eye against the standard.
var qrSpecs = []qrSpec{
	{version: 1, ecPerBlock: 7, g1Blocks: 1, g1Data: 19},
	{version: 2, ecPerBlock: 10, g1Blocks: 1, g1Data: 34},
	{version: 3, ecPerBlock: 15, g1Blocks: 1, g1Data: 55},
	{version: 4, ecPerBlock: 20, g1Blocks: 1, g1Data: 80},
	{version: 5, ecPerBlock: 26, g1Blocks: 1, g1Data: 108},
	{version: 6, ecPerBlock: 18, g1Blocks: 2, g1Data: 68},
	{version: 7, ecPerBlock: 20, g1Blocks: 2, g1Data: 78},
	{version: 8, ecPerBlock: 24, g1Blocks: 2, g1Data: 97},
	{version: 9, ecPerBlock: 30, g1Blocks: 2, g1Data: 116},
	{version: 10, ecPerBlock: 18, g1Blocks: 2, g1Data: 68, g2Blocks: 2, g2Data: 69},
	{version: 11, ecPerBlock: 20, g1Blocks: 4, g1Data: 81},
	{version: 12, ecPerBlock: 24, g1Blocks: 2, g1Data: 92, g2Blocks: 2, g2Data: 93},
	{version: 13, ecPerBlock: 26, g1Blocks: 4, g1Data: 107},
	{version: 14, ecPerBlock: 30, g1Blocks: 3, g1Data: 115, g2Blocks: 1, g2Data: 116},
	{version: 15, ecPerBlock: 22, g1Blocks: 5, g1Data: 87, g2Blocks: 1, g2Data: 88},
}

// alignCenters returns the alignment pattern coordinates for a version.
// Computing them beats tabulating forty rows by hand; the result is
// checked against the standard's table in the tests.
func alignCenters(version int) []int {
	if version == 1 {
		return nil
	}
	size := version*4 + 17
	count := version/7 + 2
	step := 26
	if version != 32 {
		step = ((version*4 + count*2 + 1) / (count*2 - 2)) * 2
	}
	out := make([]int, count)
	out[0] = 6
	for i, pos := count-1, size-7; i >= 1; i, pos = i-1, pos-step {
		out[i] = pos
	}
	return out
}

// versionBitsFor computes the 18-bit version information word: six bits
// of version and a BCH(18,6) remainder.
func versionBitsFor(version int) int {
	rem := version
	for i := 0; i < 12; i++ {
		rem = (rem << 1) ^ ((rem >> 11) * 0x1F25)
	}
	return version<<12 | rem
}

// formatBits returns the 15-bit format information for error-correction
// level L and the given mask: five data bits (level L = 01, then the mask)
// extended by a BCH(15,5) remainder and XOR-ed with the standard mask.
func formatBits(mask int) int {
	data := 0x01<<3 | mask
	rem := data
	for i := 0; i < 10; i++ {
		rem = (rem << 1) ^ ((rem >> 9) * 0x537)
	}
	return ((data<<10 | rem) ^ 0x5412) & 0x7FFF
}

// maskFns are the eight standard data-mask conditions; a true result
// inverts the module at that position.
var maskFns = [8]func(x, y int) bool{
	func(x, y int) bool { return (x+y)%2 == 0 },
	func(x, y int) bool { return y%2 == 0 },
	func(x, y int) bool { return x%3 == 0 },
	func(x, y int) bool { return (x+y)%3 == 0 },
	func(x, y int) bool { return (y/2+x/3)%2 == 0 },
	func(x, y int) bool { return x*y%2+x*y%3 == 0 },
	func(x, y int) bool { return (x*y%2+x*y%3)%2 == 0 },
	func(x, y int) bool { return ((x+y)%2+x*y%3)%2 == 0 },
}

// qrSkeleton lays out everything that does not depend on the mask: the
// function patterns and the unmasked data.
func qrSkeleton(spec *qrSpec, data []byte) (modules, function [][]bool, size int) {
	codewords := qrInterleave(spec, qrData(spec, data))
	size = spec.size()
	modules = make([][]bool, size)
	function = make([][]bool, size)
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

	// Alignment patterns sit at every pairing of the version's centers
	// except the three that would land on a finder. Note that from version
	// 7 on some centers legitimately fall on a timing pattern (e.g. 22,6),
	// so the finder corners must be excluded explicitly rather than by
	// testing whether the module is already a function module.
	last := spec.size() - 7
	for _, ay := range alignCenters(spec.version) {
		for _, ax := range alignCenters(spec.version) {
			onFinder := (ax == 6 && ay == 6) ||
				(ax == 6 && ay == last) ||
				(ax == last && ay == 6)
			if onFinder {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					set(ax+dx, ay+dy, dx == 0 && dy == 0 || maxAbs(dx, dy) == 2)
				}
			}
		}
	}

	// version information (versions 7+): two 6×3 copies, next to the
	// top-right and bottom-left finders
	if spec.version >= 7 {
		vb := versionBitsFor(spec.version)
		for i := 0; i < 18; i++ {
			dark := (vb>>uint(i))&1 == 1
			a := size - 11 + i%3
			b := i / 3
			set(a, b, dark) // top-right
			set(b, a, dark) // bottom-left
		}
	}

	// Reserve the two format-info areas. The real bits depend on the mask,
	// so they are written again once the best mask has been chosen.
	setFormat(modules, function, size, 0)
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
				modules[y][x] = b
			}
		}
	}
	return modules, function, size
}

func QRMatrix(text string) ([][]bool, error) {
	data := []byte(text)
	var spec *qrSpec
	for i := range qrSpecs {
		s := &qrSpecs[i]
		if len(data) <= s.maxBytes() {
			spec = s
			break
		}
	}
	if spec == nil {
		return nil, fmt.Errorf("qr: text too long (%d bytes, max %d)",
			len(data), qrSpecs[len(qrSpecs)-1].maxBytes())
	}
	modules, function, size := qrSkeleton(spec, data)

	// Pick the mask that scores best. The data is still unmasked, so each
	// candidate is the same grid with one mask and its format info applied.
	var best [][]bool
	bestScore := -1
	for mask := 0; mask < 8; mask++ {
		cand := applyMask(modules, function, size, mask)
		if sc := qrPenalty(cand); bestScore < 0 || sc < bestScore {
			best, bestScore = cand, sc
		}
	}
	return best, nil
}

// qrInterleave splits the data into the version's RS blocks, appends the
// error-correction codewords of each block, and interleaves everything the
// way the standard prescribes: data codeword i of every block in turn, then
// EC codeword i of every block in turn. A single-block version is just the
// data followed by its EC codewords.
func qrInterleave(spec *qrSpec, data []byte) []byte {
	nb := spec.blocks()
	if nb == 1 {
		return append(data, rsEncode(data, spec.ecPerBlock)...)
	}

	dataBlocks := make([][]byte, 0, nb)
	ecBlocks := make([][]byte, 0, nb)
	off := 0
	add := func(count, length int) {
		for i := 0; i < count; i++ {
			blk := data[off : off+length]
			off += length
			dataBlocks = append(dataBlocks, blk)
			ecBlocks = append(ecBlocks, rsEncode(blk, spec.ecPerBlock))
		}
	}
	add(spec.g1Blocks, spec.g1Data)
	add(spec.g2Blocks, spec.g2Data)

	out := make([]byte, 0, spec.dataCW()+spec.ecCW())
	// Data codeword i of every block in turn. The shorter blocks of the
	// first group simply have nothing to contribute on the last pass.
	longest := spec.g1Data
	if spec.g2Data > longest {
		longest = spec.g2Data
	}
	for i := 0; i < longest; i++ {
		for _, blk := range dataBlocks {
			if i < len(blk) {
				out = append(out, blk[i])
			}
		}
	}
	// Every block has the same number of EC codewords.
	for i := 0; i < spec.ecPerBlock; i++ {
		for _, blk := range ecBlocks {
			out = append(out, blk[i])
		}
	}
	return out
}

// qrData lays out mode + length + payload + terminator + padding.
func qrData(spec *qrSpec, data []byte) []byte {
	bits := []bool{}
	push := func(v, n int) {
		for i := n - 1; i >= 0; i-- {
			bits = append(bits, (v>>i)&1 == 1)
		}
	}
	push(0b0100, 4) // byte mode
	// The character count field widens at version 10. Getting this wrong
	// produces a symbol that encodes cleanly and decodes as nonsense.
	push(len(data), spec.countBits())
	for _, b := range data { // payload
		push(int(b), 8)
	}
	// terminator: up to 4 zero bits, not exceeding capacity
	term := spec.dataCW()*8 - len(bits)
	if term > 4 {
		term = 4
	}
	push(0, term)
	// byte-align
	if r := len(bits) % 8; r != 0 {
		push(0, 8-r)
	}
	out := make([]byte, 0, spec.dataCW())
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
	for i := padStart; i < spec.dataCW(); i++ {
		if (i-padStart)%2 == 0 {
			out = append(out, 0xEC)
		} else {
			out = append(out, 0x11)
		}
	}
	return out
}

// applyMask returns a copy of the unmasked grid with one mask and its
// format information applied.
func applyMask(modules, function [][]bool, size, mask int) [][]bool {
	cand := make([][]bool, size)
	for y := range cand {
		cand[y] = make([]bool, size)
		copy(cand[y], modules[y])
		for x := range cand[y] {
			if !function[y][x] && maskFns[mask](x, y) {
				cand[y][x] = !cand[y][x]
			}
		}
	}
	setFormat(cand, function, size, mask)
	return cand
}

// qrPenalty scores a finished matrix with the four standard rules and
// returns the total; lower is better. Rules 1 and 3 are evaluated together
// per line from a history of run lengths, which is what lets rule 3 see a
// 1:1:3:1:1 sequence of any scale and treat the area outside the symbol as
// light.
func qrPenalty(m [][]bool) int {
	size := len(m)
	score := 0

	// A sliding window of the last seven run lengths, most recent first.
	var hist [7]int
	addRun := func(run int) {
		if hist[0] == 0 {
			run += size // the quiet zone before the first run is light
		}
		copy(hist[1:], hist[:6])
		hist[0] = run
	}
	// countFinders reports how many finder-lookalikes the window holds: a
	// 1:1:3:1:1 core with four times that unit of light on one side and at
	// least the unit on the other.
	countFinders := func() int {
		n := hist[1]
		core := n > 0 && hist[2] == n && hist[3] == n*3 && hist[4] == n && hist[5] == n
		if !core {
			return 0
		}
		c := 0
		if hist[0] >= n*4 && hist[6] >= n {
			c++
		}
		if hist[6] >= n*4 && hist[0] >= n {
			c++
		}
		return c
	}

	for _, byRow := range []bool{true, false} {
		for i := 0; i < size; i++ {
			hist = [7]int{}
			color, run := false, 0
			for j := 0; j < size; j++ {
				v := m[i][j]
				if !byRow {
					v = m[j][i]
				}
				if v == color {
					run++
					continue
				}
				// rule 1 — a run of five or more costs 3, plus one per
				// module beyond five
				if run >= 5 {
					score += 3 + (run - 5)
				}
				addRun(run)
				if !color {
					score += countFinders() * 40 // rule 3
				}
				color, run = v, 1
			}
			if run >= 5 {
				score += 3 + (run - 5)
			}
			if color { // close a trailing dark run first
				addRun(run)
				run = 0
			}
			run += size // the quiet zone after the last run is light
			addRun(run)
			score += countFinders() * 40
		}
	}

	// rule 2 — every 2×2 block of one colour costs 3
	for y := 0; y < size-1; y++ {
		for x := 0; x < size-1; x++ {
			c := m[y][x]
			if m[y][x+1] == c && m[y+1][x] == c && m[y+1][x+1] == c {
				score += 3
			}
		}
	}

	// rule 4 — deviation from an even split of dark and light, in whole
	// 5% steps away from half
	dark := 0
	for _, row := range m {
		for _, v := range row {
			if v {
				dark++
			}
		}
	}
	total := size * size
	dev := dark*20 - total*10
	if dev < 0 {
		dev = -dev
	}
	score += ((dev+total-1)/total - 1) * 10
	return score
}

// setFormat writes both copies of the level-L format info for the given
// mask. Bit i is taken LSB-first, which is the order the standard places
// them in.
func setFormat(modules, function [][]bool, size, mask int) {
	fb := formatBits(mask)
	put := func(x, y int, i int) {
		modules[y][x] = (fb>>uint(i))&1 == 1
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
