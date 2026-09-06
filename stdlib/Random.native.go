package native

import "github.com/waj/fango/runtime/fangort"

// The interpreter implementations of the Random templates delegate to
// fangort's shared PRNG cell, the same one compiled templates use, so seeded
// sequences match across backends.

func RandomSwap(seed int64) int64 { return fangort.RandomSwap(seed) }

func RandomInt(lo, hi int64) int64 { return fangort.RandomInt(lo, hi) }

func RandomEntropy() int64 { return fangort.RandomEntropy(fangort.UnitValue) }
