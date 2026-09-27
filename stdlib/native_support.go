package native

import "github.com/waj/fango/runtime/fangort"

// FangoHost is supplied beside every materialized native sidecar. This copy
// makes the bundled sidecars build as part of the compiler as well.
type FangoNativeHost = fangort.NativeHost

var FangoHost FangoNativeHost = fangort.SystemNativeHost

type FangoAsyncScope = fangort.AsyncScope
type FangoAsyncTask = fangort.AsyncTask
type FangoAsyncCompletion = fangort.AsyncCompletion
type FangoAsyncChannel = fangort.AsyncChannel[any]

var FangoNewAsyncRoot = fangort.NewAsyncRoot
var FangoNewAsyncScope = fangort.NewAsyncScope
var FangoNewAsyncValue = fangort.NewAsyncValue
var FangoPublishAsyncValue = fangort.PublishAsyncValue
var FangoAsyncSleep = fangort.AsyncSleep
var FangoNewAsyncChannel = fangort.NewAsyncChannel[any]
