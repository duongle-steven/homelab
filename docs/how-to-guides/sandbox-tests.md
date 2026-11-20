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

## Groups: the sandbox runs what the specs need

The sandbox is for testing what is being built, so a run deploys only the **core** (Argo CD's config, Istio,
cert-manager, the volumes, External Secrets, OpenBao, Kanidm, and every CRD from `system/crds`) and the **groups** its
specs need; everything else is removed. One file says which: `test/scopes.yaml`'s `core:` and `deployGroups:`
(`docs/superpowers/specs/2026-11-20-sandbox-groups-design.md`). A group goes in when:

- a selected spec carries one of its `labels` (a spec's every label counts: `BackstageTemplate` carries `ci`, so
  `--tests ci` also deploys `backstage`);
- the branch changes one of its components (compared with `--base`): an upgrade is tested before production;
- `--apps` names it: `just dev --apps paperless,opencloud`, `--apps all` for every group;
- a deployed group `needs` it (`backstage` needs `ci`, which needs `git`).

`just dev` prints them (`groups '...'`; `lab test scope ... --groups` prints the same) and refuses a name that is no
group. A group left out is removed: Argo CD deletes its Applications (the cluster Secret loses its label
`homelab/group-<name>`), and `just dev` deletes its namespaces, volumes included. lab's image is built only with
`backup` (the scheduled rotation) and Backstage's only with `backstage`. `--full`, `just dev-fresh` and GitHub Actions'
release, retarget and on-demand sandboxes (`--apps all`) deploy every group, so `master` is still proven on everything
together.

A spec whose labels need a group that is not deployed skips, naming the group; a group the run asked for but the
cluster lacks fails its specs instead. In the sandbox `lab secrets rotate --all` skips the secrets whose readers'
namespaces are gone ("not deployed here").

A new label goes in `core.labels` (its specs need nothing more) or in a group's `labels`; a new component in the core
or in one group, with its directory or app file in that group's generator in `system/argocd/values-dev.yaml`
(`just cli check` and `TestArgoCDSandboxGroups` fail otherwise).

Before closing WSL, `just dev-stop` stops the sandbox so it stays stopped (Docker would start it with WSL); the next
`just dev` rebuilds it from scratch.

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
