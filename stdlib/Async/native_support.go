package native

import "github.com/waj/fango/runtime/fangort"

type FangoNativeHost = fangort.NativeHost

var FangoHost FangoNativeHost = fangort.SystemNativeHost

type FangoRequest = fangort.NativeRequest
type FangoRequestHost = fangort.NativeRequestHost
type FangoEventBridge = fangort.NativeEventBridge

var FangoNewRequestHost = fangort.NewNativeRequestHost
var FangoNewEventBridge = fangort.NewNativeEventBridge
