# Sandbox tests

`make dev` builds or updates the sandbox and runs the tests the branch's changes can affect.

| Command | Runs |
|---|---|
| `make dev` | The tests `test/scopes.yaml` maps the branch's changes to (`git diff origin/dev...HEAD`), plus `Smoke`; nothing when every change is skipped (docs and the like); the whole suite when a file is in no entry |
| `make dev scope=full` | The whole suite on the existing sandbox: after merging into `dev` |
| `make dev-fresh` | `make dev-clean`, then the whole suite on a new sandbox: before a `dev` → `master` PR or a release |
| `make dev tests='Logs\|Secrets'` | Exactly those tests (a `go test -run` regex) |

`TestNodeRestart` restarts the node, so it always runs alone, after the others.

To see what a branch would run without running it:

```sh
cd cli && go run ./cmd/lab test scope            # the main run
cd cli && go run ./cmd/lab test scope --alone    # what runs alone afterwards
```

## Adding a component

Add its paths to a group in `test/scopes.yaml` (the tests that cover it), or to `skip`. `make -C cli check` (a basic
check) fails while a component is in no entry, and while a group names a test that does not exist.
