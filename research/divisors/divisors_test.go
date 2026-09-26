// Package divisors compares ways to generate the response when int1 and int2
// are runtime values, as in the service. The compiler turns i%3 == 0 into a
// multiply only for a constant divisor. With a runtime divisor it emits a
// real division (IDIV on amd64). Each implementation writes the JSON array
// of the service into a []byte, and TestSame checks it against the generator
// of the service.
package divisors

import (
	"fmt"
	"math"
	"math/bits"
	"strconv"
	"testing"

	"fizzbuzz/internal/fizzbuzz"
)

type input struct {
	a, b, n     int
	e1, e2, e12 []byte // `,"fizz"`, `,"buzz"`, `,"fizzbuzz"`
}

func newInput(a, b, n int, s1, s2 string) input {
	return input{a: a, b: b, n: n,
		e1: []byte(`,"` + s1 + `"`), e2: []byte(`,"` + s2 + `"`), e12: []byte(`,"` + s1 + s2 + `"`)}
}

// finish turns the first comma into '[' and closes the array.
func finish(dst []byte, start int) []byte {
	dst[start] = '['
	return append(dst, ']')
}

func appendNum(dst []byte, i int) []byte {
	dst = append(dst, ',', '"')
	dst = strconv.AppendInt(dst, int64(i), 10)
	return append(dst, '"')
}

// naive tests divisibility with %: two divisions for each number.
//
//go:noinline
func naive(dst []byte, in input) []byte {
	start := len(dst)
	for i := 1; i <= in.n; i++ {
		m1, m2 := i%in.a == 0, i%in.b == 0
		switch {
		case m1 && m2:
			dst = append(dst, in.e12...)
		case m1:
			dst = append(dst, in.e1...)
		case m2:
			dst = append(dst, in.e2...)
		default:
			dst = appendNum(dst, i)
		}
	}
	return finish(dst, start)
}

// divTest is the divisibility test that the compiler uses for a constant
// divisor (Granlund and Montgomery; Hacker's Delight 10-17): d = d0 * 2^s
// with d0 odd divides n if and only if rotr(n * inverse(d0), s) <= max/d.
// It is exact for every uint64, with one multiply.
type divTest struct {
	inv, lim uint64
	shift    int
}

func newDivTest(d uint64) divTest {
	s := bits.TrailingZeros64(d)
	d0 := d >> s
	inv := d0 // correct to 3 bits, because d0*d0 = 1 mod 8
	for range 5 {
		inv *= 2 - d0*inv // Newton: each step doubles the correct bits
	}
	return divTest{inv: inv, lim: math.MaxUint64 / d, shift: s}
}

func (t divTest) divides(n uint64) bool { return bits.RotateLeft64(n*t.inv, -t.shift) <= t.lim }

// fastmod is naive with the multiply test instead of %.
//
//go:noinline
func fastmod(dst []byte, in input) []byte {
	start := len(dst)
	t1, t2 := newDivTest(uint64(in.a)), newDivTest(uint64(in.b))
	for i := 1; i <= in.n; i++ {
		m1, m2 := t1.divides(uint64(i)), t2.divides(uint64(i))
		switch {
		case m1 && m2:
			dst = append(dst, in.e12...)
		case m1:
			dst = append(dst, in.e1...)
		case m2:
			dst = append(dst, in.e2...)
		default:
			dst = appendNum(dst, i)
		}
	}
	return finish(dst, start)
}

// counters keeps the next multiple of each divisor: a compare for each number.
//
//go:noinline
func counters(dst []byte, in input) []byte {
	start := len(dst)
	next1, next2 := in.a, in.b
	for i := 1; i <= in.n; i++ {
		m1, m2 := i == next1, i == next2
		if m1 {
			next1 += in.a
		}
		if m2 {
			next2 += in.b
		}
		switch {
		case m1 && m2:
			dst = append(dst, in.e12...)
		case m1:
			dst = append(dst, in.e1...)
		case m2:
			dst = append(dst, in.e2...)
		default:
			dst = appendNum(dst, i)
		}
	}
	return finish(dst, start)
}

// dec holds `,"<digits>"` right-aligned in b[s:]. inc adds 1 in place.
type dec struct {
	b [24]byte
	s int
}

func newDec() dec {
	d := dec{s: 20}
	copy(d.b[20:], `,"0"`)
	return d
}

func (d *dec) inc() {
	k := 22
	for d.b[k] == '9' {
		d.b[k] = '0'
		k--
	}
	if k == d.s+1 { // the opening quote: 99 -> 100
		d.b[k] = '1'
		d.b[k-1] = '"'
		d.b[k-2] = ','
		d.s--
		return
	}
	d.b[k]++
}

// decimal is counters with the in-place decimal counter, so no number is
// formatted.
//
//go:noinline
func decimal(dst []byte, in input) []byte {
	start := len(dst)
	next1, next2 := in.a, in.b
	d := newDec()
	for i := 1; i <= in.n; i++ {
		d.inc()
		m1, m2 := i == next1, i == next2
		if m1 {
			next1 += in.a
		}
		if m2 {
			next2 += in.b
		}
		switch {
		case m1 && m2:
			dst = append(dst, in.e12...)
		case m1:
			dst = append(dst, in.e1...)
		case m2:
			dst = append(dst, in.e2...)
		default:
			dst = append(dst, d.b[d.s:]...)
		}
	}
	return finish(dst, start)
}

// segment is the element loop of the service: it copies the plain numbers up
// to the next multiple without a test, and handles only the multiples.
//
//go:noinline
func segment(dst []byte, in input) []byte {
	start := len(dst)
	next1, next2 := in.a, in.b
	d := newDec()
	for i := 1; i <= in.n; {
		end := min(next1, next2, in.n+1)
		for ; i < end; i++ {
			d.inc()
			dst = append(dst, d.b[d.s:]...)
		}
		if i > in.n {
			break
		}
		d.inc()
		switch {
		case i == next1 && i == next2:
			dst = append(dst, in.e12...)
		case i == next1:
			dst = append(dst, in.e1...)
		default:
			dst = append(dst, in.e2...)
		}
		if i == next1 {
			next1 += in.a
		}
		if i == next2 {
			next2 += in.b
		}
		i++
	}
	return finish(dst, start)
}

type appendWriter struct{ b *[]byte }

func (w appendWriter) Write(p []byte) (int, error) { *w.b = append(*w.b, p...); return len(p), nil }

// service is the generator of the service: the segment loop, and block
// templates when the period is short enough (see internal/fizzbuzz/block.go).
//
//go:noinline
func service(dst []byte, in input, p fizzbuzz.Params) []byte {
	_, _ = fizzbuzz.WriteJSON(appendWriter{&dst}, p)
	return dst
}

type impl struct {
	name string
	f    func([]byte, input, fizzbuzz.Params) []byte
}

func wrap(f func([]byte, input) []byte) func([]byte, input, fizzbuzz.Params) []byte {
	return func(dst []byte, in input, _ fizzbuzz.Params) []byte { return f(dst, in) }
}

var impls = []impl{
	{"naive", wrap(naive)}, {"fastmod", wrap(fastmod)}, {"counters", wrap(counters)},
	{"decimal", wrap(decimal)}, {"segment", wrap(segment)}, {"service", service},
}

func TestDivTest(t *testing.T) {
	ds := []uint64{1, 2, 3, 5, 6, 7, 10, 12, 64, 96, 997, 1 << 40, 3 << 40, math.MaxInt64, math.MaxUint64}
	ns := []uint64{0, 1, 2, 3, 15, 1000, 1 << 32, 1<<32 + 1, 3 << 40, 1<<63 - 1, math.MaxUint64, math.MaxUint64 - 1}
	for _, d := range ds {
		t1 := newDivTest(d)
		for _, n := range ns {
			for _, x := range []uint64{n, n * d, n*d + 1} {
				if got, want := t1.divides(x), x%d == 0; got != want {
					t.Fatalf("d=%d n=%d: %v, want %v", d, x, got, want)
				}
			}
		}
	}
}

func TestSame(t *testing.T) {
	for _, c := range [][3]int{{3, 5, 1}, {3, 5, 100}, {3, 5, 100_000}, {4, 6, 5000}, {7, 7, 999}, {1, 1, 50}, {997, 991, 200_000}, {1 << 40, 3, 1000}} {
		p := fizzbuzz.Params{Int1: c[0], Int2: c[1], Limit: c[2], Str1: "fizz", Str2: "buzz"}
		want := service(nil, input{}, p)
		in := newInput(c[0], c[1], c[2], "fizz", "buzz")
		for _, im := range impls {
			if got := im.f(nil, in, p); string(got) != string(want) {
				t.Fatalf("%s %v differs", im.name, c)
			}
		}
	}
}

// The divisors come from the case table at run time, not from constants.
var cases = []struct{ a, b, n int }{
	{3, 5, 100}, {3, 5, 10_000}, {3, 5, 1_000_000},
	{997, 991, 10_000}, {997, 991, 1_000_000},
}

func BenchmarkGenerate(b *testing.B) {
	buf := make([]byte, 0, 16<<20)
	for _, c := range cases {
		in := newInput(c.a, c.b, c.n, "fizz", "buzz")
		p := fizzbuzz.Params{Int1: c.a, Int2: c.b, Limit: c.n, Str1: "fizz", Str2: "buzz"}
		size := fizzbuzz.JSONSize(p)
		for _, im := range impls {
			b.Run(fmt.Sprintf("case=%d_%d_n%d/impl=%s", c.a, c.b, c.n, im.name), func(b *testing.B) {
				b.SetBytes(size)
				for b.Loop() {
					buf = im.f(buf[:0], in, p)
				}
			})
		}
	}
}
