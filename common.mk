# Monolithic Lab shared Makefile machinery — stack-agnostic.
# Canonical copy: ~/.claude/skills/lab-repo-standards/common.mk. Never edit a repo's copy in place: change the
# canonical file, then re-copy it into every repo that includes it (they must stay byte-identical).

SHELL := /bin/bash
.DEFAULT_GOAL := help
BUILD_LOG ?= .build/last-build.log

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z0-9_./-]+:[^=]*## ' $(MAKEFILE_LIST) | grep -v '## (no-help)' | sort -u \
		| awk 'BEGIN {FS = ":[^#]*## "}; {printf "  \033[36m%-24s\033[0m %s\n", $$1, $$2}'

# $(call no_compiler_warnings,<command>) runs a compiler invocation, tees it to $(BUILD_LOG) and fails on any
# `file:line:col: warning:` line — warnings-as-errors flags do not promote every diagnostic class.
define no_compiler_warnings
	mkdir -p $(dir $(BUILD_LOG)); \
	$(1) 2>&1 | tee $(BUILD_LOG); \
	status=$${PIPESTATUS[0]}; \
	if [ $$status -ne 0 ]; then exit $$status; fi; \
	if grep -qE '^[^[:space:]]+:[0-9]+:[0-9]+: warning:' $(BUILD_LOG); then \
		echo ""; \
		echo "error: the build above produced compiler warning(s) - fix them, don't suppress this check" >&2; \
		exit 1; \
	fi
endef
