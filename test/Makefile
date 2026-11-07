.POSIX:

filter=.
format=testname

default: test

test:
	gotestsum --format ${format} -- -timeout 60m -run "$(value filter)"
