package fizzbuzz

import (
	"encoding/json"
	"math"
	"strings"
)

type plan struct {
	a, b, n int
	l       int // lcm(a, b) if <= n, else 0
	// The replacements, encoded with their leading comma, for example
	// `,"fizz"`, one after the other: e1 is elems()[:i1], e2 is
	// elems()[i1:i2] and e12 is elems()[i2:end]. Short plain strings stay in
	// inline, so a plan needs no heap allocation. The plan keeps offsets, not
	// slices into itself, so a copy of a plan stays valid.
	i1, i2, end int
	heap        []byte
	inline      [inlineElems]byte
}

// inlineElems holds the three elements when str1 and str2 have 43 bytes
// together or less and need no escaping.
const inlineElems = 96

func newPlan(p Params) plan {
	pl := plan{a: p.Int1, b: p.Int2, n: p.Limit, l: lcmCapped(p.Int1, p.Int2, p.Limit)}
	if n := 2*(len(p.Str1)+len(p.Str2)) + 9; n <= inlineElems && isPlain(p.Str1) && isPlain(p.Str2) {
		// The result aliases pl.inline. Only its length is kept, so pl stays on the stack.
		_, pl.i1, pl.i2, pl.end = appendElems(pl.inline[:0:inlineElems], p.Str1, p.Str2)
	} else {
		pl.heap, pl.i1, pl.i2, pl.end = appendElems(make([]byte, 0, n), p.Str1, p.Str2)
	}
	return pl
}

// appendElems appends e1, e2 and e12 and returns the offsets of their ends.
func appendElems(buf []byte, s1, s2 string) (b []byte, i1, i2, end int) {
	buf = appendElem(buf, s1)
	i1 = len(buf)
	buf = appendElem(buf, s2)
	i2 = len(buf)
	buf = appendElem(buf, s1, s2)
	return buf, i1, i2, len(buf)
}

func (pl *plan) elems() []byte {
	if pl.heap != nil {
		return pl.heap
	}
	return pl.inline[:pl.end]
}

func (pl *plan) len1() int  { return pl.i1 }
func (pl *plan) len2() int  { return pl.i2 - pl.i1 }
func (pl *plan) len12() int { return pl.end - pl.i2 }

// appendElem copies plain printable ASCII directly and hands anything else to
// encoding/json, so escaping (HTML-safe < included) matches json.Marshal.
func appendElem(dst []byte, parts ...string) []byte {
	plain := true
	for _, s := range parts {
		plain = plain && isPlain(s)
	}
	dst = append(dst, ',')
	if !plain {
		q, _ := json.Marshal(strings.Join(parts, ""))
		return append(dst, q...)
	}
	dst = append(dst, '"')
	for _, s := range parts {
		dst = append(dst, s...)
	}
	return append(dst, '"')
}

// isPlain reports whether json.Marshal copies s without escapes.
func isPlain(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c > 0x7e || c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' {
			return false
		}
	}
	return true
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// lcmCapped returns lcm(a, b) if it is <= limit, else 0. It never overflows:
// (a/g)*b > limit  <=>  a/g > limit/b.
func lcmCapped(a, b, limit int) int {
	q := a / gcd(a, b)
	if q > limit/b {
		return 0
	}
	return q * b
}

// multiples counts the multiples of m in [1, x]. For m == 0 it returns 0.
func multiples(x, m int) int {
	if m == 0 || x <= 0 {
		return 0
	}
	return x / m
}

// rangeBytes is the exact size of elements lo..hi, each with its leading comma.
// Plain numbers are counted per decimal band by inclusion-exclusion.
func (pl *plan) rangeBytes(lo, hi int) int64 {
	if lo > hi {
		return 0
	}
	cnt := func(m int) int64 { return int64(multiples(hi, m) - multiples(lo-1, m)) }
	c1, c2, c12 := cnt(pl.a), cnt(pl.b), cnt(pl.l)
	total := (c1-c12)*int64(pl.len1()) + (c2-c12)*int64(pl.len2()) + c12*int64(pl.len12())

	bandLo := 1
	for d := 1; bandLo <= hi; d++ {
		bandHi := math.MaxInt
		if bandLo <= math.MaxInt/10 {
			bandHi = bandLo*10 - 1
		}
		x, y := max(lo, bandLo), min(hi, bandHi)
		if x <= y {
			k := func(m int) int { return multiples(y, m) - multiples(x-1, m) }
			plain := (y - x + 1) - k(pl.a) - k(pl.b) + k(pl.l)
			total += int64(plain) * int64(d+3) // ,"digits"
		}
		if bandHi == math.MaxInt {
			break
		}
		bandLo = bandHi + 1
	}
	return total
}

// JSONSize returns the exact size of the JSON array in O(log limit).
func JSONSize(p Params) int64 {
	if p.Limit <= 0 {
		return 2
	}
	pl := newPlan(p)
	return pl.rangeBytes(1, p.Limit) + 1 // first comma becomes '[', plus ']'
}
