package native

import (
	cryptorand "crypto/rand"
	"encoding/binary"
)

// NextState advances one explicit glibc-constant 31-bit LCG state.
func NextState(state int64) int64 {
	return (1103515245*state + 12345) % 2147483648
}

// ValueAt maps an already-advanced state to an inclusive range.
func ValueAt(state, lo, hi int64) int64 {
	if hi < lo {
		lo, hi = hi, lo
	}
	span := uint64(hi) - uint64(lo) + 1
	if span == 0 {
		return lo + state
	}
	return lo + int64(uint64(state)%span)
}

func EntropySeed() int64 {
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		panic(err)
	}
	return int64(binary.LittleEndian.Uint64(b[:]))
}
