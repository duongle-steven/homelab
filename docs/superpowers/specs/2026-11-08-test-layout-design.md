# 14f: test layout, Ginkgo for the cluster tests, benchmarks

## Intent

`test/` is one Go package of 214 tests: 130 offline (renders, scripts, checks) and 84 that need a cluster
(`TestRookCephAES256K` only renders a chart, but neither `ci-check`'s regex nor any scope selects it: it runs nowhere
today, the very bug below; and the regex's prefix `TestSecurityLogsRules` also runs `TestSecurityLogsRulesLoaded`, a
cluster test that skips offline). 14f-1 leaves 131 offline (with `TestRookCephAES256K` and its own
`TestRootIsTheRepository`) and 84 cluster tests. Three things hurt:

- `scripts/ci-check` runs the offline tests through a hand-written `-run` regex of 130 names: a new offline test that
  the regex misses never runs anywhere, silently.
- `test/scopes.yaml` and `lab test scope` pick cluster tests by test-name prefixes in a `-run` regex. A prefix matches
  more than meant (`Rotation` also matched `TestStatePassphraseRotation`, fixed in #78 by anchoring), and a run whose
  filter matches nothing passes.
- `go test`'s one `-timeout` covers a whole run: when the alone tests' run hit 60 minutes, the panic blamed whichever
  test was running (#77's post-merge run), and a slow test shows no progress until it ends.

The cluster tests move to Ginkgo, the framework of the Kubernetes ecosystem's e2e suites (Kubernetes' own
`test/e2e`, Cluster API, kubebuilder's scaffold) and of upstream's `tests/e2e`: labels and a label filter instead of
name regexes, `Serial`/`Ordered` for tests that need the cluster alone, a timeout per spec with progress reports,
`--fail-on-empty`, `--dry-run` listing and JUnit/JSON reports. The offline tests stay plain `go test` with testify,
the Go norm for unit and render tests, where Ginkgo's DSL adds nothing. A new benchmarks suite measures storage and
the mesh, to compare the testbed with production's hardware in step 12b.

Decided with the user (2026-11-08): convert all 84 cluster tests (no long-lived mix of two styles); keep the directory
`test/` (Kubernetes' and Cluster API's `test/e2e`; upstream's `tests/` is only its choice); add the benchmarks suite.

## Goals

- `test/offline/`: the 131 offline tests, plain `go test` + testify. `scripts/ci-check` runs the package
  (`go test ./offline/...`) with no list of names; a new test there runs in the basic checks by being there.
- `test/e2e/`: the 84 cluster tests as Ginkgo specs (`Describe`/`It`, Gomega's `Eventually` instead of testify's
  106 `assert.Eventually`), one suite entry (`TestE2E`). Every spec carries labels naming what it proves (a
  component: `identity`, `sso`, `kiali`, `forgejo`, …), so `test/scopes.yaml` lists labels and `lab test scope` prints
  a Ginkgo label filter.
- The alone tests (`NodeRestart`, `NodePowerCut`, `BackupRestore`, `Rotation`) carry the label `alone` and `Serial`;
  `just dev` runs the main specs (`!alone` within the selection), then each selected alone spec in its own Ginkgo run,
  in the order today's `alone` list gives (#78's behaviour: a run's failure or timeout names its own spec).
- A timeout per spec (`SpecTimeout`/`NodeTimeout` from the spec's context) for every spec that waits on the cluster,
  sized from today's measured durations; Ginkgo's progress report after 60 s of a slow spec, every 30 s; the suite's
  `--timeout` long enough for the full run.
- `--fail-on-empty` everywhere: a filter that selects nothing fails.
- JUnit and JSON reports under `test/reports/<suite>/` (git-ignored); GitHub Actions' sandbox jobs upload them as an
  artifact.
- `test/benchmarks/`: a Ginkgo suite run by hand (`just test benchmarks --env <env>`), not in `just dev` or CI:
  - storage: fio on the environment's default StorageClass (Ceph on the testbed, the CSI host path in the sandbox):
    random 4k read/write IOPS and sequential throughput, ReadWriteOnce (and ReadWriteMany where the class offers it);
  - mesh: throughput and latency between two pods on different nodes, in the ambient mesh (through ztunnel) and out
    of it, so the mesh's cost is a number;
  - each run writes a JSON report (environment, commit, date, the numbers) to compare later; no pass/fail bar beyond
    "the benchmark ran" (the numbers are what step 12b reads).
  The old `test/benchmark/` manifests (dbench with a `latest` tag, applied by hand) are removed.
- `test/internal/`: helpers both suites (or both e2e and benchmarks) use, as packages: the environment (`ENV`, the
  kubeconfig, the domain), clients, OpenBao and Kanidm access, render helpers the offline tests share.
- Ginkgo and Gomega pinned at their latest stable releases in `test/go.mod` (`tool github.com/onsi/ginkgo/v2/ginkgo`,
  run as `go tool ginkgo`, no separate install).
- The testbed's tests (`TestTestbed*`), the power tests (`testbed-power`) and the scans (`dev-scan`,
  `testbed-scan`) become e2e specs with labels `testbed`, `power` and `scan`, run by the same recipes through label
  filters; the sandbox's runs exclude them, as `ENV` does today.

## Non-goals

- No change to what any test checks: each spec asserts what its test asserted, with the same waits (a conversion
  may tighten a wait into a spec timeout, never loosen what it proves).
- No new e2e tests beyond the conversion (new behaviour gets its tests in its own step).
- No change to which tests a change runs: every `test/scopes.yaml` group selects the same tests through labels as it
  did through names (`lab test scope`'s output differs in form only).
- Not upstream's layout beyond what is chosen here (its `tests/`, its Python recipe, its inventory package).

## Anti-goals

- Two runners on `dev`: `dev` never has some cluster tests on `go test` and others on Ginkgo. The conversion lands
  in one pull request (14f-2), converted group by group in its commits, each group proven on the sandbox as it goes.
- A spec that passes because it ran nothing or skipped silently: an optional app's specs skip only as today (absent
  and not asked for), and fail when the app list names it.
- Lost coverage: the e2e suite's dry run lists at least the 84 tests' worth of specs, and a check compares the
  converted list against the old test names (each old test maps to one or more specs).
- Labels as free text: the label set is fixed in one place (a Go file of constants or `scopes.yaml`), and
  `just cli check` fails on a spec label or a scope label the other does not know.

## Steps

1. **14f-1** offline split: move the 131 offline tests and their fixtures to `test/offline/`, shared helpers to
   `test/internal/`; `ci-check` drops its regex; `test/scopes.yaml`'s skip list names `test/offline/**` instead of
   each file. Proof: `scripts/ci-check all` runs the 129 offline tests it ran plus `TestRookCephAES256K` (count compared), `just dev --full` once.
2. **14f-2** Ginkgo e2e: the suite, labels, `scopes.yaml` and `lab test scope` on labels, `just dev`/`just test` and
   the testbed recipes on Ginkgo, reports and the CI artifact; then the 84 tests converted group by group. Proof:
   each group with `just dev --tests '<labels>'` as it lands, `just dev --full` and `just testbed` at the end, and the
   old-name → spec check.
3. **14f-3** benchmarks: the suite and `just test benchmarks`, run once on the sandbox and on the testbed; the old
   manifests removed. Proof: both reports written, read in the PR.

## Interface

| today | after |
|---|---|
| `just dev --tests '<regex>'` | `just dev --tests '<label filter>'` (Ginkgo's syntax: `logs`, `sso && kiali`, `mesh \|\| gateway`) |
| `just test run [--env] [--filter <regex>] [--format]` | `just test e2e [--env] [--labels <filter>] [--focus <regex>]` (`--focus` on spec text, for one spec) |
| none | `just test benchmarks --env <env> [--labels storage\|mesh]` |
| `just testbed-power [--tests 'A B']` | `just testbed-power [--tests '<label filter>']`, each power spec still one run |
| `lab test scope … [--alone --each]` | same flags; prints label filters instead of regexes |

## Constraints

- The e2e specs read the environment as today (`ENV`, `INSECURE_SKIP_VERIFY`, the kubeconfig from `just`), and never
  target production unless the existing production checks did (`docs/installation/post-installation.md`'s smoke
  run keeps working: `just test e2e --labels smoke`).
- `Serial` and the alone runs keep the cluster to one spec at a time where today's suite ran tests one at a time
  (Ginkgo runs one process; no `-p`).
- The benchmarks' workloads meet the policy (securityContext, pinned images by digest or version, allowed registries)
  and clean up their namespace; they create no PVC without `homelab/backup: none`.
- Ginkgo's per-spec timeout uses the spec's `context.Context`: every wait inside a spec takes that context.

## Calls made after the builder check (mine, 2026-11-08)

- **terratest goes.** The suite calls it about 245 times (`k8s.RunKubectlAndGetOutputE`, `KubectlApplyFromString`,
  `WaitUntil*`, `http_helper`), and none takes a context: behind `GinkgoT()` a spec timeout would abandon the spec
  while its retries run on into the next one, the misattribution this step removes. `test/internal` gets a small
  layer that takes the spec's context (kubectl through `exec.CommandContext`, client-go, HTTP with the context), and
  every call site moves to it. This roughly doubles 14f-2; it is what makes per-spec timeouts real.
- **Environment gates become labels.** Today's in-test gates (`sandboxOnly`, `localOnly`, `meshSandboxOnly`,
  `secretsSandboxOnly`, `testbedOnly`, `skipUnlessSandbox`) become labels naming where a spec may run (`sandbox`,
  `testbed`, `production`; a spec for both local environments carries both). `just test e2e --env <env>` adds
  `&& <env>` to every filter itself, so a selected spec never skips for its environment; the only run-time skip left
  is an optional app absent and not asked for, as today. With `--fail-on-empty`, a filter that selects nothing for
  that environment fails.
- **No default environment.** `just test e2e` requires `--env` (no silent production run); production runs only
  specs labelled `production`, which are exactly the tests that run against production today (those with no
  environment gate: `TestSmoke`, `TestArgoCDCheck`, the others the builder finds ungated), and
  `docs/installation/post-installation.md` says `just test e2e --env production`.
- **Benchmarks on the sandbox and the testbed now; production in 12b.** Production needs policy exceptions and pull
  access the sandbox does not exercise, so 12b adds production to the suite. On the one-node sandbox the mesh
  benchmark measures two pods on the same node and records it as such in the report; the testbed measures across
  nodes.
- **Scopes keep today's effective selection.** Each group selects what its regex matches today, not what its words
  meant: `logs` covers the `TestSecurityLogs*` specs (today `Logs` matches them). The differences that go: offline
  tests no longer run inside `just dev` (23 matched today, e.g. `Policy`, `PublicScan`; the basic checks run them),
  and `TestTestbed*` specs no longer get selected on the sandbox to skip there. The old-name → spec check compares
  against today's effective selection per group.

## Left to the builder

- The label names and how groups map onto them (given the anti-goal above: one fixed set, checked).
- How `lab test scope` learns the spec labels for its checks (a `ginkgo --dry-run --json-report` it reads, or the
  constants file).
- Each spec's timeout value, from the durations the full runs logged.
- Which helpers become `test/internal/` packages and their names.
- fio's and the mesh benchmark's exact parameters and image (pinned), and the report's fields.
