# Convenience wrappers for the repository verification gates; see
# doc/design.md, "Testing and performance".

.PHONY: build test test-short test-perf update-goldens update-baselines fmt vet ci clean

build:
	go build -o fango ./cmd/fango

# Keep the default development loop deterministic and reasonably quick. This
# includes the full compiler/interpreter differential suite, but leaves noisy
# compile-latency and runtime-ratio gates to test-perf and ci.
test:
	go test $$(go list ./... | grep -v benchmarks)

test-perf:
	go test ./benchmarks

test-short:
	go test ./... -short

# Regenerate golden files after an intentional output change. Review the
# diff before committing.
update-goldens:
	go test ./internal/lexer ./internal/parser ./internal/elaborate ./internal/repl -update

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
	go test ./benchmarks

clean:
	rm -f fango
	go clean -testcache
