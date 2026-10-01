//go:build ignore

#include "fango_native.h"
#include <string.h>
#include <zlib.h>
typedef struct {
  z_stream stream;
  bool ended;
} compressor;
static void finalize_compressor(void *value, void *context) {
  (void)context;
  compressor *c = value;
  if (!c->ended) {
    c->ended = true;
    deflateEnd(&c->stream);
  }
}
fango_opaque FANGO_NATIVE(NewCompressor)(void) {
  compressor *c = fango_alloc(sizeof(*c));
  memset(c, 0, sizeof(*c));
  if (deflateInit2(&c->stream, Z_DEFAULT_COMPRESSION, Z_DEFLATED, 31, 8,
                   Z_DEFAULT_STRATEGY) != Z_OK)
    fango_panic("gzip initialization failed");
  fango_register_finalizer(c, finalize_compressor);
  return c;
}
static fango_bytes gzip_step(compressor *c, fango_bytes data, int mode) {
  if (c->ended)
    fango_panic("gzip compressor already finished");
  size_t capacity = 4096, size = 0;
  unsigned char *p = fango_alloc_atomic(capacity);
  c->stream.next_in = (Bytef *)data.data;
  c->stream.avail_in = data.length;
  int status;
  do {
    if (size == capacity) {
      capacity *= 2;
      unsigned char *next = fango_alloc_atomic(capacity);
      memcpy(next, p, size);
      p = next;
    }
    c->stream.next_out = p + size;
    c->stream.avail_out = capacity - size;
    status = deflate(&c->stream, mode);
    if (status != Z_OK && status != Z_STREAM_END && status != Z_BUF_ERROR)
      fango_panic("gzip compression failed");
    size = capacity - c->stream.avail_out;
  } while (c->stream.avail_in || c->stream.avail_out == 0 ||
           (mode == Z_FINISH && status != Z_STREAM_END));
  if (mode == Z_FINISH) {
    c->ended = true;
    deflateEnd(&c->stream);
  }
  c->stream.next_in = NULL;
  c->stream.next_out = NULL;
  return (fango_bytes){p, size};
}
fango_bytes FANGO_NATIVE(Push)(fango_opaque value, fango_bytes data) {
  return gzip_step(value, data, Z_NO_FLUSH);
}
fango_bytes FANGO_NATIVE(Flush)(fango_opaque value) {
  return gzip_step(value, (fango_bytes){0}, Z_SYNC_FLUSH);
}
fango_bytes FANGO_NATIVE(Finish)(fango_opaque value) {
  return gzip_step(value, (fango_bytes){0}, Z_FINISH);
}
