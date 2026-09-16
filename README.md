<p align="center">
  <img src="fango.jpg" alt="Fango logo" width="260">
</p>

# Fango

Fango is an experimental, strict, statically typed, purely functional
programming language inspired by Elm and Haskell. It combines whole-program
type inference, algebraic data types and pattern matching, type classes, and
direct-style algebraic effects, then compiles programs to Go.

The implementation is deliberately small: a Go toolchain, local source
modules, a bundled experimental standard library, and generated programs that
stay close to ordinary Go.

## Try it

Fango requires Go 1.26. Build the compiler, then run a program:

```sh
make
./fango run examples/guess.fango
```

Find more runnable programs and their expected outputs in [examples](examples/).

## Documentation

- [Language reference](doc/reference.md): syntax, commands, and library contracts.
- [Compiler design](doc/design.md): architecture, invariants, and source/test links.
- [Roadmap](doc/roadmap.md): priorities and unresolved work.
