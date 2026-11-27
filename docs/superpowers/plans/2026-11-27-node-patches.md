# G11: the nodes' packages kept current, and their store checked — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Renovate bumps the node flake's lock in its own weekly pull request, every node publishes how old its nixpkgs is and whether its Nix store verified, and production alerts at 14 days or on a failed or missing check.

**Architecture:**
- Renovate's config only, plus one local run of its image to prove it updates `flake.lock`.
- The nodes write metric files into `/var/lib/node-exporter`:
  - `nixpkgs.prom` at activation, from the flake inputs' `lastModified`;
  - `nix-store-verify.prom` from a weekly systemd service.
- node-exporter's textfile collector reads that directory, and one `PrometheusRule` `node-patches` alerts in production only.

**Tech Stack:** Renovate 44 (`renovate.json5`), NixOS modules (`infra/_modules/metal/flake`), kube-prometheus-stack 92.3.0 (`system/monitoring-system`), Go tests (`test/offline` with testify, `test/e2e` with Ginkgo).

**Spec:** `docs/superpowers/specs/2026-11-27-node-patches-design.md`

## Global Constraints

- The age alert fires at **14 days** (`14 * 86400`) after the locked nixpkgs' `lastModified`, `for: 1h`, `severity: warning`.
- `NodeStoreVerifyMissing` fires when there has been no run for **9 days**.
- The store check runs `nix-store --verify --check-contents`, at `IOSchedulingClass=idle` and `Nice=19`, weekly and staggered per node.
- The three alerts are on in production only: `values-local.yaml` turns them off (the sandbox and the testbed).
- Renovate's node lock pull request has its own branch, into `dev`, on Mondays like the rest, and **no `sandbox` label**.
- No `system.autoUpgrade`. Releases stay by hand (`just infra deploy production`).
- Write the repository's style: comments say why, lines within 120 columns.

## Review Focus

1. A node whose metric file is missing (activation never wrote it, or the directory was wiped) must alert, not stay quiet.
   - Task 4's rule test asserts the `unless on(nodename)` branch for both metrics.
2. A store check interrupted mid-run (a reboot, a power cut) must not leave a half-written `.prom` that reads as a success.
   - Task 3's script writes to a temporary file and renames it; the script test asserts no temporary file is left, and that a failed run writes `0`.
3. `nix-store --verify` finding a modified path must count as a failure even if it exits 0 on some Nix version.
   - Task 3's test feeds a fake `nix-store` that prints `was modified!` and exits 0, and expects `success 0`.
4. Helm replaces lists, so node-exporter's `extraArgs` restated with the textfile flag must keep the chart's defaults.
   - Task 4's render test asserts the chart's default `--collector.filesystem.mount-points-exclude` flag is still there.
5. Three nodes verifying at once after a power cut (`Persistent=true` catching up) would load every disk together.
   - Task 3's timer adds `RandomizedDelaySec=1h`, and the metal test asserts each node has a different `OnCalendar` hour.

---

### Task 1: Renovate gives the node flake's lock its own pull request

**Files:**
- Modify: `renovate.json5` (a `packageRules` entry).
- Modify: `test/offline/renovate_test.go` (`TestRenovateManagers`).
- Modify: `docs/how-to-guides/renovate.md` (the node pull request, what merging it means).

**Interfaces:**
- Produces: a lock maintenance branch for `infra/_modules/metal/flake/flake.lock`, apart from the other lock files'.

- [ ] **Step 1: Write the failing assertion.**
  - In `TestRenovateManagers`, assert the nix manager found `infra/_modules/metal/flake/flake.nix`. The package file key may be `infra/_modules/metal/flake/flake.nix` or `infra/_modules/metal/flake/flake.lock`: use whichever the extract reports.
  - Add `TestRenovateNodeLockApart`. It reads `renovate.json5` with `json5` (as the file's other test does, or `yq -p json5`), and asserts a `packageRules` entry with:
    - `matchFileNames` containing `infra/_modules/metal/flake/**`;
    - `matchUpdateTypes` equal to `["lockFileMaintenance"]`;
    - a `branchTopic` `lock-file-maintenance-nodes`;
    - `addLabels` absent, and `labels: []` so the global `sandbox` label is dropped.
- [ ] **Step 2:** Run `cd test && direnv exec .. go test ./offline -run 'TestRenovate' -count=1`. Expected: FAIL on the new rule.
- [ ] **Step 3: Add the rule.**
  - Its comment: why it is apart (a node bump that breaks holds nothing else back), why no `sandbox` label (the sandbox runs no node), and that merging it means a release, whose `lab cluster upgrade` rolls it.
  - If Renovate keeps one lock maintenance branch however the rule is written, a `nix` manager entry scoped by `managerFilePatterns` to the node flake with its own `lockFileMaintenance` is the fallback. Step 5 shows which.
- [ ] **Step 4:** Run the tests again. Expected: PASS. Also run `scripts/lint` (it validates `renovate.json5`).
- [ ] **Step 5: Prove it with Renovate's own image, once.**
  - The image is production's: `ghcr.io/renovatebot/renovate:44.172.0`.
  - Run it against a local clone of this branch with `--platform=local`, `RENOVATE_DRY_RUN=full`, `LOG_LEVEL=debug`.
  - The log must show a branch for the node lock apart from the root lock's.
  - The log must show `nix flake update` (or the lock's new revisions) for `infra/_modules/metal/flake`: this proves the image has, or installs, Nix.
  - Save the log in the session's scratchpad. Write the finding (a branch of its own, Nix installed or not) into `renovate.md`.
  - If the image cannot run Nix, stop and report to the user before going on: the spec's choice of Renovate depends on it.
- [ ] **Step 6: Commit.** Message: `feat(renovate): the nodes' flake lock in a pull request of its own`.

### Task 2: Each node publishes the age of its nixpkgs

**Files:**
- Modify: `infra/_modules/metal/flake/default.nix`:
  - `specialArgs` gains `nixpkgsLastModified = { nixpkgs = nixpkgs.lastModified; nixpkgs-unstable = nixpkgs-unstable.lastModified; }`;
  - it also gains `nodeIndex`, the host's place in `builtins.attrNames hosts` (sorted), for Task 3.
- Create: `infra/_modules/metal/flake/profiles/node-patches.nix`, added to the modules list.
- Modify: `test/offline/metal_test.go`: the `apply` expression and `metalNode` gain `nodeMetrics`.

**Interfaces:**
- Produces: a file `/var/lib/node-exporter/nixpkgs.prom`, with this metric:
  - `node_nixpkgs_last_modified_seconds{input="nixpkgs"} <int>`;
  - `node_nixpkgs_last_modified_seconds{input="nixpkgs-unstable"} <int>`;
  - with `# HELP` and `# TYPE … gauge` lines.
- Produces: `nodeIndex` (int, 0-based) in `specialArgs`.

- [ ] **Step 1: Write the failing test `TestMetalNixpkgsAge`.**
  - For each fixture node, the activation script named `node-exporter-nixpkgs` exists.
  - The metric file's text it installs contains both lines.
  - Their values equal `lastModified` of `flake.lock`'s `nixpkgs_2` and `nixpkgs-unstable` nodes, read from the copied `flake.lock` with `jq`.
  - Expose the text through `apply` by reading the store path the script names: `builtins.readFile` of the file `pkgs.writeText` made. Exposing the text as an option attribute instead is also fine.
- [ ] **Step 2:** Run `cd test && direnv exec .. go test ./offline -run 'TestMetal' -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement `profiles/node-patches.nix`.**
  - `system.activationScripts.node-exporter-nixpkgs` installs the `writeText` file to `/var/lib/node-exporter/nixpkgs.prom`, mode 0644, with `install -D`.
  - It runs at every switch and boot, so a rebuild updates it.
  - The file is copied, not symlinked: node-exporter reads the host's root under `/host/root`, where a link into `/nix/store` would resolve inside its own container.
- [ ] **Step 4:** Run the tests again. Expected: PASS.
- [ ] **Step 5: Commit.** Message: `feat(metal): each node publishes the age of the nixpkgs it runs`.

### Task 3: A weekly check of each node's Nix store

**Files:**
- Create: `infra/_modules/metal/flake/profiles/nix-store-verify.sh`.
- Modify: `profiles/node-patches.nix`: the service and the timer.
- Modify: `test/offline/metal_test.go` (`nodeMetrics` gains the unit's settings).
- Create: `test/offline/nix_store_verify_test.go`.

**Interfaces:**
- Consumes: `nodeIndex` (Task 2).
- Produces: a file `/var/lib/node-exporter/nix-store-verify.prom`, with these metrics:
  - `node_nix_store_verify_success` (1 or 0);
  - `node_nix_store_verify_last_run_seconds` (the run's end, Unix seconds).
- The script takes the output directory as `$1` (default `/var/lib/node-exporter`), so tests can point it elsewhere.

- [ ] **Step 1: Write the failing script tests** (with a fake `nix-store` on `PATH`, as `cluster_startup_test.go` fakes `kubectl`).

  | Test | Fake `nix-store` | Expected file |
  |---|---|---|
  | `TestNixStoreVerifyClean` | prints nothing, exits 0 | `success 1`, a last run within 5 s of now |
  | `TestNixStoreVerifyFails` | exits 1 | `success 0` |
  | `TestNixStoreVerifyModified` | prints `path '/nix/store/x' was modified! expected hash 'a', got 'b'`, exits 0 | `success 0` |
  | `TestNixStoreVerifyAtomic` | (after any run) | only the `.prom` file in the directory, no temporary file |

  - The script's own exit status mirrors success, so systemd shows the failure too.
- [ ] **Step 2:** Run `go test ./offline -run 'TestNixStoreVerify' -count=1`. Expected: FAIL (no script).
- [ ] **Step 3: Write the script.**
  - It runs `nix-store --verify --check-contents 2>&1 | tee`, so the journal keeps the output.
  - Failure is a non-zero exit, or any line matching `was modified!`.
  - It writes to `mktemp` in the same directory, then `mv`.
- [ ] **Step 4: Add the systemd units.**
  - `systemd.services.nix-store-verify`:
    - `Type=oneshot`, `IOSchedulingClass=idle`, `Nice=19`;
    - `ExecStart` runs the script built with `writeShellApplication` (`runtimeInputs`: `nix`, `coreutils`, `gnugrep`).
  - `systemd.timers.nix-store-verify`:
    - `OnCalendar = "Sun *-*-* ${hour}:30:00"`, where `hour = 3 + 2 * nodeIndex` (03:30, 05:30, 07:30 on the nodes' time zone);
    - `Persistent=true`, `RandomizedDelaySec=1h`.
  - Extend `TestMetalNixpkgsAge`'s sibling `TestMetalStoreVerify` to assert:
    - the service's `IOSchedulingClass` and `Nice`;
    - the timer is wanted by `timers.target`;
    - the three `OnCalendar` values are pairwise different, and `Persistent` and `RandomizedDelaySec` are set.
- [ ] **Step 5:** Run `go test ./offline -run 'TestNixStoreVerify|TestMetal' -count=1`. Expected: PASS. Then run `scripts/lint` (shellcheck).
- [ ] **Step 6: Commit.** Message: `feat(metal): a weekly check of each node's Nix store, its result for node-exporter`.

### Task 4: node-exporter reads the files; the alerts, in production

**Files:**
- Modify: `system/monitoring-system/values.yaml`:
  - `kube-prometheus-stack.prometheus-node-exporter.extraArgs`: the chart's defaults restated, plus `--collector.textfile.directory=/host/root/var/lib/node-exporter`;
  - a top-level `nodeAlerts: true`, with a comment.
- Modify: `system/monitoring-system/values-local.yaml`: `nodeAlerts: false`, with the reason (the k3d node is no NixOS node; the testbed has no monitoring).
- Create: `system/monitoring-system/templates/node-patches-rule.yaml` (`{{- if .Values.nodeAlerts }}`).
- Create: `test/offline/node_patches_rule_test.go`.
- Modify: `docs/how-to-guides/alerts.md`: the three alerts and what to do.

**Interfaces:**
- Consumes: the metric names of Tasks 2 and 3.

- [ ] **Step 1: Write the failing tests.**
  - `TestNodeExporterTextfile` renders production (`""`) and asserts the DaemonSet's args contain:
    - `--collector.textfile.directory=/host/root/var/lib/node-exporter`;
    - the chart's own `--collector.filesystem.mount-points-exclude=` flag, so the default is kept.
  - `TestNodePatchesRules`, like `TestEtcdRules`:
    - production renders the alerts `NodePackagesStale`, `NodeStoreVerifyFailed` and `NodeStoreVerifyMissing`;
    - `dev` renders no `node-patches` rule;
    - severity `warning` for all three;
    - `for: 1h` for Stale and Missing, `for: 0m` or absent for Failed;
    - the expressions verbatim, as below.
  - `NodePackagesStale`'s description names `status.nixos.org` and Renovate's pull request.
  - `NodeStoreVerifyFailed`'s description names `journalctl -u nix-store-verify` and `nix-store --repair-path`.

  The expressions (`instance` joined to the node's name through `node_uname_info`):

  ```
  NodePackagesStale:
    time() - (node_nixpkgs_last_modified_seconds * on(instance) group_left(nodename) node_uname_info) > 14 * 86400
    or on(nodename)
    (label_replace(kube_node_info, "nodename", "$1", "node", "(.*)")
      unless on(nodename) (node_nixpkgs_last_modified_seconds * on(instance) group_left(nodename) node_uname_info))
  NodeStoreVerifyFailed:
    node_nix_store_verify_success * on(instance) group_left(nodename) node_uname_info == 0
  NodeStoreVerifyMissing:
    time() - (node_nix_store_verify_last_run_seconds * on(instance) group_left(nodename) node_uname_info) > 9 * 86400
    or on(nodename)
    (label_replace(time() - kube_node_created > 9 * 86400, "nodename", "$1", "node", "(.*)")
      unless on(nodename) (node_nix_store_verify_last_run_seconds * on(instance) group_left(nodename) node_uname_info))
  ```

  - `NodeStoreVerifyMissing` takes `for: 1h`. A new node has no run until its first Sunday, so the branch for a node
    without the file counts only nodes whose Node object is over 9 days old (`kube_node_created`: unlike the boot time,
    it survives reboots and power cuts). `NodePackagesStale`'s branch needs no such grace: activation writes its file.
- [ ] **Step 2:** Run `go test ./offline -run 'TestNodeExporterTextfile|TestNodePatchesRules' -count=1`. Expected: FAIL.
- [ ] **Step 3:** Implement the values and the template. Check the expressions with `promtool check rules` on the rendered rule, if `promtool` is in the dev shell; else the render test is the check.
- [ ] **Step 4:** Run the tests and `scripts/ci-check render` (or `lab test charts` for `system/monitoring-system`). Expected: PASS.
- [ ] **Step 5: Commit.** Message: `feat(monitoring): node-exporter reads the nodes' metric files; alerts on stale packages and the store check`.

### Task 5: Proven on the sandbox and the testbed, then the docs

**Files:**
- Create: `test/e2e/node_patches_test.go`, with two specs:
  - `Describe("NodeTextfileMetrics", Label("sandbox", "monitoring"), …)`;
  - `Describe("NodePatches", Label("testbed", "production", "proof"), …)`.
- Modify: `test/scopes.yaml`:
  - the new label, if `monitoring` does not cover it;
  - `infra/_modules/metal/flake/profiles/node-patches.nix` and `nix-store-verify.sh` in the testbed's paths, if they are not covered by a pattern.
- Modify: `test/offline/fixtures/e2e-selection.json` and `test/offline/e2e_selection_test.go` for the two new specs (`TestE2EKeepsSelection`).
- Modify: `docs/superpowers/specs/2026-11-14-threat-model-design.md`: G11's patches and integrity done; `clamd` its own step after 12b-3; the Order section's item 5 updated.
- Modify: `CLAUDE.md`:
  - the Roadmap's order: G11 done, `clamd` a step after 12b-3;
  - "Before the first production deploy": the alerts first run in production; the store check measured once on the testbed.
- Modify: `docs/installation/testbed.md`: the store check's time and load on a node, measured in Step 4.

- [ ] **Step 1: Write `NodeTextfileMetrics` (sandbox).**
  - It writes `node_homelab_textfile_probe 1` into the k3d node's `/var/lib/node-exporter/probe.prom` with `docker exec`. The node's container name comes from the existing helper that finds the k3d node.
  - Within 3 minutes, Prometheus returns that series for the node. Use the existing Prometheus query helper in `test/e2e`.
  - It deletes the file afterwards (`DeferCleanup`).
- [ ] **Step 2: Write `NodePatches` (testbed and production), over `testbedSSH` on every node.**
  - `/var/lib/node-exporter/nixpkgs.prom` holds the two `input=` lines. The `nixpkgs` value equals `flake.lock`'s `nixpkgs_2.locked.lastModified`, read from the repository.
  - `systemctl is-enabled nix-store-verify.timer` is `enabled`.
  - `systemctl show nix-store-verify -p IOSchedulingClass` is `IOSchedulingClass=3`, which is idle.
- [ ] **Step 3: Run both.**
  - Run `just dev --tests 'monitoring'` (from the main checkout).
  - Run `just testbed-fresh` on the branch: the nodes need the new profile, and a fresh install is the testbed's usual path. Then `just test e2e --env testbed --focus NodePatches`. Expected: PASS.
  - The testbed currently runs VXLAN from `dev`, so `testbed-fresh` also brings G12's WireGuard. That is expected.
- [ ] **Step 4: Run the store check once on one testbed node, by hand.**
  - Run `systemctl start nix-store-verify` on `homelab-2`, and read its `.prom`.
  - Record the time it took and the node's CPU and I/O wait (`/proc/stat` before and after, as `node-iperf` samples).
  - If it takes over 2 hours or saturates the disk, report to the user before relying on the weekly schedule.
- [ ] **Step 5:** Write the docs listed above, run `scripts/ci-check all` in the dev shell (with the testbed stopped: its VMs push the offline tests past 20 minutes), and commit `docs(g11): the nodes' patches and store check, proven; clamd after 12b-3`.
