# Monolithic Lab shared Makefile machinery — Go layer, included after common.mk.
# Canonical copy: ~/.claude/skills/go-cli-development/common.go.mk. Never edit a repo's copy in place: change the
# canonical file, then re-copy it into every repo that includes it (they must stay byte-identical).
# Repo-specific values go in the repo Makefile, as plain `VAR = value` lines after the includes.

BINARY ?= $(notdir $(CURDIR))
MAIN ?= ./cmd/$(BINARY)
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS ?= -s -w -X main.version=$(VERSION)
VULNCHECK_PACKAGES ?= ./...
COVERAGE_MIN ?=
BENCH_COUNT ?= 6
TOOLS ?= go tool -modfile=tools/go.mod

# go.mod's toolchain line (else a full x.y.z go directive) is only a minimum: a newer local Go would still win, so
# pin it exactly. `go mod tidy` drops a toolchain line equal to the go directive, hence the fallback.
GOTOOLCHAIN ?= $(shell awk '/^toolchain / {t = $$2} /^go [0-9]+\.[0-9]+\.[0-9]+/ {g = "go" $$2} END {print (t != "" ? t : g)}' go.mod 2>/dev/null)
ifneq ($(GOTOOLCHAIN),)
export GOTOOLCHAIN
endif

# Tracked and untracked-but-not-ignored Go files: gofmt walks every directory, including ignored worktrees.
GO_FILES = git ls-files -z --cached --others --exclude-standard -- '*.go'

.PHONY: build
build: ## Build the binary into build/
	@go build -ldflags '$(LDFLAGS)' -o build/$(BINARY) $(MAIN)

.PHONY: install
install: ## Install the binary into $GOBIN
	@go install -ldflags '$(LDFLAGS)' $(MAIN)

.PHONY: test
test: ## Run the tests with -race and coverage (.covignore filters the profile, COVERAGE_MIN=<pct> sets a floor)
	@go test -race -coverprofile=cover.out ./...
	@if [ -f .covignore ]; then \
		patterns=$$(grep -vE '^[[:space:]]*(#|$$)' .covignore || true); \
		if [ -n "$$patterns" ]; then \
			grep -vE -f <(printf '%s\n' "$$patterns") cover.out > cover.out.tmp && mv cover.out.tmp cover.out; \
		fi; \
	fi
	@total=$$(go tool cover -func=cover.out | awk '/^total:/ {sub(/%/, "", $$NF); print $$NF}'); \
	echo "Total coverage: $$total%"; \
	if [ -n "$(COVERAGE_MIN)" ] && ! awk -v t="$$total" -v m="$(COVERAGE_MIN)" 'BEGIN {exit !(t + 0 >= m + 0)}'; then \
		echo "error: coverage $$total% is below COVERAGE_MIN=$(COVERAGE_MIN)%" >&2; \
		exit 1; \
	fi

.PHONY: bench
bench: ## Run the benchmarks once, with memory stats
	@go test -bench=. -benchmem -run='^$$' -count=1 ./...

.PHONY: bench-save
bench-save: ## Save a benchmark baseline to bench-base.txt
	@go test -bench=. -benchmem -run='^$$' -count=$(BENCH_COUNT) ./... | tee bench-base.txt

.PHONY: bench-compare
bench-compare: ## Compare the benchmarks against bench-base.txt with benchstat
	@test -f bench-base.txt || { echo "error: no bench-base.txt, run make bench-save first" >&2; exit 1; }
	@go test -bench=. -benchmem -run='^$$' -count=$(BENCH_COUNT) ./... > bench-new.txt
	@$(TOOLS) benchstat bench-base.txt bench-new.txt

.PHONY: lint
lint: lint-golangci lint-vulncheck lint-mod lint-pins ## Run every non-mutating check (needs network for govulncheck)

# One .golangci.yml runs gofmt -s, govet, staticcheck (ST1000 on), gosec and gocritic's shadow checks.
.PHONY: lint-golangci
lint-golangci:
	@$(TOOLS) golangci-lint run --allow-serial-runners ./...

.PHONY: lint-vulncheck
lint-vulncheck:
	@$(TOOLS) govulncheck $(VULNCHECK_PACKAGES)

.PHONY: lint-mod
lint-mod:
	@go mod tidy -diff

# Every action pinned to a 40-hex SHA with a "# vX" comment, no action at two SHAs, every Dockerfile FROM by digest.
# A FROM naming an earlier build stage or scratch needs no digest.
.PHONY: lint-pins
lint-pins:
	@[ -d .github/workflows ] || exit 0; \
	files=$$(find .github/workflows -name '*.y*ml'); \
	[ -n "$$files" ] || exit 0; \
	loose=$$(grep -HnE '^[[:space:]]*-?[[:space:]]*uses:' $$files | grep -vE 'uses:[[:space:]]*\./' | grep -vE '@[0-9a-f]{40} # v'); \
	split=$$(grep -hoE 'uses:[[:space:]]*[^[:space:]]+@[0-9a-f]{40}' $$files | sort -u \
		| sed -E 's/@.*//; s/.*[[:space:]]//' | uniq -d); \
	docker=$$(find . -name 'Dockerfile*' -not -path './.git/*' -not -path './.worktrees/*' | while IFS= read -r f; do \
		stages=$$(grep -ioE '^FROM .+ AS [^[:space:]]+' "$$f" | awk '{print tolower($$NF)}'); \
		grep -inE '^FROM[[:space:]]' "$$f" | grep -v '@sha256:' | while IFS= read -r line; do \
			image=$$(echo "$$line" | sed -E 's/^[0-9]+:[Ff][Rr][Oo][Mm][[:space:]]+(--[^[:space:]]+[[:space:]]+)*//; s/[[:space:]].*//' | tr 'A-Z' 'a-z'); \
			[ "$$image" = scratch ] || echo "$$stages" | grep -qx "$$image" || echo "$$f:$$line"; \
		done; \
	done); \
	[ -z "$$loose" ] || { echo "Actions must be pinned to @<40-hex-sha> # vX.Y.Z:"; echo "$$loose"; }; \
	[ -z "$$split" ] || { echo "Same action pinned to two SHAs, refresh them together:"; echo "$$split"; }; \
	[ -z "$$docker" ] || { echo "Docker base images must be pinned by @sha256: digest:"; echo "$$docker"; }; \
	[ -z "$$loose$$split$$docker" ]

.PHONY: lint-fix
lint-fix: ## Apply every auto-fix: go fix, then gofmt -s
	@go fix ./...
	@$(GO_FILES) | xargs -0 gofmt -s -w

.PHONY: ci
ci: lint test ## The gate: lint and test, never mutating (what CI runs)

.PHONY: update-deps
update-deps: ## Update every dependency, tidy, then show the go.mod diff (go get rewrites the go directive)
	@go get -u -t ./...
	@go mod tidy
	@git --no-pager diff go.mod

.PHONY: clean
clean: ## Remove build/, dist/, cover.out and the bench files
	@rm -rf build dist cover.out cover.out.tmp bench-*.txt
