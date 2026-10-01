//go:build ignore

#include "fango_native.h"
static int64_t values[128];
static bool live[128];
static int64_t next, active;
int64_t FANGO_NATIVE(OpenNative)(int64_t value) {
  if (++next == 128)
    fango_panic("fixture connection overflow");
  values[next] = value;
  live[next] = true;
  active++;
  return next;
}
void FANGO_NATIVE(CloseNative)(int64_t handle) {
  if (!live[handle])
    fango_panic("connection released twice");
  live[handle] = false;
  active--;
}
int64_t FANGO_NATIVE(ReadValue)(int64_t handle) {
  if (!live[handle])
    fango_panic("connection used after release");
  return values[handle];
}
int64_t FANGO_NATIVE(Active)(void) { return active; }
