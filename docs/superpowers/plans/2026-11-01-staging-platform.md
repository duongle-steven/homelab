# Staging platform (12a-2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Argo CD, the platform and as many apps as fit run on staging through production's deploy path, and a
release that changes what staging runs needs staging's mark.

**Architecture:** A shared values layer (`values-local.yaml`) gives staging and the sandbox the local CA and the
tests' fixtures; staging's Argo CD is production's set minus an exclusion list in `system/argocd/values-staging.yaml`,
bootstrapped from GitHub branch `staging`, then switched to `ops/homelab` on staging's Forgejo. `make staging` runs the
suite there and writes a GitHub commit status `staging`; `lab infra forgejo verify` derives the gated paths and looks
for that mark.

**Tech Stack:** Helm, Argo CD ApplicationSets, OpenTofu/Terragrunt, Go (`cli/`, `test/`), POSIX sh, yq, `gh`.

**Spec:** `docs/superpowers/specs/2026-11-01-staging-platform-design.md`

## Global Constraints

- Prerequisite: the pull request removing ollama is merged into `dev`, and this branch is rebased on it.
- Ideally staging is production; the reduction is one exclusion list, nothing else differs except
  `values-staging.yaml` where staging must.
- Layers: production `values.yaml`; staging `values.yaml`, `values-local.yaml`, `values-staging.yaml`; sandbox
  `values.yaml`, `values-local.yaml`, `values-dev.yaml`.
- The sandbox's renders are identical before and after the split.
- Fallback order: drop apps; squeeze the core (Ceph's OSD toward 900 MB, Prometheus off); 3.75 GB per VM with WSL at
  12 GB; WSL at 13 GB. Three nodes and Istio stay. Whatever is left out goes in the exclusion list; 12a-2 ships with
  what fits.
- Gated paths: `infra/staging/release-paths.yaml` plus every component and app staging deploys (production's set
  minus the proved commit's exclusion list). Compared against `ops/homelab`'s `master` on Forgejo; no Forgejo: all
  changed.
- The mark needs every test green, the destructive ones included (`TestBackupRestore`, `TestNodeRestart`,
  `TestRotation`).
- CLAUDE.md's rules: pinned latest versions, `config.json` for environment settings, the secrets catalog, backup
  labels, PolicyExceptions, mesh policies, `test/scopes.yaml`, deferred minors. Never print secrets; never run
  `make staging` or `make dev` from a worktree; staging and the sandbox never run together.

## Review Focus

- A release where only a docs file changed after the mark: `verify` passes without a rerun (Task 6's
  `TestStagingGateAcceptsEarlierMark`).
- A release that changes an excluded app only: no mark needed; a release that empties the exclusion list:
  every component counts (Task 6's `TestStagingGateExcludedApp`, `TestStagingGateUsesProvedExclusions`).
- A first production install with no Forgejo: everything counts as changed, so the mark is required (Task 6's
  `TestStagingGateNoDeployedCommit`).
- `--skip-staging` with an empty or blank reason: refused (Task 6's `TestSkipStagingNeedsReason`).
- A staging run interrupted after the switch to Forgejo: the next `make staging` keeps Argo CD on Forgejo, never back
  to the seed (Task 3: `choose-values.sh`'s existing rule, asserted by `TestStagingBootstrapKeepsForgejo`).

---

### Task 1: The shared values layer

**Files:**
- Create: `<component>/values-local.yaml` for each of the 17 components with a `values-dev.yaml`, holding what both
  non-production environments need: the local CA and its trust (cert-manager's `homelab-dev-ca` issuer named
  `letsencrypt-prod`, trust-manager's bundle, `SSL_CERT_DIR`), the tests' fixtures (Kanidm's `smoke` client and
  `dev-admin`, Woodpecker's admin, the rotation CronJob on lab's image built from the checkout), and the S3 stand-in
  for Velero (versitygw). What only the one-node sandbox needs (retentions, sizes, worker counts, the hostpath CSI, the
  CoreDNS rewrite of `*.127-0-0-1.nip.io`, retries) stays in `values-dev.yaml`.
- Modify: `scripts/render-chart` (load `values-local.yaml` for every env but `prod`, before `values-<env>.yaml`),
  `scripts/env-values` (accept `staging`), `system/argocd/values-dev.yaml` (root's `valueFiles`: `values.yaml`,
  `values-local.yaml`, `values-dev.yaml`), `scripts/check-*` that list values files
- Create: `scripts/compare-renders` (renders every component and app for an env at two revisions with
  `scripts/render-chart`, prints differing names; exit 1 on any)
- Test: `test/values_layers_test.go`

**Interfaces:**
- Produces: `scripts/render-chart <dir> staging` renders `values.yaml`, `values-local.yaml`, `values-staging.yaml`;
  `scripts/env-values <prod|dev|staging> <root|apps>`.

- [ ] **Step 1: Write the failing test**

`TestValuesLayers`: for `system/cert-manager`, `scripts/render-chart` with env `staging` contains the `homelab-dev-ca`
issuer and with env `prod` does not; for `system/monitoring-system`, the `staging` render keeps production's
Prometheus retention while the `dev` render keeps the sandbox's one day (assert both values from the files).

- [ ] **Step 2: Run it**: `cd test && direnv exec .. go test -run TestValuesLayers -count=1 .` → FAIL (no layer).
- [ ] **Step 3: Split the 17 files and teach the scripts and `values-dev.yaml` the layer.**
- [ ] **Step 4: Run** `TestValuesLayers` → PASS; `scripts/compare-renders dev origin/dev HEAD` → no differences.
- [ ] **Step 5: Run** `direnv exec . scripts/ci-check all` (sandbox stopped) → exit 0.
- [ ] **Step 6: Commit** `refactor(values): values-local.yaml, the layer staging and the sandbox share`.

### Task 2: Tests that know their environment

**Files:**
- Create: `test/env_test.go`
- Modify: every test with a hard-coded `127-0-0-1.nip.io` host, and the `*SandboxOnly` gates
- Test: `test/env_test.go`

**Interfaces:**
- Produces: `testDomain(t) string` (`domain` of `infra/<sandbox|staging>/cluster/config.json` from `ENV`
  `dev|staging`); `localOnly(t)` (skips unless `ENV` is `dev` or `staging`); `sandboxOnly(t)` (skips unless `dev`,
  for k3d, docker, the hostpath CSI and the localhost proxy), each skip naming why.

- [ ] **Step 1: Write the failing test** `TestTestDomain`: with `ENV=dev` it returns `127-0-0-1.nip.io`, with
  `ENV=staging` `192-168-100-2.nip.io`, with `ENV=prod` it fails the test.
- [ ] **Step 2: Run it** → FAIL (undefined).
- [ ] **Step 3: Implement the helpers; replace hosts with `"<app>." + testDomain(t)`; move each gate to
  `localOnly` unless the test needs k3d (docker, `sandboxAddress`, the hostpath CSI, the proxy), which keeps
  `sandboxOnly`.**
- [ ] **Step 4: Run** `go vet ./... && go test -run 'TestTestDomain|TestValuesLayers' .` → PASS; with `ENV=dev`, a
  scoped `make dev` → the same tests pass as before.
- [ ] **Step 5: Commit** `test: the suite reads its domain and gates from ENV, staging included`.

### Task 3: Staging's Argo CD and its seed

**Files:**
- Create: `system/argocd/values-staging.yaml` (production's ApplicationSets restated where Helm replaces lists: root's
  directories with staging's extra `exclude: true` entries under one comment naming the machine they were sized for;
  apps' files likewise (an excluded app: `exclude: true` if Argo CD's git files generator accepts it, else apps listed
  as in `values-dev.yaml`); `valueFiles` `values.yaml`, `values-local.yaml`, `values-staging.yaml`;
  `global.publicGateway: gateway`, `global.devCA: "true"`; the sandbox's retries)
- Create: `infra/staging/{openbao,secrets,argocd}/terragrunt.hcl` (like the sandbox's; `argocd`: `values =
  "values-staging.yaml"`, `environment = "staging"`, `seed = "values-staging.yaml"`, `seed_revision = "staging"`)
- Modify: `infra/_modules/argocd/{main.tf,bootstrap.sh}` (input `seed_revision`: when `choose-values.sh` picks the
  seed and `seed_revision` is set, the render's Forgejo repository becomes `seed_repository` at that revision, through
  `set-revision.sh`; production keeps `values-seed.yaml`)
- Modify: `Makefile` `staging` (force-push HEAD to GitHub branch `staging`, apply `infra/staging`, wait for Argo CD's
  sync, `lab infra forgejo sync` then `push --env staging`, apply `infra/staging/argocd` again to switch; drop the
  hand-applied rook-ceph render)
- Test: `test/argocd_revision_test.go`

**Interfaces:**
- Consumes: Task 1's layer; Task 4's `--env staging`.
- Produces: staging's exclusion list as `exclude: true` entries in `values-staging.yaml`, which Task 6 parses.

- [ ] **Step 1: Write the failing tests** `TestStagingSeedSyncsBranch` (rendering the bootstrap with
  `environment=seed`, `seed_revision=staging` gives every homelab source `seed_repository` at `staging`) and
  `TestStagingBootstrapKeepsForgejo` (with root already on Forgejo, `choose-values.sh` returns `values-staging.yaml`,
  environment `staging`).
- [ ] **Step 2: Run** `go test -run 'TestStagingSeed|TestStagingBootstrap' .` → FAIL.
- [ ] **Step 3: Implement the module input, the units, `values-staging.yaml` and the Makefile flow.**
- [ ] **Step 4: Run** the tests → PASS; `make -C infra check` → exit 0.
- [ ] **Step 5: Commit** `feat(staging): Argo CD from the seed branch, then ops/homelab on staging's Forgejo`.

### Task 4: `lab infra forgejo --env staging`

**Files:**
- Modify: `cli/internal/cli/infra_forgejo.go` (environment `staging`: endpoint through the Gateway VIP
  `service_vips.gateway` of `infra/staging/cluster/config.json`, trusting `forgejo/homelab-dev-ca`; push builds a
  commit with HEAD's tree like the sandbox; guards skipped), `cli/internal/forgejo/endpoint.go`
- Test: `cli/internal/cli/infra_forgejo_test.go`, `cli/internal/forgejo/endpoint_test.go`

**Interfaces:**
- Produces: `--env staging` for `sync` and `push`; `stagingEndpoint(ctx, kube, cfg) (forgejo.Endpoint, func(), error)`.

- [ ] **Step 1: Write the failing test** `TestForgejoEnvStaging`: `--env staging` loads staging's domain and
  kubeconfig; `verify --env staging` is refused (production only).
- [ ] **Step 2: Run** `cd cli && direnv exec .. go test ./internal/cli/ -run TestForgejoEnvStaging` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make -C cli test check` → PASS.
- [ ] **Step 5: Commit** `feat(cli): lab infra forgejo sync and push for staging`.

### Task 5: Staging runs, measured

**Files:**
- Modify: `system/argocd/values-staging.yaml` (the exclusion list as measured), `values-staging.yaml` of components
  squeezed by the fallback order, `infra/staging/vms` (memory, only if the fallback reaches it),
  `platform/*/values-local.yaml` where a fixture is missing on staging, `cli/internal/rotate/catalog.yaml`
  (`environments` gains `staging` where an entry runs there), `Makefile` (`staging` runs the suite: `ENV=staging`, the
  main groups then `BackupRestore|NodeRestart|NodePowerCut|Rotation` alone, like `make dev`)
- Modify: `docs/installation/staging.md` (what runs, the exclusion list and why, the measured table)

- [ ] **Step 1: Stop the sandbox; `make staging-fresh`** with the memory logger (each VM's QEMU RSS, WSL's available
  and swap every 30 s); add components in the spec's order; after each, record idle and under-test memory.
- [ ] **Step 2: When WSL's available memory falls under 1.5 GB or swap passes 1 GB, apply the next fallback step**;
  record each step taken.
- [ ] **Step 3: Run `make staging`** until the whole suite passes on staging (fix tests whose gate or fixture is wrong,
  not the expectation).
- [ ] **Step 4: Commit** `feat(staging): the platform and apps that fit, measured`.

### Task 6: The mark and the release gate

**Files:**
- Create: `infra/staging/release-paths.yaml` (globs: `infra/_modules/metal/**`, `infra/_modules/argocd/**`,
  `infra/staging/**`, `infra/production/**`, `system/argocd/**`, `system/istio-system/**`, `system/rook-ceph/**`,
  `cli/internal/forgejo/**`, `cli/internal/cluster/**`, `Makefile`)
- Create: `cli/internal/guard/staging.go`, `scripts/staging-mark`
- Modify: `cli/internal/guard/{verify.go,github.go}` (`CommitStatus`), `cli/internal/cli/infra_forgejo.go`
  (`--skip-staging`), `Makefile` (`staging` ends with `scripts/staging-mark HEAD` only when every test passed)
- Test: `cli/internal/guard/staging_test.go`, `test/release_paths_test.go`

**Interfaces:**
- Produces:
  - `func GatedPaths(root, provedCommit string) ([]string, error)`: `release-paths.yaml`'s globs plus
    `system/<x>/**`, `platform/<x>/**`, `apps/<x>.yaml`, `apps/<x>/**` for every component production deploys that
    the proved commit's `values-staging.yaml` does not exclude
  - `func StagingGate(ctx, root, repository, deployed, release string, candidates []string, gh GitHub) error`:
    `deployed` empty means everything changed; passes when a candidate (newest first) has status `staging` success
    and `git diff --name-only <candidate> <release>` touches none of `GatedPaths(root, candidate)`, or when
    `git diff --name-only <deployed> <release>` touches none
  - `func (g GitHub) CommitStatus(ctx, repository, sha, context string) (string, error)` (state, `""` if none)
  - `scripts/staging-mark <sha>`: `gh api repos/<repo>/statuses/<sha>` with context `staging`, state `success`
  - `verify --skip-staging=<reason>`: refused when blank; prints the reason and records it as status `staging`
    `success` with description `skipped: <reason>` on the release commit through `scripts/staging-mark`

- [ ] **Step 1: Write the failing tests** in `staging_test.go` (a git fixture repo and a fake GitHub):
  - `TestStagingGateAcceptsEarlierMark`: marked commit, then a docs-only commit → pass
  - `TestStagingGateRefusesUnmarked`: `system/rook-ceph/values.yaml` changed, no mark → error naming the path
  - `TestStagingGateExcludedApp`: only `apps/excalidraw.yaml` changed, excalidraw excluded → pass
  - `TestStagingGateUsesProvedExclusions`: the release empties the exclusion list but the proved commit excluded
    excalidraw → excalidraw's change still needs no mark from that proof
  - `TestStagingGateNoDeployedCommit`: `deployed` empty, no mark → error
  - `TestSkipStagingNeedsReason`: `--skip-staging=" "` → error

  and `TestReleasePathsExist` in `test/release_paths_test.go`: every glob in `release-paths.yaml` matches a file.
- [ ] **Step 2: Run** `cd cli && direnv exec .. go test ./internal/guard/ -run 'TestStagingGate|TestSkipStaging'` →
  FAIL.
- [ ] **Step 3: Implement; `Verify` reads the deployed commit from `ops/homelab`'s `master` (the session `sync` uses;
  empty when Forgejo is absent) and the candidates `git rev-list --first-parent <deployed>..<pull head>`.**
- [ ] **Step 4: Run** `make -C cli test check` and `go test -run TestReleasePathsExist ./test/` → PASS.
- [ ] **Step 5: Commit** `feat(cli): verify needs staging's mark for a release that changes what staging runs`.

### Task 7: Documentation

**Files:**
- Modify: `docs/installation/staging.md` (deploy path, the mark, `--skip-staging`), `docs/how-to-guides/deploy-to-forgejo.md`,
  `CLAUDE.md` (roadmap 12a-2 done; the values layers in Sandbox; the before-production notes this proves),
  `test/scopes.yaml` (new paths), `docs/superpowers/deferred-minors.md`
- [ ] **Step 1: Write them; `mkdocs build --strict` → exit 0; `scripts/ci-check all` (sandbox and staging stopped)
  → exit 0.**
- [ ] **Step 2: Commit** `docs(staging): the platform, the mark and verify's gate; roadmap 12a-2`.
