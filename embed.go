// Package fango holds the embedded runtime sources. It lives at the module
// root because go:embed cannot reference paths outside the embedding
// package's directory; internal packages materialize these sources into
// generated Go modules.
package fango

import "embed"

//go:embed runtime/fangort/*.go runtime/nativewire/*.go runtime/nativeworker/*.go
var RuntimeFS embed.FS

//go:embed stdlib/*.fango stdlib/*.native.go stdlib/native_support.go
var StdlibFS embed.FS
