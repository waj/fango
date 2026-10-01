//go:build ignore

#include "fango_native.h"
int64_t FANGO_NATIVE(Number)(int64_t value) { return value; }
int64_t FANGO_NATIVE(Text)(int64_t value) { return value; }
int64_t FANGO_NATIVE(Value)(int64_t token) { return token; }
