//go:build ignore

#include "fango_native.h"
#include <stdio.h>
#include <string.h>
static fango_string names[128];
static size_t count;
int64_t FANGO_NATIVE(Mint)(fango_string name) {
  if (count == 128)
    fango_panic("fixture token overflow");
  names[count] = name;
  return count++;
}
fango_string FANGO_NATIVE(Describe)(int64_t token) {
  char prefix[64];
  int n = snprintf(prefix, sizeof(prefix), "token %lld = ", (long long)token);
  fango_string name = names[token];
  unsigned char *data = fango_alloc_atomic(n + name.length);
  memcpy(data, prefix, n);
  memcpy(data + n, name.data, name.length);
  return fango_string_copy(data, n + name.length);
}
int64_t FANGO_NATIVE(Next)(int64_t token) {
  fango_string name = names[token];
  unsigned char *data = fango_alloc_atomic(name.length + 1);
  memcpy(data, name.data, name.length);
  data[name.length] = '\'';
  return FANGO_NATIVE(Mint)(fango_string_copy(data, name.length + 1));
}
