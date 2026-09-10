package native

import (
	cryptorand "crypto/rand"
	"encoding/binary"
)

func EntropySeed() int64 {
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		panic(err)
	}
	return int64(binary.LittleEndian.Uint64(b[:]))
}
