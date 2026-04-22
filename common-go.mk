# Common Makefile targets for Go projects
# 
# This file should be imported from each Makefile.

default: help

.SILENT: help
.PHONY: help
help:  ## Show this help.
	@grep --no-filename -E "^[^._][a-zA-Z_-]*:" $(MAKEFILE_LIST) | awk -F '[:#]' '{print $$1, ":", $$NF}' | grep -v "(no-help)"| sort | column -t -s:


.SILENT: lint-format
.PHONY: lint-format
lint-format:  ## test if source code is properly formated. (no-help)
	test -z "$$(gofmt -d .)" || (echo 'Formatting error, run `make format` to fix issues'; exit 1)


.SILENT: lint-vet
.PHONY: lint-vet
lint-vet:  ## (no-help)
	go vet ./...


.SILENT: lint-staticcheck
.PHONY: lint-staticcheck
lint-staticcheck:  ## (no-help)
	((test -z "$$FORCE_UPDATE" && which staticcheck) || go install -v honnef.co/go/tools/cmd/staticcheck@latest) > /dev/null
	$$(go env GOPATH)/bin/staticcheck ./...


.SILENT: lint-golangci-lint
.PHONY: lint-golangci-lint
lint-golangci-lint:  ## (no-help)
	((test -z "$$FORCE_UPDATE" && which golangci-lint) || go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest) > /dev/null
	$$(go env GOPATH)/bin/golangci-lint run ./...


.SILENT: lint-gosec
.PHONY: lint-gosec
lint-gosec:  ## (no-help)
	((test -z "$$FORCE_UPDATE" && which gosec) || go install github.com/securego/gosec/v2/cmd/gosec@latest) > /dev/null
	$$(go env GOPATH)/bin/gosec -quiet ./...


.SILENT: lint-gocritic
.PHONY: lint-gocritic
lint-gocritic:  # (no-help)
	((test -z "$$FORCE_UPDATE" && which gocritic) || go install -v github.com/go-critic/go-critic/cmd/gocritic@latest) > /dev/null
	$$(go env GOPATH)/bin/gocritic check ./...


.SILENT: lint
.PHONY: lint
lint: lint-format lint-vet lint-staticcheck lint-golangci-lint lint-gosec lint-gocritic  ## Lint source code (use -j to parallelize, use FORCE_UPDATE=1 to reinstall linters)


.SILENT: codefix
.PHONY: codefix
codefix:  ## Update to latest Golang best practices and patterns
	go fix ./...


.SILENT: format
.PHONY: format
format:  ## Format source code
	gofmt -s -w .


.SILENT: update-deps
.PHONY: update-deps
update-deps:  ## Update dependencies
	go get -u ./...
	go get -t -u ./...
	go mod tidy


.SILENT: clean
.PHONY: clean
clean:  ## Purge temporary files
	rm -rf build bench.txt cover.out coverage.html
