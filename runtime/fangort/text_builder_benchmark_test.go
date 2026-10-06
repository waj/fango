package fangort

import (
	"strconv"
	"sync"
	"testing"
)

// Retain the former storage algorithm as a benchmark baseline, independent of
// Fango dictionary/callback costs. Run these deliberately on an idle host.
type mutexTextBuffer struct {
	mu   sync.Mutex
	data []byte
}

func mutexTextAppend(buffer any, length int64, text string) (any, int64) {
	if buffer == nil {
		return &mutexTextBuffer{data: append(make([]byte, 0, max(64, len(text))), text...)}, int64(len(text))
	}
	b := buffer.(*mutexTextBuffer)
	b.mu.Lock()
	if int64(len(b.data)) == length {
		b.data = append(b.data, text...)
		b.mu.Unlock()
		return b, length + int64(len(text))
	}
	data := make([]byte, length, max(64, int(length)+len(text)))
	copy(data, b.data[:length])
	b.mu.Unlock()
	return &mutexTextBuffer{data: append(data, text...)}, length + int64(len(text))
}

func mutexTextRead(buffer any, length int64) string {
	b := buffer.(*mutexTextBuffer)
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data[:length])
}

// A mutex baseline with the same geometric capacity growth as production
// isolates synchronization/header costs from the former slice growth policy.
func geometricMutexTextAppend(buffer any, length int64, text string) (any, int64) {
	end := int(length) + len(text)
	if buffer == nil {
		return &mutexTextBuffer{data: append(make([]byte, 0, max(64, end)), text...)}, int64(end)
	}
	b := buffer.(*mutexTextBuffer)
	b.mu.Lock()
	if int64(len(b.data)) != length {
		data := make([]byte, length, max(64, end))
		copy(data, b.data[:length])
		b.mu.Unlock()
		return &mutexTextBuffer{data: append(data, text...)}, int64(end)
	}
	if end > cap(b.data) {
		data := make([]byte, length, max(end, cap(b.data)*2))
		copy(data, b.data)
		b.data = data
	}
	b.data = b.data[:end]
	copy(b.data[length:end], text)
	b.mu.Unlock()
	return b, int64(end)
}

func atomicTextAppend(buffer any, length int64, text string) (any, int64) {
	next := TextBufferAppend(buffer, length, text)
	return next, TextBufferLength(next)
}

var textBuilderBenchmarkResult string

func BenchmarkTextBuilder(b *testing.B) {
	for _, impl := range []struct {
		name   string
		append func(any, int64, string) (any, int64)
		read   func(any, int64) string
	}{
		{"OriginalMutex", mutexTextAppend, mutexTextRead},
		{"GeometricMutex", geometricMutexTextAppend, mutexTextRead},
		{"Atomic", atomicTextAppend, TextBufferText},
	} {
		for _, chunks := range []int{16, 256, 4096} {
			b.Run(impl.name+"/Sequential/"+strconv.Itoa(chunks), func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					var buffer any
					var length int64
					for range chunks {
						buffer, length = impl.append(buffer, length, "abc")
					}
					textBuilderBenchmarkResult = impl.read(buffer, length)
				}
			})
		}
		b.Run(impl.name+"/Branch", func(b *testing.B) {
			base, length := impl.append(nil, 0, "base")
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				branch, end := impl.append(base, length, "suffix")
				textBuilderBenchmarkResult = impl.read(branch, end)
			}
		})
		b.Run(impl.name+"/Concurrent", func(b *testing.B) {
			base, length := impl.append(nil, 0, "base")
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					branch, end := impl.append(base, length, "suffix")
					if impl.read(branch, end) != "basesuffix" {
						b.Fatal("changed prefix")
					}
				}
			})
		})
	}
}
