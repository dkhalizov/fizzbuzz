package fizzbuzz

import (
	"fmt"
	"io"
	"testing"
)

// BenchmarkWriteJSON reports MB/s of JSON produced into io.Discard.
//
//	go test -run='^$' -bench=WriteJSON -benchmem -count=10 ./internal/fizzbuzz | tee new.txt
//	benchstat old.txt new.txt
func BenchmarkWriteJSON(b *testing.B) {
	pairs := [][2]int{{3, 5}, {2, 3}, {1, 1}, {997, 991}, {1_000_003, 1_000_033}}
	for _, n := range []int{100, 10_000, 1_000_000, DefaultLimits.MaxLimit} {
		for _, ab := range pairs {
			p := Params{Int1: ab[0], Int2: ab[1], Limit: n, Str1: "fizz", Str2: "buzz"}
			b.Run(fmt.Sprintf("n=%d/%d_%d", n, ab[0], ab[1]), func(b *testing.B) {
				b.SetBytes(JSONSize(p))
				b.ReportAllocs()
				for b.Loop() {
					_, _ = WriteJSON(io.Discard, p)
				}
			})
		}
	}
}
