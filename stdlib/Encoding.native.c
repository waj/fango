//go:build ignore

#include "fango_native.h"
int64_t FANGO_NATIVE(Utf8CodeAt)(fango_bytes bytes, int64_t offset) {
  if (offset < 0 || (uint64_t)offset >= bytes.length)
    return -1;
  const unsigned char *p = bytes.data + offset;
  size_t n = bytes.length - offset;
  if (*p < 0x80)
    return *p;
  int needed = *p >= 0xC2 && *p <= 0xDF   ? 2
               : *p >= 0xE0 && *p <= 0xEF ? 3
               : *p >= 0xF0 && *p <= 0xF4 ? 4
                                          : 0;
  if (!needed)
    return -2;
  for (size_t i = 1; i < n && (int)i < needed; i++)
    if ((p[i] & 0xC0) != 0x80)
      return -2;
  if (n > 1 && ((*p == 0xE0 && p[1] < 0xA0) || (*p == 0xED && p[1] >= 0xA0) ||
                (*p == 0xF0 && p[1] < 0x90) || (*p == 0xF4 && p[1] >= 0x90)))
    return -2;
  if (n < (size_t)needed)
    return -3;
  size_t width;
  uint32_t r = fango_decode_utf8(p, n, &width);
  return r == 0xFFFD && width == 1 ? -2 : r;
}
