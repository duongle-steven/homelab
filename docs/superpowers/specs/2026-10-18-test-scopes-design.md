# Test scopes: sandbox tests chosen from the change

Status: design approved in chat 2026-10-18, written spec awaiting review. Branch: `feat/test-scopes` (PR into `dev`).
Roadmap: done before steps 8d–14, because every later step pays for full sandbox runs.

## Goal

`make dev` runs the sandbox tests that the branch's changes can affect, instead of the whole suite every time
(about 13 minutes for the main set plus 6 to 9 for `TestNodeRestart`; step 8c ran it five times). The full suite still
runs where it catches what a scoped run misses: after a merge into `dev`, and from scratch before `master` and
releases. The selector is the first command of a Go CLI `lab`, laid out like upstream `nixos`'s `cli/`, so the later
CLI step adds commands to it instead of rewriting it, and Woodpecker (step 10) calls the same command.

## Context

- `make dev` (root `Makefile`): pushes HEAD to branch `sandbox`, applies `infra/sandbox`, runs
  `smoke_filter='Smoke|Identity|SSO|Gateway|Forgejo|Mesh|Monitoring|Egress|Secrets|Ntfy|Logs'`, then
  `smoke_filter='NodeRestart'` alone (it restarts the node). `test/Makefile` runs
  `gotestsum -- -timeout 60m -run "${filter}"`.
- Tests are named by area: `TestSmoke*`, `TestIdentity*`, `TestSSO<App>`, `TestGateway*`, `TestForgejo*`,
  `TestMesh*`, `TestMonitoring*`, `TestEgress*`, `TestSecrets*`, `TestNtfy*`, `TestLogs*`, `TestNodeRestart*`; offline
  ones (`TestState*`, `TestIstioCNIWatchdog`, `TestNodeRestart(Blockers|WatchdogJobs)`, …) run in the basic checks.
- A change often affects areas outside its own component (Istio touches every route, OpenBao every app with a secret),
  so groups are named by test, not by component.
- How others do it:
  - Prow (Kubernetes, Istio): presubmits with `run_if_changed`, postsubmits after merge, periodics without filters.
  - Cilium (`.github/ariane-config.yaml`): a smoke set on every PR, the full e2e set on `/test`, nightly runs; heavy
    workflows carry `paths-ignore-regex` (docs and the like), so they run unless the change is plainly harmless.
  - Upstream `nixos`: Woodpecker runs unit tests on `when.path`; e2e runs by hand with `make -C tests e2e filter=…`.
  - Build-graph tools (Bazel target-determinator, Nx `affected`) need a build graph this GitOps repository lacks.
- Upstream's CLI: Go module `github.com/khuedoan/homelab/cli`, `cli/cmd/lab/main.go`, `cli/internal/cli` (cobra root,
  `SilenceUsage`, `Execute`), packages under `cli/internal/<area>`, `cli/Makefile` (`go vet`, `go test -race`), packaged
  in its flake with `buildGoModule`.

## Decisions

| Topic | Decision |
|---|---|
| Selection | Automatic from `git diff --name-only <base>...HEAD`, through a map; plainly harmless changes run nothing; a file the map does not know runs everything |
| Map | `test/scopes.yaml`, next to the tests (format below) |
| Groups | Regexes of test names (`Identity\|SSO`), not components |
| Tool | `lab test scope` in Go, `cli/` module `github.com/duongle-steven/homelab/cli` (this fork's path; files ported from upstream get their imports rewritten), cobra, upstream's layout (`cmd/lab`, `internal/cli`, `internal/scope`) |
| Running `lab` | `go run ./cmd/lab` from `cli/` in the Makefile: no Nix packaging or vendor hash until the CLI step |
| New components | `lab test scope --check` (a basic check) fails when a component is in no group and not skipped, and when a group's regex matches no test |
| Tests that run alone | `alone: [NodeRestart]`: selected alone tests run after the others, as today |
| Base | `origin/dev` by default (`make dev` fetches it first); `base=<ref>` overrides |
| Overrides | `make dev scope=full`, `make dev tests='<regex>'` |
| From scratch | `make dev-fresh` = `make dev-clean` then `make dev scope=full` |

## `test/scopes.yaml`

```yaml
# Paths are git globs (** spans directories), matched against the files a branch changes
skip: [...]          # changes that run no sandbox test
always: Smoke        # added to every non-empty selection
alone: [NodeRestart] # run after the others, one at a time (they disturb the cluster)
full: Smoke|Identity|SSO|Gateway|Forgejo|Mesh|Monitoring|Egress|Secrets|Ntfy|Logs|NodeRestart
groups:
  - paths: [...]
    tests: <regex> | full   # full: the whole suite
components:          # what --check expects the map to cover
  - system/*
  - platform/*
  - apps/*.yaml
  - apps/*/
  - infra/_modules/*
  - infra/*/*/
```

A file that matches several groups gets their union. A changed `test/<name>_test.go` (other than helpers) selects the
tests it defines (`func Test…`, read with `go/parser`), so editing a test runs it.

Initial groups:

| Paths | Tests |
|---|---|
| `docs/**`, `**/*.md`, `mkdocs.yml`, `LICENSE.md`, `renovate.json5`, `.github/**` | skip |
| `Makefile`, `test/Makefile`, `test/scopes.yaml`, `flake.nix`, `flake.lock`, `nix/**`, `.envrc`, `system/argocd/**`, `infra/_modules/argocd/**`, `infra/sandbox/argocd/**`, `test/*helpers*_test.go`, `test/go.mod`, `test/go.sum` | full |
| `system/istio-system/**`, `infra/_modules/k3d/**`, `infra/sandbox/cluster/**` | `Mesh\|Gateway\|Egress\|Identity\|NodeRestart` |
| `system/egress/**` | `Egress` |
| `system/cert-manager/**` | `Gateway\|Identity\|SSO` |
| `system/loki/**` | `Logs` |
| `system/monitoring-system/**` | `Monitoring\|Mesh\|Ntfy\|Logs` |
| `system/secret-store/**`, `platform/openbao/**`, `platform/external-secrets/**`, `platform/global-secrets/**`, `platform/infra-secrets/**`, `infra/_modules/openbao/**`, `infra/_modules/secrets/**`, `infra/sandbox/openbao/**`, `infra/sandbox/secrets/**`, `infra/root.hcl` | `Secrets\|Identity\|SSO\|Ntfy` |
| `platform/kanidm/**` | `Identity\|SSO` |
| `platform/grafana/**` | `SSOGrafana\|Logs\|Monitoring` |
| `platform/forgejo/**` | `Forgejo\|SSOForgejo\|GatewayForgejoSSH` |
| `platform/zot/**` | `SSOZot\|GatewayZotLargePush` |
| `apps/actualbudget.yaml`, `apps/paperless.yaml`, `apps/opencloud.yaml`, `apps/ntfy.yaml` | `SSOActualBudget`, `SSOPaperless`, `SSOOpenCloud`, `Ntfy` (one group each) |
| The other apps; production-only components (`platform/cloudnative-pg`, `platform/renovate`, `platform/woodpecker`, `system/cloudflared`, `system/external-dns`, `system/kured`, `system/rook-ceph`, `system/volsync-system`, `infra/production/**`, `infra/_modules/cloudflare/**`, `metal/**`); `scripts/check-*`, `scripts/render-chart`, `scripts/env-values`, `cli/**`, `infra/Makefile` | `""` (Smoke only: the sandbox does not run them, or offline checks cover them) |

Everything else (other `scripts/`, new top-level files) is unknown: `full`.

## `lab test scope`

- `lab test scope [--base origin/dev] [--alone]`, run from anywhere in the repository:
  - reads `test/scopes.yaml` and the changed files of `<base>...HEAD`;
  - prints one line to stdout: a regex for `go test -run`, `full`'s regex, or nothing when every change is skipped;
  - without `--alone` the `alone` tests are left out; with `--alone` it prints only the selected `alone` tests (as a
    regex, or nothing);
  - the output is sorted and deduplicated, so the same change prints the same line.
- `lab test scope --check`: every path matching a `components` glob matches a group or `skip`; every group's regex (and
  `always`, `full`, `alone`) matches at least one `Test…` function in `test/`. Exits non-zero with the offenders.
- Errors: an unreadable or invalid `scopes.yaml`, a failing `git diff`, or a base that does not exist exit non-zero
  with the reason (for the base: `git fetch origin dev`, or `scope=full`). `make dev` stops; it never guesses.

## `make dev`

```make
scope ?= auto
base ?= origin/dev
tests ?=
```

- `scope=auto`: `git fetch -q origin dev`; main set = `lab test scope --base $(base)`, alone set = the same with
  `--alone`. Both empty: print "nothing to test in the sandbox" and stop before pushing.
- `scope=full`: main set = `full` without the alone tests, alone set = `alone`.
- `tests=<regex>` replaces both sets with that regex, run as the main set (`TestNodeRestart` in it still runs alone
  after the rest).
- Then as today: push, apply `infra/sandbox`, refresh, the main set, then each alone test.
- `make dev-fresh`: `make dev-clean` then `make dev scope=full`.

## Rules (CLAUDE.md)

| When | What |
|---|---|
| A feature-branch commit that changes code | Basic checks (now with `lab test scope --check` and `make -C cli test`), then `make dev` (scoped) |
| A commit that only touches skipped paths | Nothing more: `make dev` prints that there is nothing to test |
| Before the PR | The last scoped `make dev` already covered the whole branch (`dev...HEAD`) |
| After merging into `dev` | `make dev scope=full` on `dev`, unless every change was skipped |
| `dev` → `master` PR, a release | `make dev-fresh` |
| A new component | Add it to `test/scopes.yaml` in the same PR (`--check` fails otherwise; until then it runs everything) |

The "comment-only commits skip tests" convention becomes the `skip` list plus judgement for comments in code files.

## Tests

- `cli/internal/scope` unit tests, with throwaway git repositories and a small `scopes.yaml`:
  - only skipped files → empty;
  - files in two groups → their union plus `always`;
  - an unknown file → `full`;
  - a `full` group → `full`;
  - a changed test file → the tests it defines; a changed helper → `full`;
  - `--alone` → only the selected alone tests; the main output never contains them;
  - a missing base, an invalid `scopes.yaml` → errors;
  - `--check`: an uncovered component fails, a regex matching no test fails, the clean case passes.
- `make -C cli test` (`go vet`, `go test -race`) joins the basic checks, and so does `lab test scope --check` on this
  repository.
- This branch's own `make dev` runs scoped: it changes `Makefile` and `test/scopes.yaml`, which select `full`, so the
  first run is the full suite; a throwaway commit on top that touches only `system/loki` must select `Smoke|Logs`
  (checked with `lab test scope`, not pushed).

## Documentation

- CLAUDE.md: the basic-checks rule and the `make dev` rule become the table above; the Sandbox section names
  `test/scopes.yaml`, `lab test scope` and `make dev-fresh`; the roadmap records this step (done) and moves go-task to
  14b; the Ideas entry for the Go CLI notes that `cli/` exists with `lab test scope`.
- `docs/`: a short page under how-to guides on running sandbox tests (scoped, full, from scratch, a single group).

## Out of scope

- Woodpecker pipelines (step 10); the rest of the `lab` commands and its Nix package (the CLI step).
- Parallel tests or faster individual tests.
- Remembering the last tested commit to narrow the base.
