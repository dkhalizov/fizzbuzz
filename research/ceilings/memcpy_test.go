// Package ceilings holds benchmarks that measure the hardware limits for the
// performance research. They do not test the service.
package ceilings

import (
	"fmt"
	"testing"
)

// BenchmarkCopy copies between two buffers of the same size. 32 KiB stays in
// L1 and L2, like the stream buffer. 256 MiB goes to DRAM.
func BenchmarkCopy(b *testing.B) {
	for _, n := range []int{32 << 10, 256 << 10, 1 << 20, 256 << 20} {
		src, dst := make([]byte, n), make([]byte, n)
		b.Run(fmt.Sprintf("size=%dKiB", n>>10), func(b *testing.B) {
			b.SetBytes(int64(n))
			for b.Loop() {
				copy(dst, src)
			}
		})
	}
}

// BenchmarkFill writes a buffer with a store loop, the lowest cost to produce
// bytes that are not copied from somewhere.
func BenchmarkFill(b *testing.B) {
	dst := make([]byte, 32<<10)
	b.SetBytes(int64(len(dst)))
	for b.Loop() {
		for i := range dst {
			dst[i] = 'x'
		}
	}
}
