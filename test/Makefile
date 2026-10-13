.POSIX:

filter=.

default: test

test:
	gotestsum --format testname -- -timeout 60m -run "${filter}"
