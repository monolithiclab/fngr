include common.mk
include common.go.mk

# Set explicitly: common.go.mk derives it from the directory name, which is not fngr in a worktree.
BINARY = fngr

.PHONY: run
run: ## List the events in the local database (read-only)
	@go run $(MAIN) list
