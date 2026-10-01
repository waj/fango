#ifndef FANGO_LLVM_RUNTIME_H
#define FANGO_LLVM_RUNTIME_H
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#ifdef __cplusplus
extern "C" {
#endif
typedef struct {
  const unsigned char *data;
  size_t length;
} fango_string;
typedef struct {
  const unsigned char *data;
  size_t length;
} fango_bytes;
typedef void *fango_opaque;
typedef struct {
  bool failed;
  int32_t kind;
  fango_string location;
  fango_string message;
} fango_native_error;
void *fango_alloc(size_t size);
void *fango_alloc_atomic(size_t size);
void fango_register_finalizer(void *object, void (*finalizer)(void *, void *));
fango_string fango_string_copy(const void *data, size_t size);
fango_bytes fango_bytes_copy(const void *data, size_t size);
fango_string fango_string_literal(const char *text);
void fango_panic(const char *message);
bool fango_valid_utf8(fango_string text);
fango_string fango_to_valid_utf8(fango_string text);
uint32_t fango_decode_utf8(const unsigned char *data, size_t length,
                           size_t *width);
fango_string fango_from_char(uint32_t code);
void fango_write(fango_string text);
bool fango_has_input(void);
fango_string fango_read_line(void);
int64_t fango_arg_count(void);
fango_string fango_arg_at(int64_t index);
void fango_runtime_init(int argc, char **argv);
fango_native_error fango_io_error(int error, fango_string location);
fango_native_error fango_net_error(int error, fango_string location);
#ifdef __cplusplus
}
#endif
#endif
