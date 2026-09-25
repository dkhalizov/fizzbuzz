package fizzbuzz

import (
	"fmt"
	"math"
	"sort"
	"unicode/utf8"
)

// The divisors use int arithmetic up to 2^63-1. This line does not compile on a
// 32-bit platform.
const _ uint = math.MaxInt - (1<<63 - 1)

type Limits struct {
	MaxLimit         int
	MaxStrBytes      int   // per string, before JSON escaping
	MaxResponseBytes int64 // exact JSON size
}

var DefaultLimits = Limits{MaxLimit: 10_000_000, MaxStrBytes: 1024, MaxResponseBytes: 256 << 20}

// Params must pass Validate before JSONSize or WriteJSON.
type Params struct {
	Int1, Int2, Limit int
	Str1, Str2        string
}

type FieldError struct {
	Param  string
	Reason string
}

func (e *FieldError) Error() string { return e.Param + ": " + e.Reason }

// Validate returns the first violation in the order int1, int2, limit, str1,
// str2, then the response size.
func (p Params) Validate(lim Limits) error {
	switch {
	case p.Int1 < 1:
		return &FieldError{"int1", "must be at least 1"}
	case p.Int2 < 1:
		return &FieldError{"int2", "must be at least 1"}
	case p.Limit < 1 || p.Limit > lim.MaxLimit:
		return &FieldError{"limit", fmt.Sprintf("must be between 1 and %d", lim.MaxLimit)}
	}
	if err := validateStr("str1", p.Str1, lim.MaxStrBytes); err != nil {
		return err
	}
	if err := validateStr("str2", p.Str2, lim.MaxStrBytes); err != nil {
		return err
	}
	if size := JSONSize(p); size > lim.MaxResponseBytes {
		// The size rounds up and the maximum rounds down, so they never print equal.
		reason := fmt.Sprintf("the response would be %d MiB, but the maximum is %d MiB",
			(size+1<<20-1)>>20, lim.MaxResponseBytes>>20)
		if n := largestLimit(p, lim.MaxResponseBytes); n > 0 {
			reason += fmt.Sprintf("; with these strings, use a limit of %d or less", n)
		}
		return &FieldError{"limit", reason}
	}
	return nil
}

// largestLimit returns the largest limit below p.Limit whose response fits, or
// 0. JSONSize grows with limit, so a binary search needs about 24 calls.
func largestLimit(p Params, maxBytes int64) int {
	return sort.Search(p.Limit, func(i int) bool {
		p.Limit = i + 1
		return JSONSize(p) > maxBytes
	})
}

func validateStr(name, s string, maxBytes int) error {
	switch {
	case s == "":
		return &FieldError{name, "must not be empty"}
	case len(s) > maxBytes:
		return &FieldError{name, fmt.Sprintf("must be at most %d bytes", maxBytes)}
	case !utf8.ValidString(s):
		return &FieldError{name, "must be valid UTF-8"}
	}
	return nil
}
