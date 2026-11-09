# 14f-2: the cluster tests on Ginkgo — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** the 84 cluster tests become Ginkgo specs in `test/e2e`, selected by labels, with a per-spec timeout from the
spec's context, and terratest leaves the module.

**Architecture:** `test/e2e` (package `e2e`, one entry `TestE2E`) holds one top-level `Describe` per old test, named
after it without `Test` (`Describe("SSOKiali", Label("sso", "kiali"), …)`); labels sit only on that container, as
string literals, from the fixed set in `test/scopes.yaml`'s `labels:`. `test/internal/kube` replaces terratest with
calls that take a `context.Context`. `lab test scope` reads the specs' labels from the source (go/ast), turns
`scopes.yaml`'s label filters into one main filter and one filter per alone spec, and evaluates them with Ginkgo's own
`types.ParseLabelFilter`. `just test e2e --env <env>` runs `go tool ginkgo` and adds `&& <env>` to every filter.

**Tech Stack:** Go 1.26, Ginkgo v2.34.0, Gomega v1.44.0 (latest on 2026-11-08: check again when pinning), testify
only in `test/offline`, chromedp.

**Spec:** `docs/superpowers/specs/2026-11-08-test-layout-design.md` (step 2, "14f-2")

## Global Constraints

- No spec asserts less than its test did, and no wait gets longer; a wait may become a spec timeout.
- Every scope group, `full`, `always`, the alone runs and the recipes' filters (`just testbed`, `testbed-power`,
  `testbed-scan`, `dev-scan`, production) select exactly the old tests that ran under them today (Task 1's record),
  except the spec's two intended differences: no offline test (already true since 14f-1) and no `TestTestbed*` on the
  sandbox.
- Labels: lower case, `-` between words, only the set below, only on top-level containers.
- `--fail-on-empty` on every Ginkgo run; one process (no `-p`); alone specs are `Serial`.
- Reports in `test/reports/e2e/` (git-ignored); GitHub Actions uploads them.
- `go tool ginkgo` (the `tool` directive in `test/go.mod`); no separate install.
- Main checkout only for `just dev` and `just testbed`; `ci-check` with the sandbox and testbed down.
- A sandbox or testbed step is a stop: the user starts it (they shut WSL down between sessions). So the groups are
  proven on the sandbox together in Task 13, one group's filter at a time in conversion order, not as each lands (the
  spec's wording): a conversion mistake costs a re-run of its group either way, since the test code runs from the
  checkout and the sandbox does not depend on it.

## Labels (the fixed set, `scopes.yaml` `labels:`)

Environment (from today's gates; `just test e2e --env X` adds `&& X`):

| Labels | Old tests (gate today) |
|---|---|
| `sandbox testbed production` | Smoke, ArgoCDCheck, MeshOptOutAllowed (no gate) |
| `sandbox testbed` | every `LocalOnly`/`*SandboxOnly`/`skipUnlessSandbox` test: Identity*, SSO*, Woodpecker, Gateway*, Forgejo*, Mesh* (but OptOutAllowed), Egress*, Monitoring*, BackupRestore, Secrets*, Ntfy*, Policy, SupplyChain, TrivyOperator, Logs*, SecurityLogs*, Rotation |
| `sandbox` | NodeRestart, NodePowerCut, PublicScan (`sandboxOnly`), ScanKubescape |
| `testbed` | Testbed*, Power*, ScanBench (`testbedOnly`) |

Family (exactly one per spec, from its old name): `smoke`, `argocd` (ArgoCDCheck), `identity`, `sso`, `gateway`,
`forgejo`, `mesh` (MeshMetrics too), `monitoring`, `egress`, `secrets`, `ntfy`, `logs`, `security-logs`,
`woodpecker`, `policy`, `supply-chain`, `trivy-operator`, `public` (PublicScan), `node-restart`, `node-power-cut`,
`backup-restore`, `rotation`, `proof` (Testbed*: what `just testbed` marks), `power`, `scan`.

Extra, where today's name-substring selection reached across families or a group names one test:

| Spec | Extra labels |
|---|---|
| SSOKiali, SSOGrafana, SSOArgoCD, SSOZot, SSOActualBudget, SSOPaperless, SSOForgejo, SSOOpenCloud | `kiali`, `grafana`, `argocd`, `zot`, `actual-budget`, `paperless`, `forgejo`, `opencloud` |
| GatewayForgejoSSH, GatewayZotLargePush, GatewayPublicAllowList | `forgejo`, `zot`, `public` |
| ForgejoDeploy | `deploy` |
| SecurityLogs* | `logs` (today `Logs` matches them) |
| NodeRestart, NodePowerCut, BackupRestore, Rotation | `alone` |
| PowerCutEveryNode, PowerShutdownStartup, PowerNodeNotReturning, PowerCutPostgres | `cut-every-node`, `shutdown-startup`, `node-not-returning`, `cut-postgres` |

`scopes.yaml` after the change (`tests:` is a Ginkgo label filter; `full` stays a keyword):

| Group (first path) | Filter |
|---|---|
| `full:` | `smoke \|\| identity \|\| sso \|\| gateway \|\| forgejo \|\| mesh \|\| monitoring \|\| egress \|\| secrets \|\| ntfy \|\| logs \|\| woodpecker \|\| policy \|\| supply-chain \|\| trivy-operator \|\| public \|\| node-restart \|\| node-power-cut \|\| backup-restore \|\| rotation` |
| `always:` | `smoke` |
| `alone:` | `[backup-restore, node-power-cut, node-restart, rotation]` (run in this order, as today's sorted regex) |
| `system/istio-system/**` | `mesh \|\| gateway \|\| egress \|\| identity \|\| node-restart \|\| node-power-cut` |
| `system/cert-manager/**` | `gateway \|\| identity \|\| sso` |
| `system/velero/**` | `backup-restore` |
| `cli/internal/rotate/**` | `rotation` |
| `system/loki/**` | `logs` |
| `system/kiali/**` | `kiali` |
| `system/kyverno/**` | `policy \|\| supply-chain` |
| `system/trivy-operator/**` | `trivy-operator` |
| `system/public-scan/**` | `public` |
| `system/monitoring-system/**` | `monitoring \|\| mesh \|\| ntfy \|\| logs` |
| `system/secret-store/**` | `secrets \|\| identity \|\| sso \|\| ntfy \|\| forgejo \|\| logs \|\| gateway \|\| monitoring \|\| woodpecker \|\| backup-restore` |
| `platform/kanidm/**` | `identity \|\| sso \|\| (forgejo && deploy) \|\| (gateway && zot) \|\| woodpecker` |
| `platform/grafana/**` | `grafana \|\| logs \|\| monitoring` |
| `platform/forgejo/**` | `forgejo \|\| woodpecker \|\| backup-restore` |
| `cli/internal/forgejo/**` | `forgejo \|\| woodpecker` |
| `platform/zot/**` | `zot \|\| woodpecker` |
| `platform/woodpecker/**` | `woodpecker` |
| `platform/secrets-rotation/**` | `rotation` |
| `apps/actualbudget.yaml`, `apps/paperless.yaml`, `apps/opencloud.yaml`, `apps/ntfy.yaml` | `actual-budget`, `paperless`, `opencloud`, `ntfy` |
| the infra group | `full`; the smoke-only group: `""` (unchanged) |

Task 1's record and Task 11's check prove this table, not the other way round: where they disagree, the record wins
and the table is fixed.

## Conversion rules (Tasks 3–10)

- One old `TestX` → `var _ = Describe("X", Label(…), func() { … })`, `Serial` (and `Ordered`) on alone specs.
- `t.Run` steps that build on each other → `Ordered` container, one `It` per step; independent subtests (tables) →
  one `It` per entry. A subtest's name becomes its `It` text.
- Every `It` takes `ctx SpecContext` and has `SpecTimeout(d)`: `d` is at least the longest single wait inside it plus
  5 minutes and at least twice its duration in `.superpowers/post-merge/test-layout-full.log` (`PASS TestX (Ns)`),
  rounded up to 5 minutes; testbed specs without a measured duration take the first rule only.
- Every wait takes the spec's context: `kube.*` (Task 2), `exec.CommandContext`, `http.NewRequestWithContext`,
  chromedp contexts derived from `ctx`, `Eventually(ctx, f).WithTimeout(old timeout).WithPolling(old tick)`.
- Assertions are Gomega (`Expect(err).NotTo(HaveOccurred())`, `Eventually(...).Should(...)` keeping the old
  message); e2e helpers drop `t` and call `Expect` themselves. `test/internal` helpers keep `testing.TB` (the offline
  tests use them), and e2e passes `GinkgoTB()`.
- Environment gates go (`LocalOnly`, `*SandboxOnly`, `testbedOnly`, `skipUnlessSandbox`, `sandboxOnly` and the inline
  `ENV` checks): the environment labels replace them. The only skips left: an optional app absent and not asked for
  (`skipUnlessDeployed`), and the testbed's two "not on its list" skips (CloudNativePG), which are the same kind.
  `skipUnlessSandbox`'s wait for the identity stack becomes a `BeforeEach` in the SSO and Woodpecker containers.
- Cleanups: `t.Cleanup`/`defer` that undo cluster changes → `DeferCleanup(func(ctx SpecContext) …)`; `t.TempDir()` →
  `GinkgoT().TempDir()`; `t.Parallel()` dropped.
- Paths through `repo.Root()`; never `".."`.

## Review Focus

- A spec that passes having checked nothing: a gate turned label where the gate also waited (`skipUnlessSandbox`), a
  `Skip` left behind, an `Eventually` without `.Should`. Task 11's dry-run check and the code reading at each task.
- A wait still blind to the spec's context (a `exec.Command`, `http.Get`, `time.Sleep`, `chromedp.NewContext(context.Background())`):
  each conversion task greps for them.
- A main filter that selects nothing (a branch touching only Velero): `lab` must print `""`, not a filter that
  `--fail-on-empty` fails. Task 11's tests.
- A label typed wrong in a spec or a filter (`trivy-operater`): known only through `scopes.yaml`; Task 11's
  `just cli check` and Task 11's check both fail.
- An alone spec reached through a manual `--tests` filter (`--tests 'node-restart || logs'`) must run alone, never in
  the main run. Task 11's tests.

---

### Task 1: record today's selection

**Files:** Create `test/offline/fixtures/e2e-selection.json`, `test/offline/e2e_selection_test.go`.

- [ ] **Step 1:** From `dev` before any move, write the record: for each `scopes.yaml` group (keyed by its first path),
  `full`, `always` and each alone term, the old test names it ran on the sandbox, as today's runner chose them. The
  main run used an unanchored `-run` (substring) over the non-alone terms, each alone term ran `^Test<term>$`, and the
  sandbox drops the testbed-gated tests (Testbed*, Power*, ScanBench). Add the recipes: `testbed/proof` (`Testbed`:
  the 8 Testbed*), `testbed/power/<label>` (one name each), `testbed/scan` (ScanBench), `sandbox/scan`
  (ScanKubescape) and `production` (Smoke, ArgoCDCheck, MeshOptOutAllowed). Names are sorted, without `Test`.
  Generate it with a throwaway script over `go test -list` (scratchpad, not committed), and put the generating commit
  in the file (`"from": "<sha>"`).
- [ ] **Step 2:** `TestE2ESelectionRecord` (offline): the JSON parses; every name in it is one of the 84 cluster tests;
  their union over `full`, the testbed recipes and `production` covers all 84 except ArgoCDCheck, which only
  `production` names.
- [ ] **Step 3:** `go test ./offline/ -run TestE2ESelectionRecord` PASS. Commit
  `test: record which cluster tests each scope and recipe runs today`.

### Task 2: `test/internal/kube`, the context-aware layer

**Files:** Create `test/internal/kube/kube.go`, `test/internal/kube/kube_test.go`.

**Interfaces (produces):**
- `Kubectl(ctx context.Context, namespace string, args ...string) (string, error)`: stdout and stderr combined, like
  terratest's `RunKubectlAndGetOutputE`; `namespace == ""` adds no `--namespace`; KUBECONFIG from the environment.
- `GetJSON(ctx context.Context, namespace string, out any, args ...string) error`: `get … --output json`, stdout only.
- `Apply(ctx, namespace, manifest string) error`, `Delete(ctx, namespace, manifest string) error` (stdin, `--wait`
  as terratest's `KubectlDeleteFromString` did: none), `CreateNamespace(ctx, name string) error`,
  `DeleteNamespace(ctx, name string) error`.

- [ ] **Step 1: failing tests** with a fake `kubectl` first on `PATH` (a script writing its args and stdin to a file):
  `TestKubectlNamespace` (`--namespace x get pods` vs none), `TestApplyPassesManifestOnStdin`,
  `TestGetJSONIgnoresStderr` (fake prints a warning on stderr), `TestKubectlStopsWithContext` (fake sleeps 60 s; a
  context cancelled after 100 ms returns within 2 s with `context.Canceled` in the error chain or a killed process).
- [ ] **Step 2:** RED (package missing). **Step 3:** implement with `exec.CommandContext`. **Step 4:** GREEN
  (`go test ./internal/kube/`). **Step 5:** commit `test: kube, kubectl calls bound to a context`.

### Task 3: the e2e package and its suite

**Files:** Move every `test/*_test.go` to `test/e2e/` (`git mv`, package `e2e`); create `test/e2e/e2e_suite_test.go`;
modify `test/go.mod` (ginkgo, gomega, `tool github.com/onsi/ginkgo/v2/ginkgo`), `test/justfile` (`run` builds
`./e2e`), `test/internal/testenv` (gets `K3dNode`/`NodeAddressFromInspect` from `restart`), `.gitignore`
(`test/reports/`), `test/scopes.yaml` and `cli/internal/scope` paths (`test/` cluster files → `test/e2e/`).

- [ ] **Step 1:** `TestE2E` registers Gomega's fail handler and runs the suite; old `Test*` functions keep running
  beside it (plain go tests in the same package) until converted.
- [ ] **Step 2:** paths through `repo.Root()` (fixes the deferred minor "cwd-relative `..`"); `K3dNode` and
  `NodeAddressFromInspect` into `testenv` (fixes "K3dNode's home").
- [ ] **Step 3:** `go test -list . ./e2e | grep ^Test | sort` = the 84 names + `TestE2E`;
  `direnv exec . scripts/ci-check go` and `just cli check` PASS (`lab test scope` follows the moved files: its
  repository test names `test/e2e/...`). Commit `refactor: the cluster tests in test/e2e, Ginkgo's suite beside them`.

### Tasks 4–10: convert, one group per task

Each task: convert its files by the rules above, give each container its labels from the tables, delete the
terratest imports there, then verify and commit `test: <group> on Ginkgo`.

- Verify: `go vet ./e2e/` clean; `go -C test test ./e2e -run '^TestE2E$' -args -ginkgo.dry-run -ginkgo.v
  -ginkgo.label-filter='<family labels>'` lists exactly the group's containers; `grep -nE 'exec\.Command\(|http\.Get|
  context\.Background\(\)|time\.Sleep|testing\.T\b' <files>` prints nothing (or a line the task's ledger explains).
- 4: smoke and argocd (`smoke_test.go`, `integration_test.go`, `env_helpers_test.go`, `gateway_helpers_test.go`);
  `routeHost` and `getResource` on `kube`.
- 5: identity, sso, kiali (`identity_helpers_test.go`, `identity_test.go`, `sso_test.go`, `kiali_test.go`); chromedp
  from the spec's context.
- 6: gateway, mesh, egress, monitoring (`gateway_test.go`, `mesh_test.go`, `mesh_metrics_test.go`, `egress_test.go`,
  `monitoring_test.go`).
- 7: secrets, ntfy, policy, supply-chain, trivy-operator, public (`secrets_test.go`, `ntfy_test.go`,
  `policy_test.go`, `supply_chain_test.go`, `trivy_operator_test.go`, `public_scan_test.go`).
- 8: logs, security-logs (`logs_test.go`, `security_logs_test.go`).
- 9: forgejo, woodpecker (`forgejo_test.go`, `woodpecker_sandbox_test.go`).
- 10: alone and testbed (`backup_test.go`, `node_restart_test.go`, `secrets_rotation_test.go`, `testbed_test.go`,
  `power_test.go`, `scan_test.go`): `Serial` on the alone ones, `Ordered` where steps build on each other; after this
  task no `func Test` but `TestE2E` is left in `test/e2e`, and terratest leaves `test/go.mod` (`go mod tidy`).

### Task 11: `lab test scope` on labels

**Files:** `cli/internal/scope/{scope.go,tests.go,check.go,labels.go}` and their tests, `cli/go.mod` (ginkgo for
`types.ParseLabelFilter`), `cli/internal/cli/test*.go`, `test/scopes.yaml`, `test/offline/e2e_selection_test.go`.

**Interfaces (produces):**
- `Specs(root string) ([]Spec, error)`, `Spec{Name string; Labels []string; File string}`: top-level `Describe`
  calls in `test/e2e/*_test.go`, their first argument and their `Label(...)` string literals.
- `Config.Filter(s Selection, specs []Spec) (main string, alone []string)`: main is `(<OR of the selected filters>)
  && !alone`, or `""` when it matches no spec labelled `sandbox`; alone lists, in `alone:` order, the family label of
  each alone spec the selection matches. `lab test scope` prints main, and with `--alone --each` one alone label per
  line, as now.
- A changed e2e file selects, per container it defines, the AND of that container's labels (environment and `alone`
  left out): never less than the file, sometimes more (ledger it). A changed `test/internal/<pkg>` file, deleted or not,
  selects through the e2e files importing the package (fixes "a deleted file selects nothing").

- [ ] **Step 1: failing tests** in `cli/internal/scope`: `TestSpecsReadsLabels`, `TestFilterOrsGroups`,
  `TestFilterEmptyMainWhenOnlyAlone` (group `backup-restore` → main `""`, alone `[backup-restore]`),
  `TestFilterManualReachesAlone` (`--tests 'node-restart || logs'` → main `(node-restart || logs) && !alone`, alone
  `[node-restart]`), `TestFileTestsE2EContainers`, `TestFileTestsInternalDeletedFile`, `TestCheckUnknownLabel` (a
  filter or a spec naming a label outside `labels:` fails `just cli check`), `TestCheckUnusedLabel`.
- [ ] **Step 2:** `scopes.yaml` as in the table above, with `labels:`; the offline `TestE2EKeepsSelection`: for every
  entry of Task 1's record, the specs (from `ginkgo --dry-run --json-report`, run by the test) that `<filter> && <env>`
  selects have exactly the record's names; `TestE2ELabelsOnlyOnContainers` (no leaf or nested labels) and
  `TestE2EOneContainerPerOldTest` (the 84 names, each once).
- [ ] **Step 3:** implement; GREEN: `go -C cli test ./internal/scope/`, `just cli check`,
  `go -C test test ./offline/ -run 'TestE2E'`. Commit `feat: lab test scope selects specs by label`.

### Task 12: recipes, CI, docs

**Files:** `test/justfile`, `justfile`, `.github/actions/sandbox/action.yml`, `.github/workflows/sandbox.yaml` (input
`tests`: a label filter), `flake.nix` (gotestsum if nothing else uses it), `docs/how-to-guides/sandbox-tests.md`,
`docs/installation/post-installation.md`, `docs/installation/testbed.md`, `docs/how-to-guides/power.md`,
`docs/how-to-guides/cluster-scan.md`, `docs/how-to-guides/ci.md`, `CLAUDE.md`, `docs/superpowers/deferred-minors.md`.

- [ ] **Step 1:** `just test e2e --env <sandbox|testbed|production> [--labels <filter>] [--focus <regex>] [--verbose]`
  replaces `run`: no `--env` → exit 1 naming the three; `go tool ginkgo run --label-filter "(<labels>) && <env>"
  --fail-on-empty --timeout 2h --poll-progress-after 60s --poll-progress-interval 30s --output-dir test/reports/e2e
  --junit-report <env>-<slug>.xml --json-report <env>-<slug>.json ./e2e` (`slug`: the filter with every other
  character than `a-z0-9-` as `_`, or `all`).
- [ ] **Step 2:** `just dev` (main and alone through `--labels`), `dev-scan` (`scan --verbose`), `testbed` (`proof`),
  `testbed-power` (`power_tests := "cut-every-node shutdown-startup node-not-returning"`, `--tests` a space-separated
  list of filters, one run each), `testbed-scan` (`scan --verbose`); the CI action uploads `test/reports/` as
  `e2e-reports` (`if: always()`).
- [ ] **Step 3:** docs and CLAUDE.md say labels where they said regexes and test names (`just dev --tests '<label
  filter>'`, `just test e2e --env production`, `--tests cut-postgres`); deferred-minors drops the three 14f-1 lines.
- [ ] **Step 4:** `just --dry-run dev --tests logs` and the other recipes' dry runs show the filters;
  `direnv exec . scripts/ci-check all` PASS. Commit `feat: just test e2e, the recipes on Ginkgo's labels`.

### Task 13: proof on the sandbox and the testbed (stop: the user starts it)

- [ ] **Step 1:** `just dev --tests smoke` (sandbox up on this commit), then each group's own filter with
  `just test e2e --env sandbox --labels '<family>'`, Tasks 4–10's order; fix in place, re-run that group.
- [ ] **Step 2:** `just dev --full`: 0 failures; the reports exist; record durations. `just dev-scan` runs kubescape.
- [ ] **Step 3:** sandbox down; `just testbed` (8 proof specs, the mark); `just testbed-scan`; `just testbed-power`
  only if the user agrees (it cuts every node, about an hour).
- [ ] **Step 4:** final review (opus), fixes, PR into `dev` with the rulings, the record check and the runs.
