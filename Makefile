# Convenience wrappers for the repository verification gates; see
# doc/design.md, "Testing and performance".

.PHONY: build install test test-short test-perf update-goldens update-baselines fmt fmt-fango vet ci clean

build:
	go build -o fango ./cmd/fango

# The compiler reads its standard library and Go runtime support from a root
# holding stdlib/ and runtime/, found beside the executable when FANGO_ROOT is
# unset. A checkout is itself a valid root, so a locally built ./fango needs no
# install; this is the layout a packaged one expects. See
# doc/reference/commands.md, "The library root".
PREFIX ?= /usr/local
LIBDIR = $(PREFIX)/lib/fango

install: build
	install -d $(PREFIX)/bin $(LIBDIR)/stdlib
	install -m 755 fango $(PREFIX)/bin/fango
	install -m 644 stdlib/*.fango stdlib/*.native.go stdlib/native_support.go $(LIBDIR)/stdlib
	for pkg in fangort nativewire nativeworker; do \
		install -d $(LIBDIR)/runtime/$$pkg; \
		for f in runtime/$$pkg/*.go; do \
			case "$$f" in *_test.go) continue;; esac; \
			install -m 644 "$$f" $(LIBDIR)/runtime/$$pkg; \
		done; \
	done

# The correctness suite: deterministic, asserting nothing about elapsed time.
# This includes the full compiler/interpreter differential suite.
test:
	go test -parallel 16 $$(go list ./... | grep -v benchmarks)

# The compile-latency and runtime-ratio gates measure elapsed time, so they
# answer to host load and, for latency, to the machine that recorded the
# baselines. Run them deliberately on an otherwise idle machine; they are
# never part of test or ci.
test-perf:
	go test ./benchmarks

test-short:
	go test ./... -short

# Regenerate golden files after an intentional output change. Review the
# diff before committing.
update-goldens:
	go test ./internal/lexer ./internal/parser ./internal/infer ./internal/elaborate ./internal/repl ./internal/format -update

# Re-record compile-latency baselines (machine-specific).
update-baselines:
	go test ./benchmarks -update-baselines

fmt:
	gofmt -w .

# The Fango formatter over the sources the project owns, mirroring what `fmt`
# does for the Go sources. testdata is excluded: it deliberately holds malformed
# and oddly laid out inputs. The `ci` gate checks the same set without writing.
fmt-fango:
	go run ./cmd/fango fmt -w stdlib/*.fango examples/*.fango

vet:
	go vet ./...

ci:
	test -z "$$(gofmt -l .)"
	go run ./cmd/fango fmt -l stdlib/*.fango examples/*.fango
	go vet ./...
	go test -parallel 16 $$(go list ./... | grep -v benchmarks)

clean:
	rm -f fango
	find . -path ./.git -prune -o -type d -name .fango -prune -exec rm -rf -- {} +
	go clean -testcache
