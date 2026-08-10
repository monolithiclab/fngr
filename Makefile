include common-go.mk

ROOT_DIR := $(dir $(realpath $(firstword $(MAKEFILE_LIST))))

VERSION ?= $(shell git describe --tags --dirty 2>/dev/null || echo "dev-$$(git rev-parse --short HEAD 2>/dev/null || echo unknown)")
LDFLAGS := -ldflags "-X main.version=$(VERSION)"


build:  ## Build binary
	go build $(LDFLAGS) -o build/fngr ./cmd/fngr


install:  ## Install fngr to $GOBIN (or $GOPATH/bin)
	go install $(LDFLAGS) ./cmd/fngr


bench:  ## Run benchmarks with memory stats
	go test -bench=. -benchmem -run=^$$ -count=1 ./...


bench-save:  ## Save benchmark baseline to bench.txt
	go test -bench=. -benchmem -run=^$$ -count=6 ./... | tee bench.txt


bench-compare:  ## Compare benchmarks against saved baseline (bench.txt)
	((test -z "$$FORCE_UPDATE" && which benchstat) || go install golang.org/x/perf/cmd/benchstat@latest) > /dev/null
	go test -bench=. -benchmem -run=^$$ -count=6 ./... > bench-new.txt
	$$(go env GOPATH)/bin/benchstat bench.txt bench-new.txt
	rm -f bench-new.txt


ci:  codefix format lint test  ## Run codefix, format, lint and tests


# Linter versions. `common-go.mk` installs each of these at @latest when the
# binary is not already on PATH, and that file is shared across repos, so
# `lint-tools` puts the version *this* repo chose in GOPATH/bin first and its
# `which` check finds it there. Bump deliberately; CI runs the same target, so
# a local `make lint` and a CI one see the same linters.
STATICCHECK_VERSION   := v0.7.0
GOLANGCI_LINT_VERSION := v1.64.8
GOSEC_VERSION         := v2.28.0
GOCRITIC_VERSION      := v0.14.4
GOVULNCHECK_VERSION   := v1.6.0

lint-tools:  ## Install the pinned linter versions into GOPATH/bin
	go install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	go install github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION)
	go install github.com/go-critic/go-critic/cmd/gocritic@$(GOCRITIC_VERSION)


vuln:  ## Report known vulnerabilities reachable from this module's code
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	$$(go env GOPATH)/bin/govulncheck ./...


# Pins go stale invisibly: nothing breaks when one regresses to a floating tag,
# it just quietly re-opens the hole. docs/PUBLISHING.md, "Refreshing the pins",
# is the canonical rationale and the refresh procedure. Hung off `lint` so CI
# and `make ci` both get it without either restating the list.
lint: lint-pins  ## (no-help)

lint-pins:  ## Check GitHub Actions are SHA-pinned and the Docker base is digest-pinned
	files=$$(ls .github/workflows/*.y*ml 2>/dev/null); \
	[ -n "$$files" ] || { echo 'lint-pins: no workflows found under .github/workflows/'; exit 1; }; \
	loose=$$( { grep -HnE '^[[:space:]]*-?[[:space:]]*uses:' $$files | grep -vE '@[0-9a-f]{40} # v'; \
	            grep -Hn '^FROM ' Dockerfile | grep -v '@sha256:'; } ); \
	split=$$(grep -hoE 'uses:[[:space:]]*[^[:space:]]+@[0-9a-f]{40}' $$files | sort -u | sed -E 's/@.*//;s/.*[[:space:]]//' | uniq -d); \
	if [ -n "$$loose" ] || [ -n "$$split" ]; then \
		[ -z "$$loose" ] || { echo 'Unpinned dependency — actions need @<40-hex-sha> with a "# vX.Y.Z" comment, FROM needs @sha256: (see docs/PUBLISHING.md):'; echo "$$loose"; }; \
		[ -z "$$split" ] || { echo 'Same action pinned to two different SHAs — refresh them together (see docs/PUBLISHING.md):'; echo "$$split"; }; \
		exit 1; \
	fi; \
	echo "Pins OK"


deploy:  ## Deploy the project
	echo "not implemented" && false


run:  ## Run the application locally
	go run ./cmd/fngr list


test:  ## Run unit tests with coverage and race detection
	go test -race -cover -coverprofile cover.out ./...
	test ! -s .covignore || { grep -v -E -f .covignore cover.out > cover.out.tmp && mv cover.out.tmp cover.out; }
	go tool cover -func cover.out


.SILENT: bench bench-compare bench-save build deploy install lint-pins lint-tools run test vuln
.PHONY: bench bench-compare bench-save build deploy install lint-pins lint-tools run test vuln
