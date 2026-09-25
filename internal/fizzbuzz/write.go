package fizzbuzz

import (
	"io"
	"strconv"
	"sync"
)

const (
	streamBuf   = 32 << 10
	dEnd        = 40           // index of the closing quote in dcounter.d
	maxNumElem  = 24           // `,"9223372036854775807"` is 22 bytes
	maxReplElem = 3 + 6*2*1024 // default MaxStrBytes, every byte escaped. Longer strings allocate.
)

// dcounter holds `,"<digits>"` right-aligned in d. A carry adds a digit on the left.
type dcounter struct {
	d     [64]byte
	start int // most significant digit
}

func (c *dcounter) set(v int) {
	var tmp [24]byte
	digits := strconv.AppendInt(tmp[:0], int64(v), 10)
	c.start = dEnd - len(digits)
	copy(c.d[c.start:], digits)
	c.d[dEnd] = '"'
	c.d[c.start-1] = '"'
	c.d[c.start-2] = ','
}

func (c *dcounter) inc() {
	k := dEnd - 1
	for c.d[k] == '9' {
		c.d[k] = '0'
		k--
	}
	if k < c.start { // 99 -> 100: shift the framing left
		c.d[k] = '1'
		c.start = k
		c.d[k-1] = '"'
		c.d[k-2] = ','
		return
	}
	c.d[k]++
}

type gen struct {
	pl           *plan
	i            int // next element
	next1, next2 int // next multiples of a and b, >= i
	c            dcounter
}

func newGen(pl *plan, lo int) *gen {
	g := &gen{pl: pl, i: lo}
	g.next1 = ((lo-1)/pl.a + 1) * pl.a
	g.next2 = ((lo-1)/pl.b + 1) * pl.b
	g.c.set(lo)
	return g
}

func (pl *plan) maxElem() int {
	return max(maxNumElem, len(pl.e1), len(pl.e2), len(pl.e12))
}

// fill emits elements g.i..hi from pos while pos <= stop. buf needs
// stop+maxElem bytes.
func (g *gen) fill(buf []byte, pos, stop, hi int) int {
	for g.i <= hi && pos <= stop {
		end := min(g.next1, g.next2, hi+1)
		for g.i < end && pos <= stop {
			pos += copy(buf[pos:], g.c.d[g.c.start-2:dEnd+1])
			g.c.inc()
			g.i++
		}
		if g.i > hi || pos > stop {
			break
		}
		pos += copy(buf[pos:], g.event())
	}
	return pos
}

func (g *gen) event() []byte {
	pl := g.pl
	var e []byte
	if g.i == g.next1 {
		g.next1 += pl.a
		if g.i == g.next2 {
			g.next2 += pl.b
			e = pl.e12
		} else {
			e = pl.e1
		}
	} else {
		g.next2 += pl.b
		e = pl.e2
	}
	g.c.inc()
	g.i++
	return e
}

var bufPool = sync.Pool{New: func() any {
	b := make([]byte, streamBuf+maxReplElem+1)
	return &b
}}

// WriteJSON streams the array for validated p and returns the bytes written,
// exactly JSONSize(p). It stops at the first write error.
func WriteJSON(w io.Writer, p Params) (int64, error) {
	pl := newPlan(p)
	// The periodic path buffers a full period. Thus it runs only when a period
	// fits the stream buffer.
	if min(pl.a, pl.b) == 1 && pl.rangeBytes(1, min(max(pl.a, pl.b), pl.n)) <= streamBuf {
		return writePeriodic(w, pl)
	}
	bp := bufPool.Get().(*[]byte)
	defer bufPool.Put(bp)
	buf := *bp
	if need := streamBuf + pl.maxElem() + 1; len(buf) < need { // unvalidated long strings
		buf = make([]byte, need)
	}
	var written int64
	g := newGen(pl, 1)
	first := true
	for g.i <= pl.n {
		pos := g.fill(buf, 0, streamBuf, pl.n)
		if first {
			buf[0] = '['
			first = false
		}
		if g.i > pl.n {
			buf[pos] = ']'
			pos++
		}
		m, err := w.Write(buf[:pos])
		written += int64(m)
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

// writePeriodic handles int1 == 1 or int2 == 1. Then the bytes repeat every
// P = max(a, b) elements. A partial period at the end is a byte prefix of a
// full period, because it has no str1+str2.
func writePeriodic(w io.Writer, pl *plan) (int64, error) {
	P := min(max(pl.a, pl.b), pl.n)
	pb := int(pl.rangeBytes(1, P))
	k := min(streamBuf/pb, pl.n/P) // periods in each chunk. WriteJSON calls this only when pb <= streamBuf.
	chunk := make([]byte, k*pb+pl.maxElem())
	g := newGen(pl, 1)
	g.fill(chunk, 0, pb, P)
	for j := 1; j < k; j++ {
		copy(chunk[j*pb:], chunk[:pb])
	}
	var written int64
	first := true
	emit := func(b []byte) error {
		if len(b) == 0 {
			return nil
		}
		if first {
			b[0] = '['
			defer func() { b[0] = ',' }() // the chunk is reused
			first = false
		}
		m, err := w.Write(b)
		written += int64(m)
		return err
	}
	periods := pl.n / P
	for ; periods >= k; periods -= k {
		if err := emit(chunk[:k*pb]); err != nil {
			return written, err
		}
	}
	if err := emit(chunk[:periods*pb]); err != nil {
		return written, err
	}
	if err := emit(chunk[:pl.rangeBytes(1, pl.n%P)]); err != nil {
		return written, err
	}
	m, err := io.WriteString(w, "]")
	return written + int64(m), err
}
