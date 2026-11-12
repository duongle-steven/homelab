# Production's Platform (12b-2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Production's platform (`system/*`, `platform/*`, no app) on the hardware through a stage switch, Ceph healthy,
an etcd disk alert, and a power cut with data, before any app.

**Architecture:**
- A label `homelab/stage` on Argo CD's cluster Secret gates whatever deploys an app: the `apps` ApplicationSet, `root`'s
  `apps/*` directories (Matrix) and `system/public-scan`.
- The bootstrap resolves the stage: unset keeps the cluster's label, a new cluster gets `all`, and it never moves back from
  `all` to `platform`.
- The first production deploy runs from a `master` release with the stage set to `platform`.

**Tech Stack:** Argo CD ApplicationSets (Helm values), OpenTofu/Terragrunt (`infra/_modules/argocd`), POSIX sh, Prometheus
rules, Ginkgo, testify, just.

**Spec:** `docs/superpowers/specs/2026-11-12-production-platform-design.md`

## Global Constraints

- **The label:** `homelab/stage`, values `platform` or `all`.
  - The variable is `HOMELAB_PRODUCTION_STAGE` (production only).
  - The sandbox and the testbed are always `all`.
- **Resolving the stage:** requested `platform` or `all` wins, except `platform` on a cluster labelled `all` fails. Unset
  keeps the current label. Unset with no label gives `all`.
- **Selectors:** every selector that gates apps matches `homelab/bootstrap: "true"` and `homelab/stage: all`. Selectors
  for anything else are unchanged.
- **Restated generators:** the generators restated in `values-dev.yaml`, `values-testbed.yaml` and `values-seed.yaml`
  change with `values.yaml`'s (Helm replaces lists; `scripts/check-env-config` checks them).
- **etcd alerts** (warning):
  - `EtcdSlowDisk`: `histogram_quantile(0.99, rate(etcd_disk_wal_fsync_duration_seconds_bucket{job="kube-etcd"}[10m])) > 0.010`
    for 1h, per instance;
  - `EtcdLeaderChanges`: `increase(etcd_server_leader_changes_seen_total{job="kube-etcd"}[1h]) > 3`.
- **Production:**
  - no test accounts or clients;
  - Renovate's key withheld until 12b-3;
  - kured held during the hardware proofs;
  - the cut before `lab backup enable`.
- **Never:**
  - `just dev` beside the testbed, or with C: under 30 GB;
  - commit to `dev` or `master`;
  - lower a cluster's stage.
- **Commits** end with the session's `Co-Authored-By` and `Claude-Session` lines.

## Review Focus

1. **An existing cluster with apps (the sandbox) loses no Application when the selectors gain `homelab/stage`.**
   - Expect the bootstrap to apply the labelled Secret before the ApplicationSets.
   - Task 1 proves the order offline; Task 4's `just dev` proves it on the sandbox.
2. **A deploy with the variable unset never changes the stage.** Expect `platform` to stay `platform` across repeated
   `just infra deploy production`. Task 1 pins it.
3. **Matrix and `public-scan` stay out under `platform`, and come with `all`.** Expect it in every environment's restated
   generators. Task 1 renders each.
4. **The cut's scratch data is gone afterwards, even when the spec fails midway.** Expect deferred cleanup. Task 3's
   spec owns it.
5. **kured cannot reboot a node during a hardware proof, and is released after.** Task 3's specs take and release its
   lock.

---

### Task 1: The stage switch

**Files:**
- Create: `infra/_modules/argocd/resolve-stage.sh`.
  - Usage: `resolve-stage.sh <requested> <current>`, either argument possibly empty.
  - It prints the stage or exits 1 with "the stage only moves forward: this cluster runs `all`".
- Modify: `infra/_modules/argocd/cluster-secret.sh`: env `STAGE` (required, `platform` or `all`), written as the label
  `homelab/stage`.
- Modify: `infra/_modules/argocd/main.tf`:
  - a variable `stage` (string, default `""`);
  - before the Secret is applied, the current label is read with `kubectl` (empty when the Secret does not exist);
  - `resolve-stage.sh` gives `STAGE`.
- Modify: `infra/production/argocd/terragrunt.hcl`: `stage = get_env("HOMELAB_PRODUCTION_STAGE", "")`.
- Modify: `system/argocd/values.yaml`, `values-dev.yaml`, `values-testbed.yaml`, `values-seed.yaml`.
  - `root` gets a second matrix generator: the cluster selector with `homelab/stage: all`, for directories `apps/*` and
    `system/public-scan`.
  - The first generator drops `apps/*` and excludes `system/public-scan`.
  - The `apps` ApplicationSet's cluster selector adds `homelab/stage: all`.
- Test: `test/offline/argocd_stage_test.go`.

**Interfaces:**
- Produces:
  - the label `homelab/stage`;
  - `resolve-stage.sh`;
  - the variable `HOMELAB_PRODUCTION_STAGE`.

- [ ] **Step 1: Write the failing tests.**
  - `TestResolveStage`: a table run through `sh resolve-stage.sh`:

    | requested | current | result |
    |---|---|---|
    | `""` | `""` | `all` |
    | `""` | `platform` | `platform` |
    | `""` | `all` | `all` |
    | `platform` | `""` | `platform` |
    | `all` | `platform` | `all` |
    | `platform` | `platform` | `platform` |
    | `platform` | `all` | exit 1, the message |
    | `bogus` | `""` | exit 1 |

  - `TestClusterSecretStage`: `STAGE=platform cluster-secret.sh` (with `REPOSITORY` and `BRANCH`) yields the label
    `homelab/stage: platform`; with `STAGE` unset it fails.
  - `TestStageGatesApps`: for each of `scripts/render-chart system/argocd` in production, dev, testbed and seed:
    - the `apps` ApplicationSet's cluster selectors all require `homelab/stage: all`;
    - `root` has exactly one generator listing `apps/*` and `system/public-scan`, and it requires `homelab/stage: all`;
    - no other `root` generator lists `apps/*`, and the others exclude `system/public-scan`.
- [ ] **Step 2: Run** `direnv exec . go -C test test -count=1 ./offline -run 'TestResolveStage|TestClusterSecretStage|TestStageGatesApps'`.
  Expected: FAIL.
- [ ] **Step 3: Implement** the files above.
- [ ] **Step 4: Run** the same tests, then `scripts/check-env-config`, `just infra check` and `shellcheck` on both
  scripts. Expected: PASS.
- [ ] **Step 5: Commit** `feat(argocd): a stage on the cluster Secret gates the apps on a new cluster`.

### Task 2: The etcd alerts

**Files:**
- Create: `system/monitoring-system/templates/etcd-rule.yaml`: a PrometheusRule with the two alerts of Global
  Constraints. Annotations name the disk finding and CLAUDE.md's options.
- Test: `test/offline/etcd_rule_test.go`.

- [ ] **Step 1: Write `TestEtcdRules`.** It renders `system/monitoring-system` (production) and asserts the rule names,
  expressions, `for: 1h` on `EtcdSlowDisk` and severity `warning`. If `promtool` is in the dev shell, it also runs
  `promtool check rules` on the extracted groups.
- [ ] **Step 2: Run** it. Expected: FAIL (no rule).
- [ ] **Step 3: Implement** the template.
- [ ] **Step 4: Run** it, plus `scripts/check-mesh-policies` and the render check (`lab test charts` for the component).
  Expected: PASS.
- [ ] **Step 5: Commit** `feat(monitoring): alert on etcd's slow disk and leader changes (12b-1's finding)`.

### Task 3: The production specs for the platform

**Files:**
- Modify: `test/e2e/testbed_test.go`: `Describe("TestbedCeph")` takes the label `production`.
- Create, in `test/e2e/production_test.go`:
  - `holdKured(ctx) (release func(context.Context))`: takes kured's lock by annotating its DaemonSet in `kured` with
    `weave.works/kured-node-lock` (kured's own lock), and releases it in a `DeferCleanup`. `ProductionVIPMoves` and
    `ProductionPowerCut` call it too.
  - `Describe("ProductionPowerCutWithData", Label("production", "hardware-proof"), Serial)`. Before the cut, it writes and
    records:
    - a random marker at OpenBao's `secret/scratch/power-cut`, written with the admin role the sandbox's specs use;
    - a commit pushed to a scratch repository `ops/power-cut-<random>` in Forgejo, through the API with the admin token
      the Forgejo specs read;
    - a file on a scratch RWO Ceph volume in a scratch namespace, written with `sync`.

    It then asks for the cut as `ProductionPowerCut` does, and waits for every node Ready and every pod in `rook-ceph`,
    `openbao`, `forgejo` and `kube-system` ready. It reads the three back, then deletes them in `DeferCleanup`s
    registered right after each write.
- Test: dry runs.

- [ ] **Step 1: Write the failing check.** `TestProductionDefaultFilter` (12b-1) is extended:
  - `ProductionPowerCutWithData` is only in the `hardware-proof` selection;
  - `TestbedCeph` is in the routine production selection.
- [ ] **Step 2: Run** it. Expected: FAIL.
- [ ] **Step 3: Implement.** Reuse the sandbox's `NodePowerCut` helpers for OpenBao and Forgejo where they exist
  (`test/e2e/node_restart_test.go`). `go vet ./e2e`.
- [ ] **Step 4: Run** the check, `TestE2EKeepsSelection` and `just cli check`. Expected: PASS.
- [ ] **Step 5: Commit** `test(12b-2): the cut with data, Ceph on production, kured held during hardware proofs`.

### Task 4: Sandbox proof and the release

- [ ] **Step 1:** `scripts/ci-check all`, then `just dev`, from the main checkout with the testbed stopped. Expected:
  PASS, and the sandbox's Applications all present after the bootstrap re-applies.
- [ ] **Step 2:** Final whole-branch review (fresh reviewer, the most capable model), then one fix pass.
- [ ] **Step 3:** PR into `dev`. The person approves the merge.
- [ ] **Step 4:** The release.
  - Open the `dev` → `master` PR; its `sandbox` check runs a fresh sandbox in CI.
  - Meanwhile, with the sandbox stopped, run `just testbed` for the mark: `system/argocd` is a gated path.
  - The person approves the merge.

### Task 5: The first production deploy, stage `platform` (by hand, with the person)

- [ ] **Step 1: The person's checklist** (spec decision 3):
  - Cloudflare's state on R2 is checked and its local copies deleted;
  - Cloudflare's tokens are rotated;
  - `cf auth login`;
  - the state passphrase;
  - the production SSH key in the agent;
  - Telegram's bot, optional;
  - no Renovate key.
- [ ] **Step 2:** From `master`: `HOMELAB_PRODUCTION_STAGE=platform just infra deploy production`. Expected:
  - `verify` passes (the testbed's mark);
  - apply, Forgejo `sync` and `push`, the bootstrap;
  - Argo CD switches to `ops/homelab`.
- [ ] **Step 3:** Wait for every Application Synced and Healthy. Expected: none from `apps/*`, no Matrix, no
  `public-scan`.
- [ ] **Step 4:** Proofs:
  - `--labels proof` (with `TestbedCeph`);
  - the platform's specs that need no test account (Argo CD, mesh, OpenBao, logs, monitoring, Smoke for the platform's
    pages);
  - the etcd rules loaded in Prometheus.
- [ ] **Step 5:** The person runs `scripts/onboard-user` for themselves and signs in to Grafana, Argo CD and Forgejo.
- [ ] **Step 6:** `--labels hardware-proof --focus ProductionPowerCutWithData --verbose`, with the person at the power
  strip. Expected: the data intact, and recovery measured.
- [ ] **Step 7:** `lab backup enable`; a Velero backup to R2; a restore of one platform volume. Expected: restored.
- [ ] **Step 8:** Rerun `just infra deploy production` with the variable unset. Expected: the stage stays `platform`.

### Task 6: Docs

**Files:**
- `docs/installation/production/deployment.md`: the stage, the first deploy's checklist, and the measurements.
- `CLAUDE.md`:
  - 12b-2 done;
  - the before-production items it settles, each checked against the list;
  - what stays for 12b-3.
- `docs/superpowers/deferred-minors.md`.

- [ ] **Step 1:** Write them. `mkdocs build --strict`, `scripts/ci-check all`. Expected: PASS.
- [ ] **Step 2: Commit** `docs(12b-2): production's platform`. Its PR follows Task 4's path; a docs-only change needs
  no new release before 12b-3.
