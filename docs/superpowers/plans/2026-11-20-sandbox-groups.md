# Sandbox Groups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A sandbox run deploys only the core plus the groups its selected specs need, and every CRD comes from one component, `system/crds`, so no component waits on another's CRDs.

**Architecture:** `test/scopes.yaml` gains `deployGroups:` (each group: its components, the groups it needs, the spec labels that need it); `lab test scope --groups` resolves a selection to groups, `just dev` passes them to the bootstrap as cluster Secret labels `homelab/group-<name>`, and `system/argocd/values-dev.yaml` gates each group's components by its label. `system/crds` holds every component's CRDs, generated from each component's own pinned chart by `scripts/vendor-crds` and checked by a basic check `crds`; the components stop installing theirs, and the workarounds that existed only for CRD order go.

**Tech Stack:** Go (`cli/internal/scope`, cobra), just, Helm, Argo CD ApplicationSets, yq, Ginkgo.

**Spec:** `docs/superpowers/specs/2026-11-20-sandbox-groups-design.md`

## Global Constraints

- `test/scopes.yaml` is the only list of groups; `lab test scope --check` (run by `just cli check`) fails when a sandbox component is in no group and not in the core, in two groups, or ungated in `values-dev.yaml`.
- Core (always deployed): `system/crds`, `system/argocd-config`, `system/istio-system`, `system/cert-manager`, `system/csi-hostpath`, `platform/external-secrets`, `platform/openbao`, `platform/generated-secrets`, `platform/infra-secrets`, `platform/kanidm`, and every Application's `system/egress` and `system/secret-store`.
- Groups: `metrics` (monitoring-system), `observability` (loki, tempo, opentelemetry, kiali, grafana; needs metrics), `policy` (kyverno), `security` (trivy-operator, falco, public-scan; needs metrics), `backup` (velero, secrets-rotation; needs git), `git` (cloudnative-pg, forgejo), `ci` (zot, argo-workflows, argo-events, ci; needs git), `backstage` (backstage; needs ci), one per app file (paperless, opencloud included).
- A run's groups: those of every label on every selected spec, plus (with `--base`) the group of every changed component, plus `--apps`; `--full` and `just dev-fresh` deploy all.
- GitHub Actions' release, retarget and on-demand sandboxes deploy every group.
- `system/crds` never prunes (`Prune=false`) and syncs first; Argo CD's own CRDs stay in `system/argocd`, the Gateway API's in k3s's manifests.
- The CRDs in `system/crds` are generated from each component's pinned chart; a basic check `crds` fails when they differ.
- Measured (Goal 7): `just dev --tests ci` under 75% of the node's memory, block reads under 50 MB/s on average; Goal 8: a fresh sandbox's first sync time before and after.
- Every rule in `CLAUDE.md` holds (latest stable, `homelab/backup` labels, securityContext, mesh `allow`, egress, rotation catalog, `test/scopes.yaml` entries, deferred minors).

## Review Focus

- **A Renovate pull request bumping a chart.** Its CRDs in `system/crds` are now stale, so the basic check `crds` fails and names `scripts/vendor-crds <component>`; the guide says so. Pinned in Task 1's check test.
- **A component whose chart keeps CRDs in `crds/` and also templates some.** Both kinds must leave its render; `TestNoComponentShipsCRDs` renders every component, not just the known ones. Task 1.
- **A spec selected but its group not deployed** (the resolver missed a label). It must skip with a message naming the group when the group was not asked for, and fail when it was. Task 4's `TestGroupsCoverSelection` checks that every selected spec's labels map to deployed groups for each of `test/scopes.yaml`'s groups and recipes.
- **A run that drops a group left from the previous run.** Its namespaces are deleted and its Applications gone; a leftover `PolicyException` or monitor stays inert because its CRD remains. Task 3's sandbox step checks the namespaces.
- **A fresh production cluster.** `system/crds` must apply before every other root Application, without the old sync waves; Task 2 renders production's root ApplicationSet and checks the wave and `Prune=false`.

---

### Task 1: `system/crds`, generated from the components' charts

**Files:**
- Create: `system/crds/Chart.yaml` (no dependencies; `homelab/*` annotations, no host), `system/crds/templates/` (one file per component, `<component>.yaml`, generated), `scripts/vendor-crds`
- Modify: every component that renders CRDs (`system/cert-manager`, `csi-hostpath`, `external-dns`, `istio-system`, `kyverno`, `monitoring-system`, `opentelemetry`, `rook-ceph`, `trivy-operator`, `velero`, `platform/argo-events`, `argo-workflows`, `cloudnative-pg`, `external-secrets`, `kanidm`): its chart's own switch off (`crds.install: false`, `installCRDs: false`, `crds.enabled: false`… whatever that chart names it); for a chart that only ships `crds/`, `skipCrds: true` in the `root` ApplicationSet's Helm source and in `scripts/render-chart`
- Modify: `scripts/ci-check` (check `crds`: `scripts/vendor-crds --check`), `test/scopes.yaml` (component `system/crds/**`, `scripts/vendor-crds`: tests `full`)
- Test: `test/offline/crds_test.go`

**Interfaces:**
- Produces: `scripts/vendor-crds [<component>...]` writes `system/crds/templates/<name>.yaml` for each component (all when none given), `<name>` the component's directory name; `scripts/vendor-crds --check` exits 1 and names each stale component with the command to regenerate it.
- Produces: `system/crds` renders only `CustomResourceDefinition`s, each annotated `argocd.argoproj.io/sync-options: Prune=false,ServerSideApply=true`.

- [ ] **Step 1: Write the failing tests** in `crds_test.go`:
  - `TestNoComponentShipsCRDs`: for every `system/*` and `platform/*` chart except `system/argocd` and `system/crds`, `scripts/render-chart <dir>` and `<dir> dev` render no `CustomResourceDefinition`.
  - `TestCRDsInOnePlace`: `scripts/render-chart system/crds` holds at least the CRDs `certificates.cert-manager.io`, `externalsecrets.external-secrets.io`, `policyexceptions.kyverno.io`, `servicemonitors.monitoring.coreos.com`, `authorizationpolicies.security.istio.io`, `kanidmoauth2clients.kaniop.rs`, `clusters.postgresql.cnpg.io`, `workflows.argoproj.io`, `backups.velero.io`, `volumesnapshots.snapshot.storage.k8s.io`; every one carries `Prune=false`; no name appears twice.
  - `TestVendorCRDsCheck`: in a copy of the repository with one line removed from `system/crds/templates/kyverno.yaml`, `scripts/vendor-crds --check` exits non-zero and prints `scripts/vendor-crds kyverno`.
- [ ] **Step 2: Run** `go -C test test ./offline -run 'CRDs'`. Expected: FAIL.
- [ ] **Step 3: Implement** `scripts/vendor-crds`: for each component, `scripts/render-chart <dir>` with the chart's CRD switch forced on (`--set` of that chart's key, read from a table at the top of the script) plus `--include-crds`, keep `kind == "CustomResourceDefinition"`, add the annotation, write the file with a header naming the component and its chart version. Then switch every component's CRDs off and run `scripts/vendor-crds`.
- [ ] **Step 4: Run** the tests and `scripts/ci-check render crds policy mesh env-config`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(crds): every component's CRDs in system/crds, generated from its chart`

### Task 2: `system/crds` first; the CRD-order workarounds go

**Files:**
- Modify: `system/argocd/values.yaml`, `values-dev.yaml`, `values-testbed.yaml`, `values-seed.yaml` (the root generators list `system/crds`; the `root` `templatePatch` gives it `argocd.argoproj.io/sync-wave: "-1"` on the Application)
- Modify: every `SkipDryRunOnMissingResource` and `sync-wave` whose comment gives a missing CRD as the reason (33 and 12 today, found with `git grep`); keep those with another reason (ESO's webhooks, monitoring-system's operator-before-Prometheus waves, `values-dev.yaml`'s retry backoff) and say so in their comments
- Modify: `CLAUDE.md` (the "first sync" paragraph: CRDs first from `system/crds`; drop the cycle explanation), `docs/reference/architecture/overview.md`
- Test: `test/offline/crds_test.go` (extend), the offline tests that assert the removed annotations (update them)

- [ ] **Step 1: Write the failing tests**: `TestCRDsSyncFirst` (production's and the sandbox's rendered root ApplicationSet put `system/crds` in wave `-1`, no other Application below 0); `TestNoCRDOrderWorkarounds` (no rendered resource of any component carries `SkipDryRunOnMissingResource` except an allow-list in the test, each entry with its reason).
- [ ] **Step 2: Run** them. Expected: FAIL.
- [ ] **Step 3: Implement** the changes above; update the offline tests that asserted the removed annotations.
- [ ] **Step 4: Run** `go -C test test -timeout 20m ./offline/...` and `scripts/ci-check render env-config mesh`. Expected: PASS.
- [ ] **Step 5: Measure before merging the next tasks**: `just dev-fresh --tests smoke` from the main checkout on the commit before Task 1 and on this one; record each first sync's time to every Application Synced and Healthy (`just dev`'s log). Expected: PASS both; the times go to the ledger and Task 6's guide.
- [ ] **Step 6: Commit** `refactor(argocd): system/crds syncs first; the CRD-order workarounds removed`

### Task 3: Groups in `test/scopes.yaml`; `lab test scope --groups`

**Files:**
- Modify: `cli/internal/scope/scope.go` (`Config.DeployGroups map[string]DeployGroup` from `deployGroups:`; `Config.Core []string` from `core:`), `cli/internal/scope/check.go`, `cli/internal/cli/test.go` (`--groups`), `test/scopes.yaml`
- Test: `cli/internal/scope/scope_test.go`, `check_test.go`, `cli/internal/cli/test_test.go`

**Interfaces:**
- Produces: `type DeployGroup struct { Components []string \`yaml:"components"\`; Needs []string \`yaml:"needs"\`; Labels []string \`yaml:"labels"\` }`.
- Produces: `func (c Config) GroupsFor(selection Selection, specs []Spec, changed []string) []string`: the sorted names of the groups whose `Labels` meet a label of a selected spec, or whose `Components` match a changed file, closed over `Needs`.
- Produces: `lab test scope [--base|--tests|--full] --groups` prints the group names, one per line (`--full`: every group).
- Produces: `--check` also fails on a component (a `values-dev.yaml` root directory or `apps` file) in no group and not in `core`, in two groups, or a group naming an unknown label or need.

- [ ] **Step 1: Write the failing tests**:
  - `TestGroupsFromLabels`: a fixture map with groups `metrics` (labels `monitoring`), `observability` (labels `logs`, needs `metrics`); selecting a spec labelled `logs` gives `[metrics observability]`.
  - `TestGroupsFromChanges`: a changed `apps/ntfy.yaml` with no spec selected gives `[ntfy]`.
  - `TestGroupsFull`: `--full` gives every group.
  - `TestCheckGroups`: a component in two groups, in none, and a need naming no group each fail with the component's or group's name.
  - In `test_test.go`: `lab test scope --tests logs --groups` on the fixture prints `metrics\nobservability\n`.
- [ ] **Step 2: Run** `just cli::test`. Expected: FAIL.
- [ ] **Step 3: Implement** the types, `GroupsFor`, the flag and the check; fill `test/scopes.yaml`'s `core:` and `deployGroups:` with Global Constraints' groups, each group's `labels` from the labels of the specs that exercise it (a label of the core lists no group).
- [ ] **Step 4: Run** `just cli::test cli::check`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(scope): deploy groups; lab test scope --groups`

### Task 4: The sandbox deploys its groups

**Files:**
- Modify: `system/argocd/values-dev.yaml` (the core in the existing generators; one `matrix` generator per group selecting `homelab/group-<name>: "true"`, listing its directories in `root` and its app files in `apps`; paperless's and OpenCloud's existing generators become their groups'), `infra/_modules/argocd/cluster-secret.sh` (`OPTIONAL_APPS` → `SANDBOX_GROUPS`, labels `homelab/group-<name>`), `infra/_modules/argocd/main.tf` and `infra/sandbox/argocd/terragrunt.hcl` (`HOMELAB_SANDBOX_GROUPS`), `justfile` (`dev`: the groups from `lab test scope --groups` plus `--apps`; deletes the namespaces of the groups left out, each group's namespaces being its components' directory names and app names; `dev-stop`: `k3d cluster stop homelab-dev` and the proxy stopped), `.github/workflows/sandbox.yaml` (`--apps all` keeps meaning every group)
- Modify: `test/e2e/smoke_test.go` (`optionalApps` → the groups from `test/scopes.yaml`, read through `test/internal/scopes`), every top-level `Describe` of a non-core component (`skipUnlessDeployed(ctx, "<group>")` first, before its waits), `test/offline/argocd_cluster_secret_test.go` (`TestArgoCDOptionalApps` → `TestArgoCDSandboxGroups`), `test/offline/fixtures/e2e-selection.json`
- Test: `test/offline/argocd_cluster_secret_test.go`, `test/offline/sandbox_groups_test.go`

**Interfaces:**
- Consumes: Task 3's `--groups`.
- Produces: `just dev [--apps a,b]` prints `just dev: groups '<names>'`; `just dev-stop`.

- [ ] **Step 1: Write the failing tests**:
  - `TestArgoCDSandboxGroups`: each group's generator in `values-dev.yaml` selects `homelab/group-<name>` and lists exactly that group's components from `test/scopes.yaml`; the core's generators list exactly the core.
  - `TestClusterSecretGroups`: `cluster-secret.sh` with `SANDBOX_GROUPS="metrics ci"` writes labels `homelab/group-metrics` and `homelab/group-ci`, no `homelab/app-`.
  - `TestGroupsCoverSelection` (`sandbox_groups_test.go`): for each `test/scopes.yaml` path group and recipe, every spec it selects has each of its labels covered by a deployed group or the core.
- [ ] **Step 2: Run** them. Expected: FAIL.
- [ ] **Step 3: Implement** the files above.
- [ ] **Step 4: Run** `go -C test test -timeout 20m ./offline/...`, `just cli::check`, `scripts/ci-check env-config render lint`. Expected: PASS.
- [ ] **Step 5: Run** `just dev --tests logs` from the main checkout. Expected: `just dev: groups 'metrics observability'`, the specs pass, and no namespace of `security`, `backup`, `git`, `ci`, `backstage` or any app exists afterwards.
- [ ] **Step 6: Commit** `feat(sandbox): deploy only the groups the selected specs need`

### Task 5: Measured

- [ ] **Step 1: Run** `just dev --tests ci` with the sampler (`docker stats` of the k3d node every 15 s: memory and cumulative block I/O). Expected: memory under 75% of the node's limit throughout; block reads under 50 MB/s on average over the run; the specs pass.
- [ ] **Step 2: Record** the numbers, Task 2's first-sync times and 2026-11-19's baseline in `docs/how-to-guides/sandbox-tests.md` (a section "What a run deploys").
- [ ] **Step 3: Commit** `docs(sandbox): what a run deploys, measured`

### Task 6: Docs

**Files:**
- Modify: `docs/how-to-guides/sandbox-tests.md` (groups, `--apps` with group names, `just dev-stop` before closing WSL), `docs/installation/sandbox.md`, `CLAUDE.md` (the Sandbox section and 14e: groups instead of optional apps; `system/crds`; `just dev-stop`), `docs/how-to-guides/renovate.md` (a chart bump needs `scripts/vendor-crds <component>`), `docs/superpowers/deferred-minors.md`
- [ ] **Step 1: Write** the docs above.
- [ ] **Step 2: Run** `scripts/ci-check all`. Expected: PASS.
- [ ] **Step 3: Commit** `docs(sandbox-groups): groups, system/crds and just dev-stop`
