.POSIX:
.PHONY: test check

# Part of the basic checks
test:
	go vet ./...
	go test -race ./...

# test/scopes.yaml covers every component and names existing tests (part of the basic checks)
check:
	go run ./cmd/lab test scope --check
