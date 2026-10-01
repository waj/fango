//go:build ignore

#include "fango_native.h"
#include <stdlib.h>
#include <string.h>
int64_t FANGO_NATIVE(EntropySeed)(void) {
  unsigned char bytes[8];
  arc4random_buf(bytes, 8);
  uint64_t value = 0;
  for (int i = 0; i < 8; i++)
    value |= (uint64_t)bytes[i] << (8 * i);
  int64_t result;
  memcpy(&result, &value, sizeof(result));
  return result;
}
