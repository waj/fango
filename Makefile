# Convenience wrappers only — every target is a one-liner delegating to go.
# CI remains one command: `go test ./...` (DESIGN.md §11).

.PHONY: build test test-short update-goldens update-baselines fmt vet ci clean

build:
	go build -o fango ./cmd/fango

# The benchmark gates run AFTER the other packages: `go test ./...` runs
# packages in parallel, and the build-heavy differential suite skews the
# latency/ratio measurements (DESIGN.md §11 sanctions "go test ./... plus
# the bench gate" as separate steps).
test:
	go test $$(go list ./... | grep -v benchmarks)
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
