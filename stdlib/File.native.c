//go:build ignore

#include "fango_native.h"
#include <dirent.h>
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>
typedef struct {
  fango_string path;
  FILE *file;
  fango_string *entries;
  size_t count, next;
  bool closed;
  unsigned char *buffer;
  size_t from, to;
} file_handle;
static void finalize_file(void *value, void *context) {
  (void)context;
  file_handle *h = value;
  if (!h->closed && h->file) {
    h->closed = true;
    fclose(h->file);
  }
}
static char *path_text(fango_string path) {
  if (path.length && memchr(path.data, 0, path.length)) {
    errno = EINVAL;
    return NULL;
  }
  char *p = fango_alloc_atomic(path.length + 1);
  memcpy(p, path.data, path.length);
  p[path.length] = 0;
  return p;
}
static fango_native_error closed_error(void) {
  return (fango_native_error){
      true, 5, {0}, fango_string_literal("closed handle")};
}
static fango_native_error open_file(fango_string path, const char *mode,
                                    fango_opaque *result) {
  char *text = path_text(path);
  if (!text)
    return fango_io_error(errno, path);
  FILE *f = fopen(text, mode);
  if (!f)
    return fango_io_error(errno, path);
  file_handle *h = fango_alloc(sizeof(*h));
  *h = (file_handle){.path = path, .file = f};
  fango_register_finalizer(h, finalize_file);
  *result = h;
  return (fango_native_error){0};
}
fango_native_error FANGO_NATIVE(OpenRead)(fango_string path,
                                          fango_opaque *result) {
  return open_file(path, "rb", result);
}
fango_native_error FANGO_NATIVE(OpenWrite)(fango_string path,
                                           fango_opaque *result) {
  return open_file(path, "wb", result);
}
fango_native_error FANGO_NATIVE(OpenAppend)(fango_string path,
                                            fango_opaque *result) {
  return open_file(path, "ab", result);
}
fango_native_error FANGO_NATIVE(CloseHandle)(fango_opaque value) {
  file_handle *h = value;
  if (!h || h->closed)
    return closed_error();
  h->closed = true;
  if (fclose(h->file))
    return fango_io_error(errno, h->path);
  return (fango_native_error){0};
}
static fango_native_error fill_file(file_handle *h) {
  if (h->from < h->to)
    return (fango_native_error){0};
  if (!h->buffer)
    h->buffer = fango_alloc_atomic(4096);
  ssize_t n;
  do {
    n = read(fileno(h->file), h->buffer, 4096);
  } while (n < 0 && errno == EINTR);
  if (n < 0)
    return fango_io_error(errno, h->path);
  h->from = 0;
  h->to = n;
  return (fango_native_error){0};
}
fango_native_error FANGO_NATIVE(HandleHasInput)(fango_opaque value,
                                                bool *result) {
  file_handle *h = value;
  if (!h || h->closed)
    return closed_error();
  fango_native_error error = fill_file(h);
  if (error.failed)
    return error;
  *result = h->from < h->to;
  return (fango_native_error){0};
}
fango_native_error FANGO_NATIVE(ReadHandleLine)(fango_opaque value,
                                                fango_string *result) {
  file_handle *h = value;
  if (!h || h->closed)
    return closed_error();
  size_t size = 0, capacity = 4096;
  unsigned char *line = fango_alloc_atomic(capacity);
  for (;;) {
    fango_native_error error = fill_file(h);
    if (error.failed)
      return error;
    if (h->from == h->to)
      break;
    unsigned char c = h->buffer[h->from++];
    if (size == capacity) {
      capacity *= 2;
      unsigned char *next = fango_alloc_atomic(capacity);
      memcpy(next, line, size);
      line = next;
    }
    line[size++] = c;
    if (c == '\n')
      break;
  }
  *result = fango_to_valid_utf8((fango_string){line, size});
  return (fango_native_error){0};
}
fango_native_error FANGO_NATIVE(ReadHandleBytes)(fango_opaque value,
                                                 int64_t max,
                                                 fango_bytes *result) {
  file_handle *h = value;
  if (!h || h->closed)
    return closed_error();
  if (max <= 0) {
    *result = (fango_bytes){0};
    return (fango_native_error){0};
  }
  if (max > 65536)
    max = 65536;
  // A large read with nothing buffered goes straight into its result, as
  // Go's bufio does, rather than arriving one 4096-byte buffer at a time.
  if (h->from >= h->to && max > 4096) {
    unsigned char *data = fango_alloc_atomic(max);
    ssize_t n;
    do {
      n = read(fileno(h->file), data, max);
    } while (n < 0 && errno == EINTR);
    if (n < 0)
      return fango_io_error(errno, h->path);
    // A short read keeps only its own size, not the whole requested array.
    *result = n < max / 2 ? fango_bytes_copy(data, n) : (fango_bytes){data, (size_t)n};
    return (fango_native_error){0};
  }
  fango_native_error error = fill_file(h);
  if (error.failed)
    return error;
  size_t n = h->to - h->from;
  if (n > (size_t)max)
    n = max;
  *result = fango_bytes_copy(h->buffer + h->from, n);
  h->from += n;
  return (fango_native_error){0};
}
fango_native_error FANGO_NATIVE(WriteHandleBytes)(fango_opaque value,
                                                  fango_bytes bytes) {
  file_handle *h = value;
  if (!h || h->closed)
    return closed_error();
  if (fwrite(bytes.data, 1, bytes.length, h->file) != bytes.length ||
      fflush(h->file))
    return fango_io_error(errno, h->path);
  return (fango_native_error){0};
}
fango_native_error FANGO_NATIVE(WriteHandle)(fango_opaque value,
                                             fango_string text) {
  return FANGO_NATIVE(WriteHandleBytes)(value,
                                        (fango_bytes){text.data, text.length});
}
fango_native_error FANGO_NATIVE(ReadFileResult)(fango_string path,
                                                fango_string *result) {
  fango_opaque raw;
  fango_native_error err = open_file(path, "rb", &raw);
  if (err.failed)
    return err;
  file_handle *h = raw;
  struct stat info;
  if (fstat(fileno(h->file), &info)) {
    err = fango_io_error(errno, path);
    fclose(h->file);
    h->closed = true;
    return err;
  }
  if (S_ISDIR(info.st_mode)) {
    fclose(h->file);
    h->closed = true;
    return fango_io_error(EISDIR, path);
  }
  size_t size = 0, capacity = 4096;
  unsigned char *p = fango_alloc_atomic(capacity);
  for (;;) {
    if (size == capacity) {
      capacity *= 2;
      unsigned char *next = fango_alloc_atomic(capacity);
      memcpy(next, p, size);
      p = next;
    }
    size_t n = fread(p + size, 1, capacity - size, h->file);
    size += n;
    if (!n)
      break;
  }
  if (ferror(h->file))
    err = fango_io_error(errno, path);
  fclose(h->file);
  h->closed = true;
  if (err.failed)
    return err;
  *result = fango_to_valid_utf8((fango_string){p, size});
  return (fango_native_error){0};
}
fango_native_error FANGO_NATIVE(WriteFileResult)(fango_string path,
                                                 fango_string text) {
  fango_opaque raw;
  fango_native_error err = open_file(path, "wb", &raw);
  if (err.failed)
    return err;
  err = FANGO_NATIVE(WriteHandle)(raw, text);
  fango_native_error close = FANGO_NATIVE(CloseHandle)(raw);
  return err.failed ? err : close;
}
static int compare_names(const void *left, const void *right) {
  const fango_string *a = left, *b = right;
  size_t n = a->length < b->length ? a->length : b->length;
  int r = memcmp(a->data, b->data, n);
  return r ? r : (a->length > b->length) - (a->length < b->length);
}
fango_native_error FANGO_NATIVE(OpenDirectory)(fango_string path,
                                               fango_opaque *result) {
  char *text = path_text(path);
  if (!text)
    return fango_io_error(errno, path);
  DIR *dir = opendir(text);
  if (!dir)
    return fango_io_error(errno, path);
  file_handle *h = fango_alloc(sizeof(*h));
  *h = (file_handle){.path = path};
  size_t capacity = 16;
  h->entries = fango_alloc(sizeof(fango_string) * capacity);
  struct dirent *entry;
  errno = 0;
  while ((entry = readdir(dir))) {
    if (!strcmp(entry->d_name, ".") || !strcmp(entry->d_name, ".."))
      continue;
    if (h->count == capacity) {
      capacity *= 2;
      fango_string *next = fango_alloc(sizeof(fango_string) * capacity);
      memcpy(next, h->entries, sizeof(fango_string) * h->count);
      h->entries = next;
    }
    h->entries[h->count++] = fango_to_valid_utf8(
        fango_string_copy(entry->d_name, strlen(entry->d_name)));
    errno = 0;
  }
  int failure = errno;
  closedir(dir);
  if (failure)
    return fango_io_error(failure, path);
  qsort(h->entries, h->count, sizeof(fango_string), compare_names);
  *result = h;
  return (fango_native_error){0};
}
fango_native_error FANGO_NATIVE(ReadDirectoryEntry)(fango_opaque value,
                                                    fango_string *result) {
  file_handle *h = value;
  if (!h || h->closed)
    return closed_error();
  *result = h->next < h->count ? h->entries[h->next++] : (fango_string){0};
  return (fango_native_error){0};
}
fango_native_error FANGO_NATIVE(CloseDirectory)(fango_opaque value) {
  file_handle *h = value;
  if (!h || h->closed)
    return closed_error();
  h->closed = true;
  h->entries = NULL;
  return (fango_native_error){0};
}
fango_native_error FANGO_NATIVE(IsDirectoryPath)(fango_string path,
                                                 bool *result) {
  struct stat info;
  char *text = path_text(path);
  if (!text)
    return fango_io_error(errno, path);
  if (stat(text, &info))
    return fango_io_error(errno, path);
  *result = S_ISDIR(info.st_mode);
  return (fango_native_error){0};
}
