// Package fango holds the embedded runtime sources. It lives at the module
// root because go:embed cannot reference paths outside the embedding
// package's directory; internal/build imports it to materialize fangort
// into the persistent build directory.
package fango

import "embed"

//go:embed runtime/fangort/*.go
var FangortFS embed.FS

//go:embed stdlib/*.fango stdlib/*.native.go
var StdlibFS embed.FS
