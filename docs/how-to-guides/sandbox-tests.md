# Sandbox tests

`just dev` builds or updates the sandbox and runs the specs the branch's changes can affect. The cluster tests are
Ginkgo specs in `test/e2e` (one top-level `Describe` each, named after the test it was); each carries labels, on its
top-level container only, from the fixed list in `test/scopes.yaml` (`labels:`): where it may run (`sandbox`, `testbed`,
`production`), what it proves (`logs`, `sso`, `kiali`, …) and `alone` for those that run by themselves.

| Command | Runs |
|---|---|
| `just dev` | The specs `test/scopes.yaml` maps the branch's changes to (`git diff origin/dev...HEAD`), plus `smoke`; nothing when every change is skipped (docs and the like); the whole suite when a file is in no entry |
| `just dev --full` | The whole suite on the existing sandbox: after merging into `dev` |
| `just dev-fresh` | `just dev-clean`, then the whole suite on a new sandbox: before a `dev` → `master` PR or a release |
| `just dev --tests '<label filter>'` | Exactly those specs (a Ginkgo label filter, examples below); a filter that does not parse, names a label outside `labels:` or selects no sandbox spec is refused |
| `just dev --revision <sha>` | The same selection, syncing that commit without pushing branch `sandbox` (what GitHub Actions runs, [CI](ci.md)); it must be the commit checked out (HEAD), because the specs and their scope come from the checkout |
| `just test e2e --env <sandbox\|testbed\|production> [--labels '<filter>'] [--focus <regex>] [--verbose]` | The suite against an environment's cluster as it is, no build; `--env` is required, and `&& <env>` is added to the filter |

## Optional apps

Paperless and OpenCloud take about 900 MB, so the local sandbox runs them only when asked (the justfile's
`optional_apps`):

- **automatically**, when the branch changes their app file (`apps/paperless.yaml`, `apps/opencloud.yaml`): an upgrade
  is tested before production;
- **on request**: `just dev --apps paperless`, `--apps paperless,opencloud`, or `--apps all`, for example with
  `just dev-fresh --apps all`.

`--apps none` (alone, never with names) leaves both out even when the branch changes their files: a branch that touches every app can then be
proved in two runs that each fit in memory, `just dev --full --apps none` and then
`just dev --apps all --tests 'paperless || opencloud || smoke'`.

`just dev` prints the list (`optional apps '...'`) and refuses a name that is not an optional app. An app left out is
removed: Argo CD deletes its Application, and `just dev` deletes its namespace (Argo CD keeps namespaces it created),
volumes included. Its specs skip (`SSOPaperless`, `SSOOpenCloud`, its smoke spec), and in the sandbox
`lab secrets rotate --all` skips its secrets ("not deployed here"). An app the list names but that is absent fails
its specs instead. GitHub Actions' release, retarget and on-demand sandboxes run with `--apps all`, so paperless and
OpenCloud are proved before every release; a pull request labelled `sandbox` deploys them only when it changes their
files, as `just dev` does.

For example, the Loki and OpenBao specs only, then Kiali's (`sso && kiali`: a family and an extra label):

```sh
just dev --tests 'logs || secrets'
just dev --tests 'sso && kiali'
```

Specs labelled `alone` (NodeRestart restarts the node) never run with the others: `just dev` runs the main run with
`&& !alone`, then each alone spec the filter reaches in its own Ginkgo run, in `alone:`'s order, so a failure or a
timeout names its own spec (`--tests 'logs || node-restart'` runs `logs`, then `node-restart` alone).

Each spec has its own timeout (`SpecTimeout`, sized from the measured durations), and Ginkgo prints a progress report
for a spec still running after a minute, every 30 seconds; a filter that selects nothing fails (`--fail-on-empty`).
JUnit and JSON reports land in `test/reports/e2e/` (git-ignored; GitHub Actions uploads them as `e2e-reports`).

To see what a branch would run without running it:

```sh
cd cli && go run ./cmd/lab test scope                  # the main run's filter
cd cli && go run ./cmd/lab test scope --alone --each   # the alone runs afterwards, one per line
cd test && go tool ginkgo run --dry-run -v --label-filter '(logs) && sandbox' ./e2e   # the specs a filter selects
```

## Adding a component or a spec

Add a component's paths to a group in `test/scopes.yaml` (a label filter of the specs that cover it), or to `skip`.
A new spec is a top-level `var _ = Describe("Name", Label(...), func() { ... })` in `test/e2e` with its environment
labels and its family, each `It` with `SpecTimeout`, every wait on the spec's context (`kube.*`, `Eventually(ctx, …)`,
`exec.CommandContext`). `just cli check` (a basic check) fails while a component is in no entry, a label is outside
`labels:` or labels no spec, or a filter selects no spec.
