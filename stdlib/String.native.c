//go:build ignore

#include "fango_native.h"
#include <errno.h>
#include <math.h>
#include <stdlib.h>
#include <string.h>
int64_t FANGO_NATIVE(Length)(fango_string text) {
  int64_t n = 0;
  for (size_t i = 0; i < text.length;) {
    size_t width;
    fango_decode_utf8(text.data + i, text.length - i, &width);
    i += width;
    n++;
  }
  return n;
}
int64_t FANGO_NATIVE(ByteLength)(fango_string text) {
  return (int64_t)text.length;
}
int64_t FANGO_NATIVE(ByteAt)(int64_t index, fango_string text) {
  return index < 0 || (uint64_t)index >= text.length ? -1 : text.data[index];
}
fango_string FANGO_NATIVE(ByteSlice)(int64_t start, int64_t end,
                                     fango_string text) {
  if (start < 0 || end < start || (uint64_t)end > text.length)
    fango_panic("string slice out of range");
  return (fango_string){text.data + start, (size_t)(end - start)};
}
fango_string FANGO_NATIVE(Slice)(int64_t start, int64_t end,
                                 fango_string text) {
  if (start < 0)
    start = 0;
  if (end < 0)
    end = 0;
  size_t from = text.length, to = text.length;
  int64_t index = 0;
  for (size_t i = 0; i < text.length;) {
    if (index == start)
      from = i;
    if (index == end)
      to = i;
    size_t width;
    fango_decode_utf8(text.data + i, text.length - i, &width);
    i += width;
    index++;
  }
  if (to <= from)
    return (fango_string){0};
  return (fango_string){text.data + from, to - from};
}
uint32_t FANGO_NATIVE(FirstChar)(fango_string text) {
  size_t width;
  return fango_decode_utf8(text.data, text.length, &width);
}
fango_string FANGO_NATIVE(RestString)(fango_string text) {
  size_t width;
  fango_decode_utf8(text.data, text.length, &width);
  return (fango_string){text.data + width, text.length - width};
}
fango_string FANGO_NATIVE(FromChar)(uint32_t value) {
  return fango_from_char(value);
}
double FANGO_NATIVE(ToFloatNative)(fango_string text) {
  size_t i = 0;
  if (i < text.length && (text.data[i] == '+' || text.data[i] == '-'))
    i++;
  size_t start = i;
  bool nonzero = false;
  while (i < text.length && text.data[i] >= '0' && text.data[i] <= '9') {
    nonzero |= text.data[i] != '0';
    i++;
  }
  if (i == start)
    return NAN;
  if (i < text.length && text.data[i] == '.') {
    start = ++i;
    while (i < text.length && text.data[i] >= '0' && text.data[i] <= '9') {
      nonzero |= text.data[i] != '0';
      i++;
    }
    if (i == start)
      return NAN;
  }
  if (i < text.length && (text.data[i] == 'e' || text.data[i] == 'E')) {
    i++;
    if (i < text.length && (text.data[i] == '+' || text.data[i] == '-'))
      i++;
    start = i;
    while (i < text.length && text.data[i] >= '0' && text.data[i] <= '9')
      i++;
    if (i == start)
      return NAN;
  }
  if (i != text.length)
    return NAN;
  char *copy = fango_alloc_atomic(text.length + 1);
  memcpy(copy, text.data, text.length);
  copy[text.length] = 0;
  errno = 0;
  double value = strtod(copy, NULL);
  if (isinf(value) || (value == 0 && nonzero))
    return NAN;
  return value;
}
