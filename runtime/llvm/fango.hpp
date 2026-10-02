#ifndef FANGO_LLVM_RUNTIME_HPP
#define FANGO_LLVM_RUNTIME_HPP
#include "fango.h"
#include <bit>
#include <charconv>
#include <cmath>
#include <cstdio>
#include <cstring>
#include <gc/gc.h>
#include <limits>
#include <new>
#include <string>
#include <type_traits>
#include <typeinfo>
#include <utility>

struct fg_unit {};
using fg_string = fango_string;
using fg_bytes = fango_bytes;
struct fg_descriptor {
  fg_string name;
  bool inspectable;
  size_t count = 0;
  const fg_descriptor **arguments = nullptr;
};
template <class T> struct fg_metadata {
  static void describe(fg_descriptor &descriptor) {
    descriptor.name = fango_string_literal(typeid(T).name());
  }
};
template <class T> struct fg_inspection : std::false_type {};
template <> struct fg_inspection<int64_t> : std::true_type {};
template <> struct fg_inspection<double> : std::true_type {};
template <> struct fg_inspection<uint32_t> : std::true_type {};
template <> struct fg_inspection<bool> : std::true_type {};
template <> struct fg_inspection<fg_unit> : std::true_type {};
template <> struct fg_inspection<fg_string> : std::true_type {};
template <> struct fg_inspection<fg_bytes> : std::true_type {};
template <class T> const fg_descriptor *fg_type() {
  static const fg_descriptor descriptor = []() {
    fg_descriptor result{};
    result.inspectable = fg_inspection<T>::value;
    fg_metadata<T>::describe(result);
    return result;
  }();
  return &descriptor;
}
struct fg_any;
inline bool fg_descriptor_equal(const fg_descriptor *a,
                                const fg_descriptor *b) {
  if (a == b)
    return true;
  if (!a || !b || a->name.length != b->name.length ||
      memcmp(a->name.data, b->name.data, a->name.length) ||
      a->count != b->count)
    return false;
  for (size_t i = 0; i < a->count; i++)
    if (!fg_descriptor_equal(a->arguments[i], b->arguments[i]))
      return false;
  return true;
}
inline const fg_descriptor *fg_applied(fg_string name, size_t count,
                                       const fg_descriptor **arguments,
                                       bool safe) {
  auto *result =
      static_cast<fg_descriptor *>(fango_alloc(sizeof(fg_descriptor)));
  for (size_t i = 0; i < count; i++)
    safe = safe && arguments[i]->inspectable;
  new (result) fg_descriptor{name, safe, count, arguments};
  return result;
}
template <class T> struct fg_shape {
  using type = T;
};
template <class To, class From> To fg_convert(From value);
struct fg_any {
  const fg_descriptor *type;
  void *value;
  const fg_descriptor *storage = nullptr;
};
template <class T> fg_any fg_box(T value) {
  using Shape = typename fg_shape<T>::type;
  static_assert(std::is_trivially_destructible_v<Shape>);
  auto *storage = static_cast<Shape *>(fango_alloc(sizeof(Shape)));
  new (storage) Shape(fg_convert<Shape>(value));
  return {fg_type<T>(), storage, fg_type<Shape>()};
}
template <class T> T fg_unbox(fg_any value) {
  if (!fg_descriptor_equal(value.type, fg_type<T>()))
    fango_panic("invalid opaque value type");
  using Shape = typename fg_shape<T>::type;
  return fg_convert<T>(*static_cast<Shape *>(value.value));
}
template <class T> struct fg_list_node;
template <class T> struct fg_list {
  fg_list_node<T> *node = nullptr;
  bool empty() const { return node == nullptr; }
  T head() const;
  fg_list tail() const;
};
template <class T> struct fg_inspection<fg_list<T>> : fg_inspection<T> {};
template <class T> struct fg_metadata<fg_list<T>> {
  static void describe(fg_descriptor &descriptor) {
    static const fg_descriptor *arguments[] = {fg_type<T>()};
    descriptor.name = fango_string_literal("List.List");
    descriptor.count = 1;
    descriptor.arguments = arguments;
  }
};
template <class T> struct fg_shape<fg_list<T>> {
  using type = fg_list<fg_any>;
};
template <class T> struct fg_list_node {
  T value;
  fg_list<T> next;
};
template <class T> T fg_list<T>::head() const {
  if (!node)
    fango_panic("empty list");
  return node->value;
}
template <class T> fg_list<T> fg_list<T>::tail() const {
  if (!node)
    fango_panic("empty list");
  return node->next;
}
template <class T> fg_list<T> fg_cons(T value, fg_list<T> next) {
  auto *node =
      static_cast<fg_list_node<T> *>(fango_alloc(sizeof(fg_list_node<T>)));
  new (node) fg_list_node<T>{value, next};
  return {node};
}
template <class T, class F> auto fg_list_map(fg_list<T> input, F function) {
  using U = decltype(function(input.head()));
  fg_list<U> output;
  fg_list_node<U> **slot = &output.node;
  while (!input.empty()) {
    auto *node =
        static_cast<fg_list_node<U> *>(fango_alloc(sizeof(fg_list_node<U>)));
    new (node) fg_list_node<U>{function(input.head()), {}};
    *slot = node;
    slot = &node->next.node;
    input = input.tail();
  }
  return output;
}
struct fg_exit;
struct fg_evidence;
struct fg_erased_op;
struct fg_exit {
  fg_evidence *target;
  fg_string effect, operation;
  int index;
  size_t count;
  fg_any *payload;
  fg_list<fg_exit *> suppressed;
};
template <class T> struct fg_out {
  T value{};
  fg_exit *exit = nullptr;
};
template <class T> T fg_normal(fg_out<T> out) {
  if (out.exit)
    fango_panic("exit from Direct call");
  return out.value;
}
template <class T> fg_out<T> fg_ok(T value) { return {value, nullptr}; }
template <class T> T *fg_new(T value) {
  static_assert(std::is_trivially_destructible_v<T>);
  auto *p = static_cast<T *>(fango_alloc(sizeof(T)));
  new (p) T(value);
  // Keep aggregate inputs visible on the stack across a collecting allocation.
  // Optimized copies can otherwise retain their pointers only in SIMD registers.
  GC_reachable_here(&value);
  return p;
}
struct fg_evidence {
  fg_string name;
  size_t count;
  const fg_descriptor **arguments;
  const fg_descriptor *layout = nullptr;
  fg_erased_op *operations = nullptr;
};
struct fg_erased_op {
  void *environment;
  fg_out<fg_any> (*invoke)(void *, const fg_descriptor **, fg_any *);
};
struct fg_binding {
  fg_evidence *evidence = nullptr;
};
struct fg_row_layer;
struct fg_row {
  fg_binding first{}, second{};
  fg_row_layer *rest = nullptr;
};
struct fg_row_layer {
  fg_binding first, second;
  fg_row_layer *rest;
};
inline bool fg_same_string(fg_string a, fg_string b) {
  return a.length == b.length &&
         (a.length == 0 || memcmp(a.data, b.data, a.length) == 0);
}
inline bool fg_matches(fg_evidence *e, fg_string name, size_t count,
                       const fg_descriptor **args) {
  if (!e || !fg_same_string(e->name, name) || e->count != count)
    return false;
  for (size_t i = 0; i < count; i++)
    if (!fg_descriptor_equal(e->arguments[i], args[i]))
      return false;
  return true;
}
inline fg_evidence *fg_lookup(fg_row row, fg_string name, size_t count,
                              const fg_descriptor **args) {
  if (fg_matches(row.first.evidence, name, count, args))
    return row.first.evidence;
  if (fg_matches(row.second.evidence, name, count, args))
    return row.second.evidence;
  for (auto *p = row.rest; p; p = p->rest) {
    if (fg_matches(p->first.evidence, name, count, args))
      return p->first.evidence;
    if (fg_matches(p->second.evidence, name, count, args))
      return p->second.evidence;
  }
  fango_panic("missing residual effect evidence");
  return nullptr;
}
inline fg_row fg_extend(fg_row row, fg_evidence *evidence) {
  if (row.first.evidence == evidence)
    return row;
  if (!row.second.evidence) {
    row.second = row.first;
    row.first = {evidence};
    return row;
  }
  row.rest = fg_new(fg_row_layer{row.second, {}, row.rest});
  row.second = row.first;
  row.first = {evidence};
  return row;
}
template <class Signature> struct fg_fn;
template <class R, class... A> struct fg_fn<R(A...)> {
  void *environment = nullptr;
  R (*direct)(void *, A...) = nullptr;
  fg_out<R> (*exiting)(void *, A...) = nullptr;
  R call(A... a) const {
    if (!direct)
      fango_panic("unavailable Direct function");
    return direct(environment, a...);
  }
  fg_out<R> call_exit(A... a) const {
    if (exiting)
      return exiting(environment, a...);
    return fg_ok(call(a...));
  }
};
template <class Signature> struct fg_function_builder;
template <class R, class... A> struct fg_function_builder<R(A...)> {
  template <class F> static fg_fn<R(A...)> make(F function) {
    static_assert(std::is_trivially_destructible_v<F>);
    F *environment = fg_new(function);
    return {environment,
            [](void *env, A... a) -> R {
              return fg_normal((*static_cast<F *>(env))(a...));
            },
            [](void *env, A... a) -> fg_out<R> {
              return (*static_cast<F *>(env))(a...);
            }};
  }
};
template <class Signature, class F> fg_fn<Signature> fg_make_fn(F function) {
  return fg_function_builder<Signature>::make(function);
}
template <class R, class... A>
const fg_descriptor *fg_type_function(fg_fn<R(A...)> *) {
  static const fg_descriptor descriptor{
      fango_string_literal(typeid(fg_fn<R(A...)>).name()), false};
  return &descriptor;
}
inline fg_any fg_box(fg_any value) { return value; }
template <class T> fg_any fg_box_typed(T value, const fg_descriptor *type) {
  auto result = fg_box(value);
  result.type = type;
  return result;
}
template <> inline fg_any fg_unbox<fg_any>(fg_any value) { return value; }
template <class R, class... A, size_t... I>
fg_out<fg_any> fg_erased_invoke(fg_fn<R(A...)> value, fg_any *arguments,
                                std::index_sequence<I...>) {
  auto out = value.call_exit(fg_convert<A>(arguments[I])...);
  if (out.exit)
    return {{}, out.exit};
  return {fg_box(out.value), nullptr};
}
template <class R, class... A>
fg_erased_op fg_erase_operation(fg_fn<R(A...)> value) {
  return {fg_new(value),
          [](void *env, const fg_descriptor **,
             fg_any *arguments) -> fg_out<fg_any> {
            return fg_erased_invoke(*static_cast<fg_fn<R(A...)> *>(env),
                                    arguments, std::index_sequence_for<A...>{});
          }};
}
fg_exit *fg_suppress(fg_exit *primary, fg_exit *secondary);
fg_list<fg_exit *> fg_suppressed(fg_exit *exit);
fg_string fg_show(int64_t value);
fg_string fg_show(double value);
fg_string fg_show(bool value);
fg_string fg_show(uint32_t value);
inline fg_string fg_show(fg_string value) { return value; }
inline fg_string fg_show(fg_unit) { return fango_string_literal("()"); }
fg_string fg_append(fg_string a, fg_string b);
fg_string fg_quote(fg_string value);
fg_string fg_show(fg_bytes value);
// fango_string and fango_bytes deliberately have separate C struct identities.
inline int fg_compare(fg_string a, fg_string b) {
  size_t n = a.length < b.length ? a.length : b.length;
  int v = n ? memcmp(a.data, b.data, n) : 0;
  return v ? v : (a.length > b.length) - (a.length < b.length);
}
inline bool fg_eq(fg_string a, fg_string b) { return fg_same_string(a, b); }
inline bool fg_eq(fg_bytes a, fg_bytes b) {
  return a.length == b.length &&
         (!a.length || memcmp(a.data, b.data, a.length) == 0);
}
inline bool fg_eq(fg_unit, fg_unit) { return true; }
template <class T> bool fg_eq(T a, T b) {
  if constexpr (requires { a == b; })
    return a == b;
  else {
    fango_panic("equality unavailable for this type");
    return false;
  }
}
template <class T> fg_string fg_show(T) {
  fango_panic("display unavailable for this type");
  return {};
}
template <class T> bool fg_eq(fg_list<T> a, fg_list<T> b) {
  while (!a.empty() && !b.empty()) {
    if (!fg_eq(a.head(), b.head()))
      return false;
    a = a.tail();
    b = b.tail();
  }
  return a.empty() && b.empty();
}
template <class T> fg_string fg_show_nested(T value) { return fg_show(value); }
template <class T> fg_string fg_show(fg_list<T> value) {
  std::string text = "[";
  bool first = true;
  while (!value.empty()) {
    if (!first)
      text += ", ";
    first = false;
    auto s = fg_show_nested(value.head());
    text.append(reinterpret_cast<const char *>(s.data), s.length);
    value = value.tail();
  }
  text += "]";
  return fango_string_copy(text.data(), text.size());
}
template <class To, class From> To fg_convert(From value);
template <class To, class From> struct fg_conversion {
  static To apply(From value) { return To(value); }
};
template <class To, class From>
struct fg_conversion<fg_list<To>, fg_list<From>> {
  static fg_list<To> apply(fg_list<From> value) {
    return fg_list_map(value, [](From item) { return fg_convert<To>(item); });
  }
};
template <class ToR, class... ToA, class FromR, class... FromA>
struct fg_conversion<fg_fn<ToR(ToA...)>, fg_fn<FromR(FromA...)>> {
  static fg_fn<ToR(ToA...)> apply(fg_fn<FromR(FromA...)> value) {
    static_assert(sizeof...(ToA) == sizeof...(FromA));
    return fg_make_fn<ToR(ToA...)>([value](ToA... args) -> fg_out<ToR> {
      auto out = value.call_exit(fg_convert<FromA>(args)...);
      if (out.exit)
        return {{}, out.exit};
      return {fg_convert<ToR>(out.value), nullptr};
    });
  }
};
template <class To, class From> To fg_convert(From value) {
  if constexpr (std::is_same_v<To, From>)
    return value;
  else if constexpr (std::is_same_v<To, fg_any>)
    return fg_box(value);
  else if constexpr (std::is_same_v<From, fg_any>) {
    using Shape = typename fg_shape<To>::type;
    if (!fg_descriptor_equal(value.storage, fg_type<Shape>()))
      fango_panic("invalid opaque value layout");
    return fg_convert<To>(*static_cast<Shape *>(value.value));
  } else
    return fg_conversion<To, From>::apply(value);
}
inline int64_t fg_add(int64_t a, int64_t b) {
  return std::bit_cast<int64_t>(uint64_t(a) + uint64_t(b));
}
inline int64_t fg_sub(int64_t a, int64_t b) {
  return std::bit_cast<int64_t>(uint64_t(a) - uint64_t(b));
}
inline int64_t fg_mul(int64_t a, int64_t b) {
  return std::bit_cast<int64_t>(uint64_t(a) * uint64_t(b));
}
inline double fg_add(double a, double b) { return a + b; }
inline double fg_sub(double a, double b) { return a - b; }
inline double fg_mul(double a, double b) { return a * b; }
inline int64_t fg_quotient(int64_t a, int64_t b) {
  if (!b)
    fango_panic("integer divide by zero");
  if (a == INT64_MIN && b == -1)
    return a;
  return a / b;
}
inline int64_t fg_remainder(int64_t a, int64_t b) {
  if (!b)
    fango_panic("integer divide by zero");
  if (a == INT64_MIN && b == -1)
    return 0;
  return a % b;
}
fg_bytes fg_bytes_slice(int64_t start, int64_t end, fg_bytes bytes);
fg_bytes fg_bytes_append(fg_bytes a, fg_bytes b);
fg_bytes fg_bytes_concat(fg_list<fg_bytes> input);
int64_t fg_bytes_index(fg_bytes needle, int64_t offset, fg_bytes haystack);
fg_bytes fg_bytes_from_list(fg_list<int64_t> input);
fg_list<int64_t> fg_bytes_to_list(fg_bytes input);
fg_string fg_string_from_list(fg_list<uint32_t> input);
fg_string fg_string_concat(fg_list<fg_string> input);
fg_string fg_string_lossy(fg_bytes input);
#endif
