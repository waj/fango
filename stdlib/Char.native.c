//go:build ignore

#include "fango_native.h"
int64_t FANGO_NATIVE(ToCode)(uint32_t value) { return value; }
uint32_t FANGO_NATIVE(Scalar)(int64_t value) { return (uint32_t)value; }
