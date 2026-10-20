.POSIX:
.PHONY: test check

# Part of the basic checks
test:
	go vet ./...
	# -count=1: the scope tests read test/scopes.yaml through git, which go test's cache does not see
	go test -race -count=1 ./...

# test/scopes.yaml covers every component and names existing tests (part of the basic checks)
check:
	go run ./cmd/lab test scope --check
