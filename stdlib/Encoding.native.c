//go:build ignore

#include "fango_native.h"
#include <string.h>
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

bool FANGO_NATIVE(Utf8MatchAt)(fango_bytes bytes, int64_t offset, fango_string text) {
  if (offset < 0 || (uint64_t)offset > bytes.length || text.length > bytes.length - offset)
    return false;
  return text.length == 0 || memcmp(bytes.data + offset, text.data, text.length) == 0;
}

int64_t FANGO_NATIVE(Utf8SpanUntil)(fango_bytes table, fango_bytes bytes, int64_t offset) {
  if (offset < 0 || table.length < 128)
    return offset;
  uint64_t i = (uint64_t)offset;
  while (i < bytes.length) {
    unsigned char first = bytes.data[i];
    if (first < 0x80) {
      if (table.data[first])
        break;
      i++;
      continue;
    }
    // Validate the sequence in place, with the bounds Utf8CodeAt applies:
    // no overlong forms, surrogates, or values above U+10FFFF.
    const unsigned char *p = bytes.data + i;
    uint64_t n = bytes.length - i;
    uint64_t needed = first >= 0xC2 && first <= 0xDF   ? 2
                      : first >= 0xE0 && first <= 0xEF ? 3
                      : first >= 0xF0 && first <= 0xF4 ? 4
                                                       : 0;
    if (!needed || n < needed)
      break;
    if ((first == 0xE0 && p[1] < 0xA0) || (first == 0xED && p[1] >= 0xA0) ||
        (first == 0xF0 && p[1] < 0x90) || (first == 0xF4 && p[1] >= 0x90))
      break;
    uint64_t k = 1;
    while (k < needed && (p[k] & 0xC0) == 0x80)
      k++;
    if (k < needed)
      break;
    i += needed;
  }
  return (int64_t)i;
}
