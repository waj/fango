package native

import (
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestTextBuilderPersistenceAndGrowth(t *testing.T) {
	base := BufferAppend(nil, 0, "base")
	left := BufferAppend(base, 4, "-left")
	right := BufferAppendChar(base, 4, '二')
	if BufferText(base, 4) != "base" || BufferText(left, 9) != "base-left" || BufferText(right, 7) != "base二" {
		t.Fatal("branch rewrote a prefix")
	}
	var built any
	var length int64
	for range 1000 {
		built = BufferAppend(built, length, "abc")
		length += 3
	}
	if got := BufferText(built, length); got != strings.Repeat("abc", 1000) {
		t.Fatal("growth lost text")
	}
	if BufferAppend(base, 4, "") != base {
		t.Fatal("empty append changed storage")
	}
	if BufferText(nil, 0) != "" {
		t.Fatal("empty text")
	}
}

func TestTextBuilderConcurrentVersions(t *testing.T) {
	base := BufferAppend(nil, 0, "base")
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
				built = BufferAppend(built, length, text)
				length = BufferLength(built)
				if length != oldLength+int64(len(text)) {
					t.Error("wrong new version length")
					return
				}
				if got := BufferText(old, oldLength); got != want {
					t.Errorf("old version: %q != %q", got, want)
					return
				}
				want += text
				branch := BufferAppendChar(old, oldLength, 'λ')
				if BufferText(branch, oldLength+2) != want[:oldLength]+"λ" {
					t.Error("branch changed")
				}
				if BufferText(base, 4) != "base" {
					t.Error("base changed")
				}
			}
			if BufferText(built, length) != want {
				t.Error("latest version changed")
			}
		})
	}
	close(start)
	wg.Wait()
}

func TestTextBuilderUnpublishedTail(t *testing.T) {
	base := BufferAppend(nil, 0, "base")
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
			if BufferText(base, 4) != "base" {
				t.Error("unfinished tail changed published text")
				return
			}
			branch := BufferAppend(base, 4, "-branch")
			if BufferLength(branch) != 11 || BufferText(branch, 11) != "base-branch" {
				t.Error("branch read unfinished tail")
				return
			}
		}
	}()
	copy(tail, "-unpublished")
	<-done
	if BufferText(reserved, BufferLength(reserved)) != "base-unpublished" {
		t.Fatal("reserved tail lost")
	}
}

func TestTextBuilderScalarRendering(t *testing.T) {
	for _, n := range []int64{math.MinInt64, math.MaxInt64, -1, 0, 1} {
		b := BufferAppendInt(nil, 0, n)
		if got := BufferText(b, BufferLength(b)); got != IntToString(n) {
			t.Fatalf("%d: %q", n, got)
		}
	}
	values := []float64{0, math.Copysign(0, -1), 1, -1.25, 1e-6, 1e-7, 1e20, 1e21, math.SmallestNonzeroFloat64, math.MaxFloat64, math.Inf(1), math.Inf(-1), math.NaN()}
	rng := rand.New(rand.NewPCG(1, 2))
	for range 1000 {
		values = append(values, math.Float64frombits(rng.Uint64()))
	}
	for _, n := range values {
		b := BufferAppendFloat(nil, 0, n)
		text := BufferText(b, BufferLength(b))
		if text != FloatToString(n) {
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
	// Once capacity exists, scalar and literal appends must not allocate scratch
	// strings, version objects, or replacement backing storage.
	for name, appendValue := range map[string]func(any, int64) any{
		"stringLiteral": func(b any, n int64) any { return BufferAppendStringLiteral(b, n, "a\n\"\\#{二}") },
		"charLiteral":   func(b any, n int64) any { return BufferAppendCharLiteral(b, n, '\'') },
		"char":          func(b any, n int64) any { return BufferAppendChar(b, n, '二') },
		"int":           func(b any, n int64) any { return BufferAppendInt(b, n, math.MinInt64) },
		"float":         func(b any, n int64) any { return BufferAppendFloat(b, n, math.MaxFloat64) },
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
