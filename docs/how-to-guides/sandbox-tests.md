# Sandbox tests

`make dev` builds or updates the sandbox and runs the tests the branch's changes can affect.

| Command | Runs |
|---|---|
| `make dev` | The tests `test/scopes.yaml` maps the branch's changes to (`git diff origin/dev...HEAD`), plus `Smoke`; nothing when every change is skipped (docs and the like); the whole suite when a file is in no entry |
| `make dev scope=full` | The whole suite on the existing sandbox: after merging into `dev` |
| `make dev-fresh` | `make dev-clean`, then the whole suite on a new sandbox: before a `dev` → `master` PR or a release |
| `make dev tests=<regex>` | Exactly those tests (a `go test -run` regex, example below); a term that matches no test is refused |
| `make dev revision=<sha>` | The same selection, syncing that commit without pushing branch `sandbox` (what GitHub Actions runs, [CI](ci.md)); it must be the commit checked out (HEAD), because the tests and their scope come from the checkout |

## Optional apps

Paperless and OpenCloud take about 900 MB, so the local sandbox runs them only when asked (the Makefile's
`optional_apps`):

- **automatically**, when the branch changes their app file (`apps/paperless.yaml`, `apps/opencloud.yaml`): an upgrade
  is tested before production;
- **on request**: `make dev apps=paperless`, `apps=paperless,opencloud`, or `apps=all`, for example with
  `make dev-fresh apps=all`.

`make dev` prints the list (`optional apps '...'`) and refuses a name that is not an optional app. An app left out is
removed: Argo CD deletes its Application, and `make dev` deletes its namespace (Argo CD keeps namespaces it created),
volumes included. Its tests skip (`TestSSOPaperless`, `TestSSOOpenCloud`, the smoke check), and in the sandbox
`lab secrets rotate --all` skips its secrets ("not deployed here"). An app the list names but that is absent fails
the tests instead. GitHub Actions' release, retarget and on-demand sandboxes run with `apps=all`, so paperless and
OpenCloud are proved before every release; a pull request labelled `sandbox` deploys them only when it changes their
files, as `make dev` does.

For example, the Loki and OpenBao tests only:

```sh
make dev tests='Logs|Secrets'
```

`TestNodeRestart` restarts the node, so it always runs alone, after the others; name it on its own
(`tests='Logs|NodeRestart'`), not through a broader term such as `.`.

To see what a branch would run without running it:

```sh
cd cli && go run ./cmd/lab test scope            # the main run
cd cli && go run ./cmd/lab test scope --alone    # what runs alone afterwards
```

## Adding a component

Add its paths to a group in `test/scopes.yaml` (the tests that cover it), or to `skip`. `make -C cli check` (a basic
check) fails while a component is in no entry, and while a group names a test that does not exist.
