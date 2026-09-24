package fangort

// NativeValue is a representation-blind token. Sidecars may retain it and
// return it at the declared index, but cannot inspect or invoke its payload.
type NativeValue struct{ value any }

func PackNativeValue(value any) any { return NativeValue{value: value} }

func UnpackNativeValue[T any](value any) T {
	payload := value.(NativeValue).value
	if payload == nil {
		var zero T
		return zero
	}
	return payload.(T)
}
