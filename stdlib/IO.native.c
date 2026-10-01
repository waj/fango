//go:build ignore

#include "fango_native.h"
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
static char *path_text(fango_string path) {
  if (path.length && memchr(path.data, 0, path.length))
    fango_panic("invalid argument");
  char *p = fango_alloc_atomic(path.length + 1);
  memcpy(p, path.data, path.length);
  p[path.length] = 0;
  return p;
}
bool FANGO_NATIVE(HasInput)(void) { return fango_has_input(); }
fango_string FANGO_NATIVE(ReadRawLine)(void) {
  return fango_to_valid_utf8(fango_read_line());
}
void FANGO_NATIVE(Write)(fango_string text) { fango_write(text); }
int64_t FANGO_NATIVE(ArgCount)(void) { return fango_arg_count(); }
fango_string FANGO_NATIVE(ArgAt)(int64_t index) {
  if (index < 0 || index >= fango_arg_count())
    fango_panic("argument index out of range");
  return fango_arg_at(index);
}
bool FANGO_NATIVE(PathExists)(fango_string path) {
  struct stat s;
  if (stat(path_text(path), &s) < 0) {
    if (errno == ENOENT || errno == ENOTDIR)
      return false;
    fango_panic(strerror(errno));
  }
  return true;
}
fango_string FANGO_NATIVE(ReadFileText)(fango_string path) {
  FILE *file = fopen(path_text(path), "rb");
  if (!file)
    fango_panic(strerror(errno));
  size_t size = 0, capacity = 4096;
  unsigned char *p = fango_alloc_atomic(capacity);
  for (;;) {
    if (size == capacity) {
      capacity *= 2;
      unsigned char *next = fango_alloc_atomic(capacity);
      memcpy(next, p, size);
      p = next;
    }
    size_t n = fread(p + size, 1, capacity - size, file);
    size += n;
    if (!n)
      break;
  }
  if (ferror(file))
    fango_panic(strerror(errno));
  fclose(file);
  return fango_to_valid_utf8((fango_string){p, size});
}
void FANGO_NATIVE(WriteFile)(fango_string path, fango_string text) {
  FILE *file = fopen(path_text(path), "wb");
  if (!file)
    fango_panic(strerror(errno));
  if (fwrite(text.data, 1, text.length, file) != text.length || fclose(file))
    fango_panic(strerror(errno));
}
void FANGO_NATIVE(Exit)(int64_t code) { exit((int)code); }
fango_string FANGO_NATIVE(LineEnding)(fango_string text) {
  if (text.length >= 2 && text.data[text.length - 2] == '\r' &&
      text.data[text.length - 1] == '\n')
    return fango_string_literal("\r\n");
  if (text.length && text.data[text.length - 1] == '\n')
    return fango_string_literal("\n");
  return (fango_string){0};
}
fango_string FANGO_NATIVE(LineText)(fango_string text) {
  fango_string end = FANGO_NATIVE(LineEnding)(text);
  text.length -= end.length;
  return text;
}
