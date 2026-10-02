# Convenience wrappers for the repository verification gates; see
# doc/design.md, "Testing and performance".

.PHONY: build install test test-short test-grammar test-perf update-goldens update-baselines fmt fmt-fango vet check-files ci clean

build:
	go build -o fango ./cmd/fango

# The compiler reads its standard library and Go runtime support from a root
# holding stdlib/ and runtime/, found beside the executable when FANGO_ROOT is
# unset. A checkout is itself a valid root, so a locally built ./fango needs no
# install; this is the layout a packaged one expects. See
# doc/reference/commands.md, "The library root".
PREFIX ?= /usr/local
LIBDIR = $(PREFIX)/lib/fango
FANGO_SOURCES = $(shell find stdlib examples -name '*.fango' -type f -print)

install: build
	install -d $(PREFIX)/bin $(LIBDIR)/stdlib
	install -m 755 fango $(PREFIX)/bin/fango
	find stdlib -type f \( -name '*.fango' -o -name '*.native.go' -o -name 'native_support.go' \) -print | while IFS= read -r f; do \
		rel=$${f#stdlib/}; \
		case "$$rel" in */*) dest=$(LIBDIR)/stdlib/$${rel%/*};; *) dest=$(LIBDIR)/stdlib;; esac; \
		install -d "$$dest"; \
		install -m 644 "$$f" "$$dest"; \
	done
	for pkg in fangort nativewire nativeworker; do \
		install -d $(LIBDIR)/runtime/$$pkg; \
		for f in runtime/$$pkg/*.go; do \
			case "$$f" in *_test.go) continue;; esac; \
			install -m 644 "$$f" $(LIBDIR)/runtime/$$pkg; \
		done; \
	done
	for pkg in ast core eval execcodec meta natives objectcodec source types; do \
		install -d $(LIBDIR)/internal/$$pkg; \
		for f in internal/$$pkg/*.go; do \
			case "$$f" in *_test.go) continue;; esac; \
			install -m 644 "$$f" $(LIBDIR)/internal/$$pkg; \
		done; \
	done

# The correctness suite: deterministic, asserting nothing about elapsed time.
# This includes the full compiler/interpreter differential suite.
test:
	go test -parallel 16 $$(go list ./... | grep -v benchmarks)

# The TextMate grammar in editors/vscode/ encodes exact lexer rules, so a
# change to the surface syntax must be re-checked against it: this tokenizes
# every .fango file under stdlib, testdata, and examples. Node and the two
# grammar packages come from the Nix development shell.
test-grammar:
	node editors/vscode/tests/tokenize.cjs

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
	go run ./cmd/fango fmt -w $(FANGO_SOURCES)

vet:
	go vet ./...

# Rejects executable and oversized files in the index; the same script is the
# pre-commit hook the Nix development shell installs.
check-files:
	sh .githooks/pre-commit

ci: check-files
	test -z "$$(gofmt -l .)"
	go run ./cmd/fango fmt -l $(FANGO_SOURCES)
	go vet ./...
	go test -parallel 16 $$(go list ./... | grep -v benchmarks)

clean:
	rm -f fango
	find . -path ./.git -prune -o -type d -name .fango -prune -exec rm -rf -- {} +
	go clean -testcache
