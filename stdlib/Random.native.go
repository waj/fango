package native

import (
	cryptorand "crypto/rand"
	"encoding/binary"
)

// randomState is process-global. Random.runSeeded swaps and restores it around
// a handled computation, so nested uses remain lexical.
var randomState int64 = 5489

func SwapSeed(seed int64) int64 {
	old := randomState
	randomState = seed
	return old
}

// NextInt advances a glibc-constant 31-bit LCG and returns a draw in [lo, hi].
func NextInt(lo, hi int64) int64 {
	if hi < lo {
		lo, hi = hi, lo
	}
	randomState = (1103515245*randomState + 12345) % 2147483648
	span := uint64(hi) - uint64(lo) + 1
	if span == 0 {
		return lo + randomState
	}
	return lo + int64(uint64(randomState)%span)
}

func EntropySeed() int64 {
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		panic(err)
	}
	return int64(binary.LittleEndian.Uint64(b[:]))
}
