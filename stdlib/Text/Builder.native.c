//go:build ignore

#include "fango_native.h"
#include <string.h>

// The LLVM backend runs one thread, so versions share a buffer without a
// lock; bytes below any version's length are still never rewritten.
typedef struct {
  unsigned char *data;
  size_t length, capacity;
} text_buffer;

static text_buffer *buffer_with(const unsigned char *prefix, size_t length, size_t extra) {
  text_buffer *b = fango_alloc(sizeof(text_buffer));
  b->capacity = length + extra < 64 ? 64 : length + extra;
  b->data = fango_alloc_atomic(b->capacity);
  if (length)
    memcpy(b->data, prefix, length);
  b->length = length;
  return b;
}

static void put(text_buffer *b, const unsigned char *bytes, size_t n) {
  if (b->length + n > b->capacity) {
    size_t capacity = b->capacity * 2;
    if (capacity < b->length + n)
      capacity = b->length + n;
    unsigned char *data = fango_alloc_atomic(capacity);
    memcpy(data, b->data, b->length);
    b->data = data;
    b->capacity = capacity;
  }
  memcpy(b->data + b->length, bytes, n);
  b->length += n;
}

static fango_opaque extend(fango_opaque buffer, int64_t length, const unsigned char *bytes, size_t n) {
  text_buffer *b = buffer;
  if (b->length != (size_t)length)
    b = buffer_with(b->data, (size_t)length, n);
  put(b, bytes, n);
  return b;
}

static size_t encode(uint32_t c, unsigned char *out) {
  if (c < 0x80) {
    out[0] = c;
    return 1;
  }
  if (c < 0x800) {
    out[0] = 0xC0 | c >> 6;
    out[1] = 0x80 | (c & 0x3F);
    return 2;
  }
  if (c < 0x10000) {
    out[0] = 0xE0 | c >> 12;
    out[1] = 0x80 | (c >> 6 & 0x3F);
    out[2] = 0x80 | (c & 0x3F);
    return 3;
  }
  out[0] = 0xF0 | c >> 18;
  out[1] = 0x80 | (c >> 12 & 0x3F);
  out[2] = 0x80 | (c >> 6 & 0x3F);
  out[3] = 0x80 | (c & 0x3F);
  return 4;
}

fango_opaque FANGO_NATIVE(NewBuffer)(fango_string text) {
  text_buffer *b = buffer_with(NULL, 0, text.length);
  put(b, text.data, text.length);
  return b;
}

fango_opaque FANGO_NATIVE(ExtendBuffer)(fango_opaque buffer, int64_t length, fango_string text) {
  return extend(buffer, length, text.data, text.length);
}

fango_opaque FANGO_NATIVE(NewBufferChar)(uint32_t c) {
  unsigned char bytes[4];
  size_t n = encode(c, bytes);
  text_buffer *b = buffer_with(NULL, 0, n);
  put(b, bytes, n);
  return b;
}

fango_opaque FANGO_NATIVE(ExtendBufferChar)(fango_opaque buffer, int64_t length, uint32_t c) {
  unsigned char bytes[4];
  return extend(buffer, length, bytes, encode(c, bytes));
}

fango_string FANGO_NATIVE(BufferText)(fango_opaque buffer, int64_t length) {
  text_buffer *b = buffer;
  return fango_string_copy(b->data, (size_t)length);
}
