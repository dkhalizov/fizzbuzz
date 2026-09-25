package fizzbuzz

import (
	"encoding/binary"
	"slices"
	"sync"
)

// Block templates.
//
// Take K = 10^k and L = lcm(int1, int2, K). In a block of L elements that
// starts after a multiple of L, the element types repeat, and so do the low k
// digits of each number. Only the high part of a number changes: element j
// of the block (1 <= j <= L) after s is the number s+j, and its high part is
// s/K + j/K. When all high parts in the block have the same width w, the
// bytes of the block are a fixed template with a w-byte hole in each number:
//
//	j:      ... 9998    9999    10000   10001  ...     (k = 4, s/K = 7)
//	bytes:  ,"7|9998" ,"7|9999" ,"8|0000" ,"8|0001"    hole = 7, 7, 8, 8
//
// The template is built one time for each width. For each block, the writer
// fills the holes in place with one store per number and writes the block
// buffer to w directly. Blocks that cross a power of ten, the first block and
// the tail use the element generator.
const (
	maxBlockElems = 1 << 16
	maxBlockBytes = 1 << 20
)

// blocks keeps copies of the plan fields, not a pointer to the plan, so the
// plan of the caller stays on the stack.
type blocks struct {
	a, b, l      int
	i1, i2, end  int // element offsets in elems, as in plan
	k, K, L      int
	plain, bytes int // numbers and bytes in a block, for the widest holes
	width        int // width of the holes in tmpl, 0 before the first build
	*blockBufs
}

type blockBufs struct {
	elems   []byte   // a copy of the plan elements
	tmpl    []byte   // the block with holes
	holes   []int32  // hole positions, ordered by j
	holeEnd [][2]int // for each t = j/K: its range in holes
}

// A template is up to 1 MiB, so requests reuse the buffers.
var blockPool = sync.Pool{New: func() any { return new(blockBufs) }}

// newBlocks returns false when the plan has no block of useful size: the
// period is too long, or the response too short to repeat a block often.
func newBlocks(pl *plan) (blocks, bool) {
	// The shortest block has max(lcm, 10) elements, and a response needs 16.
	if pl.l == 0 || pl.l > pl.n/16 || pl.n < 160 {
		return blocks{}, false
	}
	nd := decWidth(pl.n)
	for k, K := 6, 1_000_000; k >= 1; k, K = k-1, K/10 {
		if k >= nd {
			continue
		}
		L := lcmCapped(pl.l, K, maxBlockElems)
		if L == 0 || L > pl.n/16 {
			continue
		}
		plain := L - L/pl.a - L/pl.b + L/pl.l
		bytes := plain*(3+nd) + (L/pl.a-L/pl.l)*pl.len1() + (L/pl.b-L/pl.l)*pl.len2() + L/pl.l*pl.len12()
		if bytes > maxBlockBytes {
			continue
		}
		return blocks{a: pl.a, b: pl.b, l: pl.l, i1: pl.i1, i2: pl.i2, end: pl.end, k: k, K: K, L: L, plain: plain, bytes: bytes}, true
	}
	return blocks{}, false
}

func decWidth(v int) int {
	w := 1
	for v >= 10 {
		v /= 10
		w++
	}
	return w
}

// render returns the bytes of elements s+1..s+L, or nil when the high parts
// in the block have different widths. s is a positive multiple of L.
func (b *blocks) render(s int) []byte {
	H := s / b.K
	w := decWidth(H)
	if decWidth(H+b.L/b.K) != w {
		return nil
	}
	if w != b.width {
		b.build(w)
	}
	var digits [20]byte
	for t, r := range b.holeEnd {
		v := H + t
		for i := w - 1; i >= 0; i-- {
			digits[i] = byte('0' + v%10)
			v /= 10
		}
		fillHoles(b.tmpl, b.holes[r[0]:r[1]], digits[:w])
	}
	return b.tmpl
}

// fillHoles stores d at each position. The switch runs one time for all
// positions, so the loops are simple stores.
func fillHoles(buf []byte, at []int32, d []byte) {
	switch len(d) {
	case 1:
		c := d[0]
		for _, p := range at {
			buf[p] = c
		}
	case 2:
		v := binary.LittleEndian.Uint16(d)
		for _, p := range at {
			binary.LittleEndian.PutUint16(buf[p:], v)
		}
	case 3:
		v, c := binary.LittleEndian.Uint16(d), d[2]
		for _, p := range at {
			binary.LittleEndian.PutUint16(buf[p:], v)
			buf[p+2] = c
		}
	case 4:
		v := binary.LittleEndian.Uint32(d)
		for _, p := range at {
			binary.LittleEndian.PutUint32(buf[p:], v)
		}
	default:
		for _, p := range at {
			copy(buf[p:], d)
		}
	}
}

// build lays out the template for holes of width w. It tracks the next
// multiples and a k-digit counter, so it does not divide.
func (b *blocks) build(w int) {
	b.width = w
	b.tmpl = slices.Grow(b.tmpl[:0], b.bytes)
	b.holes = slices.Grow(b.holes[:0], b.plain)
	b.holeEnd = slices.Grow(b.holeEnd[:0], b.L/b.K+1)
	e := b.elems
	e1, e2, e12 := e[:b.i1], e[b.i1:b.i2], e[b.i2:b.end]
	low := make([]byte, b.k)
	for i := range low {
		low[i] = '0'
	}
	next1, next2 := b.a, b.b
	start := 0
	for j := 1; j <= b.L; j++ {
		// low = j mod K, as k digits
		i := b.k - 1
		for low[i] == '9' {
			low[i] = '0'
			i--
			if i < 0 {
				break
			}
		}
		if i >= 0 {
			low[i]++
		}
		if j%b.K == 0 { // a new high part starts at j
			b.holeEnd = append(b.holeEnd, [2]int{start, len(b.holes)})
			start = len(b.holes)
		}
		switch {
		case j == next1 && j == next2:
			b.tmpl = append(b.tmpl, e12...)
		case j == next1:
			b.tmpl = append(b.tmpl, e1...)
		case j == next2:
			b.tmpl = append(b.tmpl, e2...)
		default:
			b.tmpl = append(b.tmpl, ',', '"')
			b.holes = append(b.holes, int32(len(b.tmpl)))
			for range w {
				b.tmpl = append(b.tmpl, '0')
			}
			b.tmpl = append(b.tmpl, low...)
			b.tmpl = append(b.tmpl, '"')
		}
		if j == next1 {
			next1 += b.a
		}
		if j == next2 {
			next2 += b.b
		}
	}
	b.holeEnd = append(b.holeEnd, [2]int{start, len(b.holes)})
}
