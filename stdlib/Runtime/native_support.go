package native

import "github.com/waj/fango/runtime/fangort"

// FangoHost is supplied beside every materialized native sidecar. This copy
// makes the bundled sidecars build as part of the compiler as well.
type FangoNativeHost = fangort.NativeHost

var FangoHost FangoNativeHost = fangort.SystemNativeHost

// These scoped tokens carry no authority to invoke Fango or access FangoHost.
type FangoRequest = fangort.NativeRequest
type FangoRequestHost = fangort.NativeRequestHost

var FangoNewRequestHost = fangort.NewNativeRequestHost
