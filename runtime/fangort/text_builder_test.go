package fangort

import (
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestTextBuilderPersistenceAndGrowth(t *testing.T) {
	base := TextBufferAppend(nil, 0, "base")
	left := TextBufferAppend(base, 4, "-left")
	right := TextBufferAppendChar(base, 4, '二')
	if TextBufferText(base, 4) != "base" || TextBufferText(left, 9) != "base-left" || TextBufferText(right, 7) != "base二" {
		t.Fatal("branch rewrote a prefix")
	}
	var built any
	var length int64
	for range 1000 {
		built = TextBufferAppend(built, length, "abc")
		length += 3
	}
	if got := TextBufferText(built, length); got != strings.Repeat("abc", 1000) {
		t.Fatal("growth lost text")
	}
	if TextBufferAppend(base, 4, "") != base {
		t.Fatal("empty append changed storage")
	}
	if TextBufferText(nil, 0) != "" {
		t.Fatal("empty text")
	}
}

func TestTextBuilderConcurrentVersions(t *testing.T) {
	base := TextBufferAppend(nil, 0, "base")
	var wg sync.WaitGroup
	start := make(chan struct{})
	for worker := range 16 {
		wg.Go(func() {
			<-start
			built := base
			length := int64(4)
			want := "base"
			for step := range 100 {
				old, oldLength := built, length
				text := strconv.Itoa(worker) + "二" + strconv.Itoa(step)
				built = TextBufferAppend(built, length, text)
				length = TextBufferLength(built)
				if length != oldLength+int64(len(text)) {
					t.Error("wrong new version length")
					return
				}
				if got := TextBufferText(old, oldLength); got != want {
					t.Errorf("old version: %q != %q", got, want)
					return
				}
				want += text
				branch := TextBufferAppendChar(old, oldLength, 'λ')
				if TextBufferText(branch, oldLength+2) != want[:oldLength]+"λ" {
					t.Error("branch changed")
				}
				if TextBufferText(base, 4) != "base" {
					t.Error("base changed")
				}
			}
			if TextBufferText(built, length) != want {
				t.Error("latest version changed")
			}
		})
	}
	close(start)
	wg.Wait()
}

func TestTextBuilderUnpublishedTail(t *testing.T) {
	base := TextBufferAppend(nil, 0, "base")
	// Reserve in the existing storage but deliberately leave the tail unwritten.
	// Readers and competing appenders hold only the published four-byte prefix.
	reserved, tail := reserveText(base, 4, len("-unpublished"))
	if reserved != base {
		t.Fatal("expected to reserve existing capacity")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			if TextBufferText(base, 4) != "base" {
				t.Error("unfinished tail changed published text")
				return
			}
			branch := TextBufferAppend(base, 4, "-branch")
			if TextBufferLength(branch) != 11 || TextBufferText(branch, 11) != "base-branch" {
				t.Error("branch read unfinished tail")
				return
			}
		}
	}()
	copy(tail, "-unpublished")
	<-done
	if TextBufferText(reserved, TextBufferLength(reserved)) != "base-unpublished" {
		t.Fatal("reserved tail lost")
	}
}

func TestTextBuilderScalarRendering(t *testing.T) {
	for _, n := range []int64{math.MinInt64, math.MaxInt64, -1, 0, 1} {
		b := TextBufferAppendInt(nil, 0, n)
		if got := TextBufferText(b, TextBufferLength(b)); got != ShowInt(n) {
			t.Fatalf("%d: %q", n, got)
		}
	}
	values := []float64{0, math.Copysign(0, -1), 1, -1.25, 1e-6, 1e-7, 1e20, 1e21, math.SmallestNonzeroFloat64, math.MaxFloat64, math.Inf(1), math.Inf(-1), math.NaN()}
	rng := rand.New(rand.NewPCG(1, 2))
	for range 1000 {
		values = append(values, math.Float64frombits(rng.Uint64()))
	}
	for _, n := range values {
		b := TextBufferAppendFloat(nil, 0, n)
		text := TextBufferText(b, TextBufferLength(b))
		if text != ShowFloat(n) {
			t.Fatalf("%g: %q", n, text)
		}
		if !math.IsNaN(n) && !math.IsInf(n, 0) {
			parsed, err := strconv.ParseFloat(text, 64)
			if err != nil || parsed != n {
				t.Fatalf("round trip %g: %q -> %g (%v)", n, text, parsed, err)
			}
		}
	}
}

func TestTextBuilderSequentialAppendAllocations(t *testing.T) {
	// Once capacity exists, char/int/float appends must not allocate scratch
	// strings, version objects, or replacement backing storage.
	for name, appendValue := range map[string]func(any, int64) any{
		"char":  func(b any, n int64) any { return TextBufferAppendChar(b, n, '二') },
		"int":   func(b any, n int64) any { return TextBufferAppendInt(b, n, math.MinInt64) },
		"float": func(b any, n int64) any { return TextBufferAppendFloat(b, n, math.MaxFloat64) },
	} {
		t.Run(name, func(t *testing.T) {
			b := &textBuffer{data: make([]byte, 4096)}
			allocs := testing.AllocsPerRun(100, func() { b.frontier.Store(0); appendValue(b, 0) })
			if allocs != 0 {
				t.Fatalf("%g allocations per append", allocs)
			}
		})
	}
}
