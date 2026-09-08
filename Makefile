# Convenience wrappers for the repository verification gates; see
# doc/design.md, "Testing and performance".

.PHONY: build test test-short test-perf update-goldens update-baselines fmt vet ci clean

build:
	go build -o fango ./cmd/fango

# The correctness suite: deterministic, asserting nothing about elapsed time.
# This includes the full compiler/interpreter differential suite.
test:
	go test $$(go list ./... | grep -v benchmarks)

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
	go test ./internal/lexer ./internal/parser ./internal/infer ./internal/elaborate ./internal/repl -update

# Re-record compile-latency baselines (machine-specific).
update-baselines:
	go test ./benchmarks -update-baselines

fmt:
	gofmt -w .

vet:
	go vet ./...

ci:
	test -z "$$(gofmt -l .)"
	go vet ./...
	go test $$(go list ./... | grep -v benchmarks)

clean:
	rm -f fango
	go clean -testcache
