package fizzbuzz

import (
	"encoding/json"
	"math"
	"strings"
)

type plan struct {
	a, b, n int
	l       int // lcm(a, b) if <= n, else 0
	// The replacements, encoded with their leading comma, for example `,"fizz"`.
	e1, e2, e12 []byte
}

func newPlan(p Params) *plan {
	pl := &plan{a: p.Int1, b: p.Int2, n: p.Limit, l: lcmCapped(p.Int1, p.Int2, p.Limit)}
	buf := make([]byte, 0, 2*(len(p.Str1)+len(p.Str2))+9)
	buf = appendElem(buf, p.Str1)
	i1 := len(buf)
	buf = appendElem(buf, p.Str2)
	i2 := len(buf)
	buf = appendElem(buf, p.Str1, p.Str2)
	pl.e1, pl.e2, pl.e12 = buf[:i1:i1], buf[i1:i2:i2], buf[i2:]
	return pl
}

// appendElem copies plain printable ASCII directly and hands anything else to
// encoding/json, so escaping (HTML-safe < included) matches json.Marshal.
func appendElem(dst []byte, parts ...string) []byte {
	plain := true
	for _, s := range parts {
		for i := 0; i < len(s); i++ {
			if c := s[i]; c < 0x20 || c > 0x7e || c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' {
				plain = false
			}
		}
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
	total := (c1-c12)*int64(len(pl.e1)) + (c2-c12)*int64(len(pl.e2)) + c12*int64(len(pl.e12))

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
	return newPlan(p).rangeBytes(1, p.Limit) + 1 // first comma becomes '[', plus ']'
}
