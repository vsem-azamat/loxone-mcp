# One entry point for the checks CI runs. golangci-lint, govulncheck and actionlint are pinned in
# tools/go.mod and tools/lint/go.mod and run through `go tool`, so nothing but Go needs a global
# install. `make check` runs every CI job but the release snapshot (`make release-check`).

SHELL := bash

TOOL := go tool -modfile=tools/go.mod
LINT := go tool -modfile=tools/lint/go.mod golangci-lint

# The GoReleaser of the workflows.
GORELEASER_VERSION := v2.19.0
GORELEASER := go run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)

MODULES := . tools tools/lint

.PHONY: check fmt lint test vuln tidy tidy-check repo release-check build help

## check: every CI job but the release snapshot
check: repo tidy-check lint test vuln

## build: build ./loxone-mcp
build:
	CGO_ENABLED=0 go build -trimpath -o loxone-mcp ./cmd/loxone-mcp

## fmt: apply the Go formatters (gofumpt, goimports)
fmt:
	$(LINT) fmt

## lint: Go linters and formatting (golangci-lint)
lint:
	$(LINT) config verify
	$(LINT) run

## test: unit tests with the race detector, in random order (no network but loopback)
test:
	go test -race -shuffle=on ./...

## vuln: known vulnerabilities in the code and its dependencies (govulncheck, needs network)
vuln:
	$(TOOL) govulncheck ./...

## tidy: run go mod tidy in every module
tidy:
	@for m in $(MODULES); do echo "go mod tidy ($$m)"; go -C $$m mod tidy || exit 1; done

## tidy-check: go.mod and go.sum are tidy in every module
tidy-check:
	@for m in $(MODULES); do \
		go -C $$m mod tidy -diff || { echo "$$m: go.mod or go.sum is not tidy: run make tidy" >&2; exit 1; }; \
	done

## repo: repository policy, editorconfig and workflow checks
repo:
	scripts/repo-check.sh
	$(TOOL) editorconfig-checker
	$(TOOL) actionlint

## release-check: the GoReleaser config is valid and a snapshot builds into dist/
release-check:
	$(GORELEASER) check
	$(GORELEASER) release --snapshot --clean

## help: list the targets
help:
	@awk '/^## /{l=substr($$0,4); i=index(l,": "); printf "  %-14s %s\n", substr(l,1,i-1), substr(l,i+2)}' $(MAKEFILE_LIST)
