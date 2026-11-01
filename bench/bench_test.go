// Package bench compares unsure's data structures against other libraries.
// It's a separate module so the main module has no dependencies.
package bench

import (
	"encoding/binary"
	"math"
	"runtime"
)

var sink bool

// makeKeys returns n distinct 16-byte keys. Keys from different seeds don't overlap.
func makeKeys(seed uint64, n int) [][]byte {
	keys := make([][]byte, n)
	buf := make([]byte, 16*n)
	for i := range keys {
		k := buf[i*16 : (i+1)*16 : (i+1)*16]
		binary.LittleEndian.PutUint64(k[:8], seed)
		binary.LittleEndian.PutUint64(k[8:], uint64(i))
		keys[i] = k
	}
	return keys
}

// allocated returns the number of heap bytes allocated by fn.
// Takes the min of several runs, since unrelated runtime allocations
// may occasionally land within a single measurement.
func allocated[T any](fn func() T) (T, int) {
	var v T
	bytes := math.MaxInt
	for range 3 {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		v = fn()
		runtime.ReadMemStats(&after)
		bytes = min(bytes, int(after.TotalAlloc-before.TotalAlloc))
	}
	return v, bytes
}
