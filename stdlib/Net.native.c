//go:build ignore

#include "fango_native.h"
#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <netdb.h>
#include <poll.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/time.h>
#include <time.h>
#include <unistd.h>
typedef struct {
  int fd;
  bool closed;
  fango_string address;
  int64_t read_deadline, write_deadline;
  unsigned char *buffer;
  size_t from, to;
} socket_handle;
static void finalize_socket(void *value, void *context) {
  (void)context;
  socket_handle *h = value;
  if (!h->closed) {
    h->closed = true;
    close(h->fd);
  }
}
static int64_t now_ms(void) {
  struct timespec t;
  clock_gettime(CLOCK_MONOTONIC, &t);
  return t.tv_sec * 1000 + t.tv_nsec / 1000000;
}
static fango_native_error closed_error(const char *text) {
  return (fango_native_error){true, 4, {0}, fango_string_literal(text)};
}
static fango_native_error wait_socket(socket_handle *h, bool writing) {
  int64_t deadline = writing ? h->write_deadline : h->read_deadline;
  int timeout = -1;
  if (deadline) {
    int64_t now = now_ms();
    if (deadline <= now)
      return fango_net_error(ETIMEDOUT, h->address);
    int64_t remaining = deadline - now;
    timeout = remaining > INT32_MAX ? INT32_MAX : (int)remaining;
  }
  struct pollfd p = {h->fd, writing ? POLLOUT : POLLIN, 0};
  int n;
  do {
    n = poll(&p, 1, timeout);
  } while (n < 0 && errno == EINTR);
  if (n == 0)
    return fango_net_error(ETIMEDOUT, h->address);
  if (n < 0)
    return fango_net_error(errno, h->address);
  return (fango_native_error){0};
}
static fango_string endpoint(const struct sockaddr *address, socklen_t length) {
  char host[NI_MAXHOST], service[NI_MAXSERV], text[NI_MAXHOST + NI_MAXSERV + 4];
  if (getnameinfo(address, length, host, sizeof(host), service, sizeof(service),
                  NI_NUMERICHOST | NI_NUMERICSERV))
    return (fango_string){0};
  snprintf(text, sizeof(text),
           address->sa_family == AF_INET6 ? "[%s]:%s" : "%s:%s", host, service);
  return fango_string_copy(text, strlen(text));
}
static socket_handle *new_socket(int fd) {
  socket_handle *h = fango_alloc(sizeof(*h));
  *h = (socket_handle){.fd = fd};
  struct sockaddr_storage address;
  socklen_t length = sizeof(address);
  if (!getpeername(fd, (struct sockaddr *)&address, &length) ||
      !getsockname(fd, (struct sockaddr *)&address, &length))
    h->address = endpoint((struct sockaddr *)&address, length);
  fango_register_finalizer(h, finalize_socket);
  int one = 1;
  setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &one, sizeof(one));
  int flags = fcntl(fd, F_GETFL, 0);
  if (flags < 0 || fcntl(fd, F_SETFL, flags | O_NONBLOCK) < 0)
    fango_panic("socket nonblocking setup failed");
  return h;
}
fango_native_error FANGO_NATIVE(Listen)(int64_t port, fango_opaque *result) {
  if (port < 0 || port > 65535)
    return closed_error("invalid port");
  int fd = socket(AF_INET6, SOCK_STREAM, 0);
  if (fd < 0)
    return fango_net_error(errno, (fango_string){0});
  int one = 1, zero = 0;
  setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &one, sizeof(one));
  setsockopt(fd, IPPROTO_IPV6, IPV6_V6ONLY, &zero, sizeof(zero));
  struct sockaddr_in6 a = {.sin6_family = AF_INET6,
                           .sin6_port = htons(port),
                           .sin6_addr = IN6ADDR_ANY_INIT};
  if (bind(fd, (struct sockaddr *)&a, sizeof(a)) || listen(fd, SOMAXCONN)) {
    int e = errno;
    close(fd);
    return fango_net_error(e, endpoint((struct sockaddr *)&a, sizeof(a)));
  }
  *result = new_socket(fd);
  return (fango_native_error){0};
}
fango_native_error FANGO_NATIVE(CloseListener)(fango_opaque value) {
  socket_handle *h = value;
  if (!h)
    return closed_error("closed listener");
  if (h->closed)
    return (fango_native_error){0};
  h->closed = true;
  if (close(h->fd))
    return fango_net_error(errno, h->address);
  return (fango_native_error){0};
}
bool FANGO_NATIVE(ListenerStopped)(fango_opaque value) {
  socket_handle *h = value;
  return !h || h->closed;
}
fango_native_error FANGO_NATIVE(AcceptConnection)(fango_opaque value,
                                                  fango_opaque *result) {
  socket_handle *h = value;
  if (!h || h->closed)
    return closed_error("closed listener");
  int fd;
  do {
    fd = accept(h->fd, NULL, NULL);
  } while (fd < 0 && errno == EINTR);
  if (fd < 0)
    return fango_net_error(errno, h->address);
  *result = new_socket(fd);
  return (fango_native_error){0};
}
fango_native_error FANGO_NATIVE(Dial)(fango_string host, int64_t port,
                                      fango_opaque *result) {
  char service[32];
  snprintf(service, sizeof(service), "%lld", (long long)port);
  char *hostname = fango_alloc_atomic(host.length + 1);
  memcpy(hostname, host.data, host.length);
  hostname[host.length] = 0;
  struct addrinfo hints = {.ai_socktype = SOCK_STREAM}, *addresses;
  int n = getaddrinfo(hostname, service, &hints, &addresses);
  if (n)
    return closed_error(gai_strerror(n));
  int fd = -1, error = 0;
  fango_string address = {0};
  for (struct addrinfo *a = addresses; a; a = a->ai_next) {
    address = endpoint(a->ai_addr, a->ai_addrlen);
    fd = socket(a->ai_family, a->ai_socktype, a->ai_protocol);
    if (fd < 0) {
      error = errno;
      continue;
    }
    if (!connect(fd, a->ai_addr, a->ai_addrlen))
      break;
    error = errno;
    close(fd);
    fd = -1;
  }
  freeaddrinfo(addresses);
  if (fd < 0)
    return fango_net_error(error, address);
  socket_handle *h = new_socket(fd);
  *result = h;
  return (fango_native_error){0};
}
fango_native_error FANGO_NATIVE(CloseConnection)(fango_opaque value) {
  socket_handle *h = value;
  if (!h)
    return closed_error("closed connection");
  return FANGO_NATIVE(CloseListener)(h);
}
static fango_native_error fill(socket_handle *h) {
  if (h->from < h->to)
    return (fango_native_error){0};
  ssize_t n;
  if (!h->buffer)
    h->buffer = fango_alloc_atomic(4096);
  for (;;) {
    fango_native_error e = wait_socket(h, false);
    if (e.failed)
      return e;
    n = recv(h->fd, h->buffer, 4096, 0);
    if (n >= 0 || (errno != EINTR && errno != EAGAIN && errno != EWOULDBLOCK))
      break;
  }
  if (n < 0)
    return fango_net_error(errno, h->address);
  h->from = 0;
  h->to = n;
  return (fango_native_error){0};
}
fango_native_error FANGO_NATIVE(ConnectionHasInput)(fango_opaque value,
                                                    bool *result) {
  socket_handle *h = value;
  if (!h || h->closed)
    return closed_error("closed connection");
  fango_native_error e = fill(h);
  if (e.failed)
    return e;
  *result = h->from < h->to;
  return (fango_native_error){0};
}
fango_native_error FANGO_NATIVE(ReadConnectionBytes)(fango_opaque value,
                                                     int64_t max,
                                                     fango_bytes *result) {
  socket_handle *h = value;
  if (!h || h->closed)
    return closed_error("closed connection");
  if (max <= 0) {
    *result = (fango_bytes){0};
    return (fango_native_error){0};
  }
  if (max > 65536)
    max = 65536;
  fango_native_error e = fill(h);
  if (e.failed)
    return e;
  size_t n = h->to - h->from;
  if (n > (size_t)max)
    n = max;
  *result = fango_bytes_copy(h->buffer + h->from, n);
  h->from += n;
  return (fango_native_error){0};
}
fango_native_error FANGO_NATIVE(WriteConnectionBytes)(fango_opaque value,
                                                      fango_bytes data) {
  socket_handle *h = value;
  if (!h || h->closed)
    return closed_error("closed connection");
  size_t i = 0;
  while (i < data.length) {
    fango_native_error e = wait_socket(h, true);
    if (e.failed)
      return e;
    ssize_t n = send(h->fd, data.data + i, data.length - i, 0);
    if (n < 0) {
      if (errno == EINTR || errno == EAGAIN || errno == EWOULDBLOCK)
        continue;
      return fango_net_error(errno, h->address);
    }
    if (n == 0)
      return closed_error("socket write made no progress");
    i += n;
  }
  return (fango_native_error){0};
}
fango_native_error FANGO_NATIVE(SetReadDeadline)(fango_opaque value,
                                                 int64_t millis) {
  socket_handle *h = value;
  if (!h || h->closed)
    return closed_error("closed connection");
  uint64_t bits = (uint64_t)millis * 1000000;
  int64_t duration;
  memcpy(&duration, &bits, sizeof(duration));
  h->read_deadline = millis > 0 ? now_ms() + duration / 1000000 : 0;
  return (fango_native_error){0};
}
fango_native_error FANGO_NATIVE(SetWriteDeadline)(fango_opaque value,
                                                  int64_t millis) {
  socket_handle *h = value;
  if (!h || h->closed)
    return closed_error("closed connection");
  uint64_t bits = (uint64_t)millis * 1000000;
  int64_t duration;
  memcpy(&duration, &bits, sizeof(duration));
  h->write_deadline = millis > 0 ? now_ms() + duration / 1000000 : 0;
  return (fango_native_error){0};
}
// These declarations are private to Net's Async APIs. Reachability rejects
// them before emission; their definitions keep the C module contract complete.
fango_native_error FANGO_NATIVE(AcceptConnectionAsync)(fango_opaque token,
                                                       fango_opaque value,
                                                       fango_opaque *result) {
  (void)token;
  (void)value;
  (void)result;
  fango_panic("Async is unsupported by LLVM");
  return (fango_native_error){0};
}
fango_native_error FANGO_NATIVE(ReadConnectionBytesAsync)(fango_opaque token,
                                                          fango_opaque value,
                                                          int64_t max,
                                                          fango_bytes *result) {
  (void)token;
  (void)value;
  (void)max;
  (void)result;
  fango_panic("Async is unsupported by LLVM");
  return (fango_native_error){0};
}
fango_native_error FANGO_NATIVE(WriteConnectionBytesAsync)(fango_opaque token,
                                                           fango_opaque value,
                                                           fango_bytes data) {
  (void)token;
  (void)value;
  (void)data;
  fango_panic("Async is unsupported by LLVM");
  return (fango_native_error){0};
}
