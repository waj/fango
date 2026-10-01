#include "fango.hpp"
#include <cerrno>
#include <cstdlib>
#include <unistd.h>

static int fg_argc;
static char **fg_argv;
extern "C" void fango_runtime_init(int argc, char **argv) {
  GC_INIT();
  fg_argc = argc;
  fg_argv = argv;
}
extern "C" void *fango_alloc(size_t size) {
  void *p = GC_MALLOC(size ? size : 1);
  if (!p)
    fango_panic("out of memory");
  return p;
}
extern "C" void *fango_alloc_atomic(size_t size) {
  void *p = GC_MALLOC_ATOMIC(size ? size : 1);
  if (!p)
    fango_panic("out of memory");
  return p;
}
extern "C" void fango_register_finalizer(void *object,
                                         void (*finalizer)(void *, void *)) {
  GC_register_finalizer_no_order(object, finalizer, nullptr, nullptr, nullptr);
}
extern "C" void fango_panic(const char *message) {
  fprintf(stderr, "fango: %s\n", message);
  exit(2);
}
extern "C" fango_string fango_string_copy(const void *data, size_t size) {
  auto *p = static_cast<unsigned char *>(fango_alloc_atomic(size));
  if (size)
    memcpy(p, data, size);
  return {p, size};
}
extern "C" fango_bytes fango_bytes_copy(const void *data, size_t size) {
  auto s = fango_string_copy(data, size);
  return {s.data, s.length};
}
extern "C" fango_string fango_string_literal(const char *text) {
  return {reinterpret_cast<const unsigned char *>(text), strlen(text)};
}
extern "C" uint32_t fango_decode_utf8(const unsigned char *p, size_t n,
                                      size_t *width) {
  *width = 0;
  if (!n)
    return 0xFFFD;
  unsigned char a = p[0];
  *width = 1;
  if (a < 0x80)
    return a;
  int length = a >= 0xC2 && a <= 0xDF   ? 2
               : a >= 0xE0 && a <= 0xEF ? 3
               : a >= 0xF0 && a <= 0xF4 ? 4
                                        : 0;
  if (!length || n < size_t(length))
    return 0xFFFD;
  uint32_t r = a & ((1u << (7 - length)) - 1);
  for (int i = 1; i < length; i++) {
    if ((p[i] & 0xC0) != 0x80)
      return 0xFFFD;
    r = (r << 6) | (p[i] & 63);
  }
  if ((length == 2 && r < 0x80) || (length == 3 && r < 0x800) ||
      (length == 4 && r < 0x10000) || r > 0x10FFFF ||
      (r >= 0xD800 && r <= 0xDFFF))
    return 0xFFFD;
  *width = size_t(length);
  return r;
}
extern "C" bool fango_valid_utf8(fango_string text) {
  for (size_t i = 0; i < text.length;) {
    size_t width;
    uint32_t r = fango_decode_utf8(text.data + i, text.length - i, &width);
    if (r == 0xFFFD && width == 1)
      return false;
    i += width;
  }
  return true;
}
extern "C" fango_string fango_to_valid_utf8(fango_string text) {
  if (fango_valid_utf8(text))
    return text;
  std::string out;
  bool invalid = false;
  for (size_t i = 0; i < text.length;) {
    size_t width;
    uint32_t r = fango_decode_utf8(text.data + i, text.length - i, &width);
    if (r == 0xFFFD && width == 1) {
      if (!invalid)
        out += "\xEF\xBF\xBD";
      invalid = true;
    } else {
      out.append(reinterpret_cast<const char *>(text.data + i), width);
      invalid = false;
    }
    i += width;
  }
  return fango_string_copy(out.data(), out.size());
}
extern "C" fango_string fango_from_char(uint32_t r) {
  if (r > 0x10FFFF || (r >= 0xD800 && r <= 0xDFFF))
    fango_panic("invalid Char");
  unsigned char p[4];
  size_t n;
  if (r < 0x80) {
    p[0] = r;
    n = 1;
  } else if (r < 0x800) {
    p[0] = 0xC0 | (r >> 6);
    p[1] = 0x80 | (r & 63);
    n = 2;
  } else if (r < 0x10000) {
    p[0] = 0xE0 | (r >> 12);
    p[1] = 0x80 | ((r >> 6) & 63);
    p[2] = 0x80 | (r & 63);
    n = 3;
  } else {
    p[0] = 0xF0 | (r >> 18);
    p[1] = 0x80 | ((r >> 12) & 63);
    p[2] = 0x80 | ((r >> 6) & 63);
    p[3] = 0x80 | (r & 63);
    n = 4;
  }
  return fango_string_copy(p, n);
}
extern "C" void fango_write(fango_string text) {
  if (text.length && fwrite(text.data, 1, text.length, stdout) != text.length)
    fango_panic("write stdout failed");
  fflush(stdout);
}
extern "C" bool fango_has_input(void) {
  int c = getc(stdin);
  if (c == EOF)
    return false;
  ungetc(c, stdin);
  return true;
}
extern "C" fango_string fango_read_line(void) {
  std::string s;
  for (int c; (c = getc(stdin)) != EOF;) {
    s.push_back(char(c));
    if (c == '\n')
      break;
  }
  return fango_string_copy(s.data(), s.size());
}
extern "C" int64_t fango_arg_count(void) { return fg_argc - 1; }
extern "C" fango_string fango_arg_at(int64_t index) {
  if (index < 0 || index >= fg_argc - 1)
    return {};
  return fango_string_literal(fg_argv[index + 1]);
}
extern "C" fango_native_error fango_io_error(int error, fango_string location) {
  int kind = error == ENOENT                     ? 0
             : error == EACCES || error == EPERM ? 1
             : error == EEXIST                   ? 2
             : error == EISDIR                   ? 3
             : error == ENOTDIR                  ? 4
                                                 : 5;
  const char *messages[] = {"no such file or directory", "permission denied",
                            "file exists", "is a directory", "not a directory"};
  return {true, kind, location,
          fango_string_literal(kind < 5 ? messages[kind] : strerror(error))};
}
extern "C" fango_native_error fango_net_error(int error,
                                              fango_string location) {
  int kind = error == ECONNREFUSED                   ? 0
             : error == ECONNRESET || error == EPIPE ? 1
             : error == EADDRINUSE                   ? 2
             : error == ETIMEDOUT || error == EAGAIN ? 3
                                                     : 4;
  const char *messages[] = {"connection refused", "connection reset",
                            "address already in use", "timed out"};
  return {true, kind, location,
          fango_string_literal(kind < 4 ? messages[kind] : strerror(error))};
}
fg_string fg_show(int64_t value) {
  char text[32];
  auto end = std::to_chars(text, text + sizeof(text), value);
  return fango_string_copy(text, size_t(end.ptr - text));
}
fg_string fg_show(double value) {
  if (std::isnan(value))
    return fango_string_literal("NaN");
  if (std::isinf(value))
    return fango_string_literal(value < 0 ? "-Infinity" : "Infinity");
  if (value == 0)
    return fango_string_literal("0");
  char text[64];
  auto result = std::to_chars(text, text + sizeof(text), value,
                              std::chars_format::scientific);
  std::string s(text, result.ptr);
  bool negative = s[0] == '-';
  size_t begin = negative ? 1 : 0, e = s.find('e');
  int exponent = std::stoi(s.substr(e + 1));
  std::string digits;
  for (size_t i = begin; i < e; i++)
    if (s[i] != '.')
      digits += s[i];
  int n = exponent + 1;
  std::string out = negative ? "-" : "";
  if (n > 0 && n <= 21) {
    if (n >= int(digits.size()))
      out += digits + std::string(n - digits.size(), '0');
    else
      out += digits.substr(0, n) + "." + digits.substr(n);
  } else if (n > -6 && n <= 0)
    out += "0." + std::string(-n, '0') + digits;
  else {
    out += digits.substr(0, 1);
    if (digits.size() > 1)
      out += "." + digits.substr(1);
    out += "e";
    if (exponent >= 0)
      out += "+";
    out += std::to_string(exponent);
  }
  return fango_string_copy(out.data(), out.size());
}
fg_string fg_show(bool value) {
  return fango_string_literal(value ? "True" : "False");
}
fg_string fg_show(uint32_t value) { return fango_from_char(value); }
fg_string fg_append(fg_string a, fg_string b) {
  auto *p =
      static_cast<unsigned char *>(fango_alloc_atomic(a.length + b.length));
  if (a.length)
    memcpy(p, a.data, a.length);
  if (b.length)
    memcpy(p + a.length, b.data, b.length);
  return {p, a.length + b.length};
}
fg_string fg_quote(fg_string value) {
  std::string s = "\"";
  for (size_t i = 0; i < value.length;) {
    size_t width;
    uint32_t r = fango_decode_utf8(value.data + i, value.length - i, &width);
    switch (r) {
    case '\\':
      s += "\\\\";
      break;
    case '"':
      s += "\\\"";
      break;
    case '\n':
      s += "\\n";
      break;
    case '\t':
      s += "\\t";
      break;
    case '\r':
      s += "\\r";
      break;
    default:
      if (r < 32 || r == 127) {
        char p[16];
        snprintf(p, sizeof(p), "\\u{%X}", r);
        s += p;
      } else
        s.append(reinterpret_cast<const char *>(value.data + i), width);
    }
    i += width;
  }
  s += '"';
  return fango_string_copy(s.data(), s.size());
}
fg_string fg_show(fg_bytes value) {
  std::string s = "\"";
  for (size_t i = 0; i < value.length; i++) {
    unsigned char c = value.data[i];
    switch (c) {
    case '\\':
      s += "\\\\";
      break;
    case '"':
      s += "\\\"";
      break;
    case '\n':
      s += "\\n";
      break;
    case '\t':
      s += "\\t";
      break;
    case '\r':
      s += "\\r";
      break;
    default:
      if (c < 32 || c > 126) {
        char p[5];
        snprintf(p, sizeof(p), "\\x%02X", c);
        s += p;
      } else
        s += char(c);
    }
  }
  s += '"';
  return fango_string_copy(s.data(), s.size());
}
fg_exit *fg_suppress(fg_exit *primary, fg_exit *secondary) {
  if (!primary)
    return secondary;
  if (!secondary)
    return primary;
  fg_exit *out = fg_new(*primary);
  fg_list<fg_exit *> list;
  fg_list_node<fg_exit *> **slot = &list.node;
  for (auto old = primary->suppressed; !old.empty(); old = old.tail()) {
    *slot = fg_cons(old.head(), {}).node;
    slot = &(*slot)->next.node;
  }
  *slot = fg_cons(secondary, {}).node;
  out->suppressed = list;
  return out;
}
static fg_exit *fg_detach(fg_exit *exit) {
  auto *copy = fg_new(*exit);
  copy->target = nullptr;
  copy->suppressed = fg_list_map(
      exit->suppressed, [](fg_exit *child) { return fg_detach(child); });
  return copy;
}
fg_list<fg_exit *> fg_suppressed(fg_exit *exit) {
  return fg_list_map(exit->suppressed,
                     [](fg_exit *child) { return fg_detach(child); });
}
fg_bytes fg_bytes_slice(int64_t start, int64_t end, fg_bytes bytes) {
  if (start < 0)
    start = 0;
  if (end < start)
    end = start;
  if (uint64_t(start) > bytes.length)
    start = bytes.length;
  if (uint64_t(end) > bytes.length)
    end = bytes.length;
  if (end <= start)
    return {};
  return {bytes.data + start, size_t(end - start)};
}
fg_bytes fg_bytes_append(fg_bytes a, fg_bytes b) {
  auto s = fg_append({a.data, a.length}, {b.data, b.length});
  return {s.data, s.length};
}
fg_bytes fg_bytes_concat(fg_list<fg_bytes> input) {
  size_t length = 0;
  for (auto xs = input; !xs.empty(); xs = xs.tail())
    length += xs.head().length;
  auto *p = static_cast<unsigned char *>(fango_alloc_atomic(length));
  size_t offset = 0;
  for (auto xs = input; !xs.empty(); xs = xs.tail()) {
    auto s = xs.head();
    if (s.length)
      memcpy(p + offset, s.data, s.length);
    offset += s.length;
  }
  return {p, length};
}
int64_t fg_bytes_index(fg_bytes needle, int64_t offset, fg_bytes haystack) {
  if (offset < 0)
    offset = 0;
  if (uint64_t(offset) > haystack.length)
    return -1;
  for (size_t i = offset; i + needle.length <= haystack.length; i++)
    if (!needle.length ||
        memcmp(haystack.data + i, needle.data, needle.length) == 0)
      return i;
  return -1;
}
fg_bytes fg_bytes_from_list(fg_list<int64_t> input) {
  size_t length = 0;
  for (auto xs = input; !xs.empty(); xs = xs.tail())
    length++;
  auto *p = static_cast<unsigned char *>(fango_alloc_atomic(length));
  size_t i = 0;
  while (!input.empty()) {
    p[i++] = uint8_t(input.head());
    input = input.tail();
  }
  return {p, length};
}
fg_list<int64_t> fg_bytes_to_list(fg_bytes input) {
  fg_list<int64_t> result;
  for (size_t i = input.length; i; i--)
    result = fg_cons<int64_t>(input.data[i - 1], result);
  return result;
}
fg_string fg_string_from_list(fg_list<uint32_t> input) {
  std::string s;
  while (!input.empty()) {
    auto c = fango_from_char(input.head());
    s.append(reinterpret_cast<const char *>(c.data), c.length);
    input = input.tail();
  }
  return fango_string_copy(s.data(), s.size());
}
fg_string fg_string_concat(fg_list<fg_string> input) {
  size_t length = 0;
  for (auto xs = input; !xs.empty(); xs = xs.tail())
    length += xs.head().length;
  auto *p = static_cast<unsigned char *>(fango_alloc_atomic(length));
  size_t offset = 0;
  for (auto xs = input; !xs.empty(); xs = xs.tail()) {
    auto s = xs.head();
    if (s.length)
      memcpy(p + offset, s.data, s.length);
    offset += s.length;
  }
  return {p, length};
}
fg_string fg_string_lossy(fg_bytes input) {
  std::string s;
  for (size_t i = 0; i < input.length;) {
    size_t width;
    uint32_t r = fango_decode_utf8(input.data + i, input.length - i, &width);
    auto c = fango_from_char(r);
    s.append(reinterpret_cast<const char *>(c.data), c.length);
    i += width;
  }
  return fango_string_copy(s.data(), s.size());
}
