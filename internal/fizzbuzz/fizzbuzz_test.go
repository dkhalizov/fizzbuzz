package fizzbuzz

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// reference is the specification, written as literally as possible.
func reference(p Params) []string {
	out := []string{}
	for i := 1; i <= p.Limit; i++ {
		switch {
		case i%p.Int1 == 0 && i%p.Int2 == 0:
			out = append(out, p.Str1+p.Str2)
		case i%p.Int1 == 0:
			out = append(out, p.Str1)
		case i%p.Int2 == 0:
			out = append(out, p.Str2)
		default:
			out = append(out, strconv.Itoa(i))
		}
	}
	return out
}

// check verifies WriteJSON and JSONSize against json.Marshal(reference(p)).
func check(t *testing.T, p Params) {
	t.Helper()
	want, _ := json.Marshal(reference(p))
	if size := JSONSize(p); size != int64(len(want)) {
		t.Fatalf("JSONSize = %d, want %d for %+v", size, len(want), p)
	}
	var b bytes.Buffer
	n, err := WriteJSON(&b, p)
	if err != nil || n != int64(len(want)) || !bytes.Equal(b.Bytes(), want) {
		t.Fatalf("WriteJSON mismatch for %+v (n=%d, err=%v):\n got %.200s\nwant %.200s", p, n, err, b.Bytes(), want)
	}
}

func TestExample(t *testing.T) {
	p := Params{Int1: 3, Int2: 5, Limit: 16, Str1: "fizz", Str2: "buzz"}
	var b bytes.Buffer
	if _, err := WriteJSON(&b, p); err != nil {
		t.Fatal(err)
	}
	want := `["1","2","fizz","4","buzz","fizz","7","8","fizz","buzz","11","fizz","13","14","fizzbuzz","16"]`
	if b.String() != want {
		t.Fatalf("got %s", b.String())
	}
}

// Every divisor pair up to 24 covers equal divisors, non-coprime pairs
// (4, 6 -> both at 12), divisors of each other and the periodic path (1, b).
func TestGrid(t *testing.T) {
	limits := []int{1, 2, 3, 7, 9, 10, 11, 99, 100, 101, 1000, 1001, 2311}
	for a := 1; a <= 24; a++ {
		for b := 1; b <= 24; b++ {
			for _, n := range limits {
				check(t, Params{Int1: a, Int2: b, Limit: n, Str1: "fizz", Str2: "buzz"})
			}
		}
	}
}

func TestStrings(t *testing.T) {
	pairs := [][2]string{
		{"a", "b"},
		{`"q\`, "<&>é🙂 "}, // JSON and HTML escaping, multi-byte UTF-8
		{"x", strings.Repeat("y", 300)},
		{strings.Repeat("<", DefaultLimits.MaxStrBytes), strings.Repeat(`"`, DefaultLimits.MaxStrBytes)}, // ~8 KiB elements
		{"\x00\t\n", "日本語"},
	}
	for _, s := range pairs {
		for _, ab := range [][2]int{{3, 5}, {1, 1}, {1, 4}, {6, 1}, {4, 6}, {7, 7}, {50, 1000}} {
			for _, n := range []int{1, 5, 60, 777, 5000} {
				check(t, Params{Int1: ab[0], Int2: ab[1], Limit: n, Str1: s[0], Str2: s[1]})
			}
		}
	}
}

func TestSpecialDivisors(t *testing.T) {
	cases := [][2]int{
		{math.MaxInt, math.MaxInt}, {math.MaxInt, 3}, {3, math.MaxInt},
		{1_000_003, 1_000_033}, {997, 991}, {1 << 40, 1 << 41}, {3, 6}, {6, 3},
		{12, 18}, {100_000, 100_000}, {99_999, 100_001},
	}
	for _, c := range cases {
		for _, n := range []int{1, 1000, 99_999, 100_000, 100_001, 250_000} {
			check(t, Params{Int1: c[0], Int2: c[1], Limit: n, Str1: "a", Str2: "bb"})
		}
	}
}

// Large ranges cross many buffer boundaries and exercise the periodic path's
// chunk repetition.
func TestLarge(t *testing.T) {
	for _, c := range [][2]int{{3, 5}, {1, 7}, {5, 1}, {1, 1}, {2, 3}, {1, 300_007}} {
		check(t, Params{Int1: c[0], Int2: c[1], Limit: 300_000, Str1: "fizz", Str2: "buzz"})
	}
}

func TestDecimalCounter(t *testing.T) {
	for k := 0; k <= 18; k++ {
		base := int(math.Pow10(k))
		lo := max(1, base-60)
		var c dcounter
		c.set(lo)
		for v := lo; v < lo+120 && v > 0; v++ {
			want := `,"` + strconv.Itoa(v) + `"`
			if got := string(c.d[c.start-2 : dEnd+1]); got != want {
				t.Fatalf("counter at %d: got %s want %s", v, got, want)
			}
			c.inc()
		}
	}
}

func TestLcmCapped(t *testing.T) {
	cases := []struct{ a, b, lim, want int }{
		{3, 5, 100, 15}, {3, 5, 14, 0}, {4, 6, 100, 12}, {7, 7, 7, 7},
		{math.MaxInt, math.MaxInt, math.MaxInt, math.MaxInt},
		{math.MaxInt, math.MaxInt - 1, math.MaxInt, 0}, // true lcm overflows
		{1 << 62, 1 << 61, math.MaxInt, 1 << 62},
	}
	for _, c := range cases {
		if got := lcmCapped(c.a, c.b, c.lim); got != c.want {
			t.Errorf("lcmCapped(%d, %d, %d) = %d, want %d", c.a, c.b, c.lim, got, c.want)
		}
	}
}

// For divisors above the limit every element is a number, so the size has a
// closed form: total digits + 3n + 1. Checks the formula far beyond DefaultLimits.MaxLimit.
func TestJSONSizeClosedForm(t *testing.T) {
	for _, n := range []int{9, 10, 99, 100, 12345, 100_000_000, 999_999_999_999} {
		p := Params{Int1: math.MaxInt, Int2: math.MaxInt, Limit: n}
		var digits int64
		d, pow := int64(1), 1
		for pow <= n {
			digits += int64(min(n, pow*10-1)-pow+1) * d
			d++
			pow *= 10
		}
		if got, want := JSONSize(p), digits+3*int64(n)+1; got != want {
			t.Errorf("n=%d: got %d want %d", n, got, want)
		}
	}
}

type failWriter struct{ writes int }

func (f *failWriter) Write(p []byte) (int, error) {
	f.writes++
	if f.writes > 1 {
		return 0, errors.New("client gone")
	}
	return len(p), nil
}

func TestWriteErrorStopsGeneration(t *testing.T) {
	for _, p := range []Params{
		{Int1: 3, Int2: 5, Limit: 1_000_000, Str1: "fizz", Str2: "buzz"},
		{Int1: 1, Int2: 7, Limit: 1_000_000, Str1: "fizz", Str2: "buzz"}, // periodic path
	} {
		w := &failWriter{}
		if _, err := WriteJSON(w, p); err == nil || w.writes != 2 {
			t.Errorf("%+v: err=%v after %d writes, want an error after 2", p, err, w.writes)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := Params{Int1: 3, Int2: 5, Limit: 100, Str1: "fizz", Str2: "buzz"}
	if err := ok.Validate(DefaultLimits); err != nil {
		t.Fatal(err)
	}
	with := func(f func(*Params)) Params { p := ok; f(&p); return p }
	cases := []struct {
		p     Params
		param string
	}{
		{with(func(p *Params) { p.Int1 = 0 }), "int1"},
		{with(func(p *Params) { p.Int2 = -1 }), "int2"},
		{with(func(p *Params) { p.Limit = 0 }), "limit"},
		{with(func(p *Params) { p.Limit = DefaultLimits.MaxLimit + 1 }), "limit"},
		{with(func(p *Params) { p.Str1 = "" }), "str1"},
		{with(func(p *Params) { p.Str2 = strings.Repeat("x", DefaultLimits.MaxStrBytes+1) }), "str2"},
		{with(func(p *Params) { p.Str1 = "\xff" }), "str1"},
		{with(func(p *Params) { p.Int1, p.Limit = 0, 0 }), "int1"}, // first failure wins
		// Every field in range, but the response would be about 10 GB.
		{Params{Int1: 1, Int2: 1, Limit: DefaultLimits.MaxLimit, Str1: strings.Repeat("x", 1000), Str2: "y"}, "limit"},
	}
	for _, c := range cases {
		var fe *FieldError
		if err := c.p.Validate(DefaultLimits); !errors.As(err, &fe) || fe.Param != c.param {
			t.Errorf("Validate(%+v) = %v, want a %s error", c.p, err, c.param)
		}
	}
	edges := []Params{
		with(func(p *Params) { p.Limit = DefaultLimits.MaxLimit }),
		with(func(p *Params) { p.Str1 = strings.Repeat("x", DefaultLimits.MaxStrBytes) }),
		with(func(p *Params) { p.Int1, p.Int2 = math.MaxInt, math.MaxInt }),
	}
	for _, p := range edges {
		if err := p.Validate(DefaultLimits); err != nil {
			t.Errorf("Validate(limit=%d, len(str1)=%d) = %v, want nil", p.Limit, len(p.Str1), err)
		}
	}
}

// The hand-optimized generator must match the literal reference for any
// input; the seed corpus runs as part of go test.
func FuzzEquivalence(f *testing.F) {
	f.Add(uint16(3), uint16(5), uint16(100), "fizz", "buzz")
	f.Add(uint16(1), uint16(1), uint16(10), "a", "b")
	f.Add(uint16(6), uint16(4), uint16(1000), `"`, "<")
	f.Fuzz(func(t *testing.T, a, b, n uint16, s1, s2 string) {
		p := Params{Int1: int(a%64) + 1, Int2: int(b%64) + 1, Limit: int(n%5000) + 1, Str1: s1, Str2: s2}
		if p.Validate(DefaultLimits) != nil {
			return
		}
		check(t, p)
	})
}

// One period of int1=1, int2=limit is the whole response; it must still be
// streamed through a bounded buffer, not built in memory.
func TestPeriodicMemoryBounded(t *testing.T) {
	p := Params{Int1: 1, Int2: DefaultLimits.MaxLimit, Limit: DefaultLimits.MaxLimit, Str1: "a", Str2: "b"}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if _, err := WriteJSON(io.Discard, p); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 1<<20 {
		t.Fatalf("WriteJSON allocated %d bytes for a %d-byte response", alloc, JSONSize(p))
	}
}
