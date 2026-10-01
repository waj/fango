//go:build ignore

#include "fango_native.h"
typedef struct {
  fango_opaque value;
} cell;
fango_opaque FANGO_NATIVE(Box)(fango_opaque value) {
  cell *handle = fango_alloc(sizeof(*handle));
  handle->value = value;
  return handle;
}
fango_opaque FANGO_NATIVE(Read)(fango_opaque handle) {
  return ((cell *)handle)->value;
}
void FANGO_NATIVE(Write)(fango_opaque handle, fango_opaque value) {
  ((cell *)handle)->value = value;
}
