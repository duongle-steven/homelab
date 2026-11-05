# Cluster scan (step 15d) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `make testbed-scan` (kube-bench) and `make dev-scan` (kubescape) fail on any undecided finding and on a scan that did not really run; every open finding is decided; the fixes are made; Secrets in etcd are proven encrypted.

**Architecture:** Go tests in `test/` run the tools and hand their JSON to pure functions, which compare it with `.cluster-scan-accepted.yaml`. Those functions are tested offline in `scripts/ci-check`. The live tests sit outside `make dev`'s and `make testbed`'s selections, except `TestTestbedSecretsEncrypted`.

**Tech Stack:** Go (testify, terratest's `k8s` helpers as in `test/testbed_test.go`), kube-bench, kubescape, NixOS node profile, k3d config, Helm values.

**Spec:** `docs/superpowers/specs/2026-11-05-cluster-scan-design.md`

## Global Constraints

- Branch `feat/cluster-scan`, PR into `dev`; merge only when the user says "merge".
- Live runs only from the main checkout; never `make -n`; testbed and sandbox never up together; no `scripts/ci-check all` while either is up. Before each: `pgrep -x terragrunt; pgrep -x make`.
- Stop a run by hand on two samples a minute apart under 1.5 GB available; do not restart a run killed for memory without the user's go-ahead.
- Tool reports hold command lines and config: they stay in `t.TempDir()`, are never committed, and failures name only check number, control ID and `<namespace>/<Kind>/<name>`, never `actual_value`.
- kubescape always with `--keep-local`.
- Versions: latest stable pinned exactly; check upstream when building. kube-bench image v0.16.0 or later; kubescape v4.0.17 or later. If a release has `k3s-cis-1.11`, use it and vendor nothing.
- The dangerous controls, accepted only by resource: C-0185, C-0187, C-0035, C-0065, C-0191, C-0057, C-0045, C-0048, C-0041, C-0038, C-0046, C-0012, C-0259.
- A dangerous finding that cannot be fixed small is raised with the user before it is accepted.

## Review Focus

1. A report that is not JSON, empty, or from a tool that exited non-zero: the target fails, never passes. Pinned by `TestScanReportsRefused`.
2. Fewer reports than nodes (a Job failed on one node): the target fails naming the node. Pinned in `TestScanBench`, by requiring one parsed report per `testbedHosts` entry.
3. A whole-control acceptance of a dangerous control, or an entry with no reason: the file is refused. Pinned by `TestScanAcceptedRefused`.
4. A run stopped midway leaves the privileged namespace: the next run deletes it first, and `t.Cleanup` deletes it after. Pinned in Task 2, Step 4.
5. Resources with generated names (`svclb-gateway-istio-62125460`): an accepted `resource` is a `path.Match` pattern, so a rebuild does not fail the target. Pinned by `TestScanKubescapeDecided`.

---

### Task 1: The decision logic, offline

**Files:**
- Create: `test/scan_test.go`
- Create: `.cluster-scan-accepted.yaml` (empty lists)
- Modify: `scripts/ci-check:7` (add `TestScan(AcceptedRefused|ReportsRefused|BenchDecided|KubescapeDecided)|TestSecretIsEncrypted` to `offline_tests`)
- Modify: `test/scopes.yaml` (`skip`: `.cluster-scan-accepted.yaml`)

**Interfaces:**
- Produces, all in `test/scan_test.go`:
  - `type scanAccepted struct { KubeBench []scanCheck `yaml:"kubeBench"`; Kubescape struct { Controls []scanControl `yaml:"controls"`; Resources []scanResource `yaml:"resources"` } `yaml:"kubescape"` }`, with `scanCheck{Check, Reason}`, `scanControl{ID, Reason}`, `scanResource{ID, Resource, Reason}`
  - `func loadScanAccepted(raw []byte) (scanAccepted, error)`
  - `func undecidedBench(report []byte, accepted scanAccepted) ([]string, error)`: undecided FAIL check numbers
  - `func undecidedKubescape(report []byte, accepted scanAccepted) ([]string, error)`: undecided `<controlID> <namespace>/<Kind>/<name>`
  - `func secretIsEncrypted(raw []byte, value string) error`
  - `const scanMinResources = 500`

- [ ] **Step 1: Write the failing tests**

```go
func TestScanAcceptedRefused(t *testing.T)   // no reason on any entry kind; C-0185 under controls: each returns an error naming the entry
func TestScanReportsRefused(t *testing.T)    // both parsers: "", "not json", a warning line before the JSON are errors
func TestScanBenchDecided(t *testing.T)
// fixture: sections 1..5, FAIL 1.2.30 and 4.2.4, WARN 1.2.3. With 4.2.4 accepted: ["1.2.30"]; WARN never returned.
// sections 1,4,5 only: error containing "section 2".
// a result whose reason holds "journalctl: not found": error containing "1.2.1", even if PASS.
func TestScanKubescapeDecided(t *testing.T)
// results use the real shape: results[].resourceID, controls[].controlID, controls[].status.status; summaryDetails.ResourceCounters.
// C-0050 accepted as a control: no finding. C-0057 on istio-system/DaemonSet/ztunnel accepted by resource: no finding;
// on another resource: "C-0057 ns/DaemonSet/other". resource "kube-system/DaemonSet/svclb-*" matches a generated name.
// fewer than scanMinResources, or no istio-system/DaemonSet/ztunnel in the results: error.
func TestSecretIsEncrypted(t *testing.T)
// "k8s:enc:aescbc:v1:aescbckey:<bytes>" with no value: nil; plain value: error; no prefix: error; value inside an encrypted-looking blob: error
```

- [ ] **Step 2: Run them**

Run: `cd test && direnv exec .. go test -count=1 -run 'TestScan(AcceptedRefused|ReportsRefused|BenchDecided|KubescapeDecided)|TestSecretIsEncrypted' .`
Expected: FAIL, the functions are undefined.

- [ ] **Step 3: Implement**

Resource key: the last three `/`-separated fields of `resourceID`, as the spike's `ks.py` did (`/ClusterRoleBinding/velero-server` for a cluster-scoped one). Match `: not found` for the audit guard. Sort results.

- [ ] **Step 4: Run them, and the checks they touch**

Run: the command of Step 2, then `direnv exec . make -C cli check`.
Expected: PASS.

- [ ] **Step 5: Commit** `test(scan): decide a scanner's findings against one file`

### Task 2: kube-bench on the testbed

**Files:**
- Create: `test/kube-bench-cfg/` (`config.yaml` and `k3s-cis-1.11/*` from a named `main` commit, that commit and the reason in a header comment)
- Modify: `test/scan_test.go` (`TestScanBench`)
- Modify: `Makefile` (`testbed-scan`, beside `testbed-power`)
- Modify: `.yamllint` or `scripts/lint` (exclude the vendored directory, if yamllint refuses it)

**Interfaces:**
- Consumes: `undecidedBench`, `loadScanAccepted`, `testbedOnly`, `testbedHosts`, `testbedKubectl`
- Produces: `make testbed-scan`, running `$(MAKE) env=testbed ENV=testbed INSECURE_SKIP_VERIFY=1 smoke_filter=ScanBench format=standard-verbose smoke-test` after `scripts/testbed-guard testbed`

Vendoring has one trap: the benchmark directory has its own `config.yaml`. The top-level one holds `target_mapping`; check `grep -c target_mapping test/kube-bench-cfg/config.yaml` is 1.

- [ ] **Step 1: Write `TestScanBench`.** `testbedOnly`; delete namespace `scan-bench` if present, create it labelled `pod-security.kubernetes.io/enforce: privileged`, `t.Cleanup` deletes it; ConfigMap from the vendored files; one Job per node (`nodeName`, `hostPID`, `privileged`, `backoffLimit: 0`), command `kube-bench run --benchmark k3s-cis-1.11 --targets master,etcd,controlplane,node,policies --json`, `PATH` ending in `/run/current-system/sw/bin`, config mounted at `/opt/kube-bench/cfg`, read-only host mounts `/var/lib/rancher`, `/etc/rancher`, `/var/log/journal`, `/etc/machine-id`, `/etc/systemd`, `/nix`, `/run/current-system`. Require every Job complete, parse each log, and fail with `<node>: <undecided>`.
- [ ] **Step 2: Run on a testbed at this branch's commit, with nothing accepted**

Run: `make testbed-scan` (VMs up: `make testbed` first if they are down, and `make testbed-stop` after).
Expected: FAIL naming exactly 1.2.30, 1.3.7, 1.4.2, 4.2.4, 4.3.1 on each of the three nodes. Another list is a finding: read it.

- [ ] **Step 3: See both guards fail.** Temporarily drop the `/nix` mount, then `--targets`.
Expected: an error containing `not found`, then one containing `section 1`. Restore both.
- [ ] **Step 4: Check the cleanup.** Stop a run mid-Job (kill the exact `go` test PID), run again.
Expected: the second run starts clean; afterwards `kubectl get ns scan-bench` is NotFound.
- [ ] **Step 5: Accept 1.3.7, 1.4.2, 4.3.1, 4.2.4** with the spec's reasons, and rerun.
Expected: FAIL with only 1.2.30, which Task 5 fixes.
- [ ] **Step 6: Commit** `test(scan): kube-bench on the testbed's nodes`

### Task 3: kubescape on the sandbox

**Files:**
- Modify: `flake.nix` (kubescape from its release: fetch the asset with its hash, since nixpkgs has 4.0.8; the reason beside it)
- Modify: `test/scan_test.go` (`TestScanKubescape`)
- Modify: `Makefile` (`dev-scan`)
- Modify: `.cluster-scan-accepted.yaml`

**Interfaces:**
- Consumes: `undecidedKubescape`, `loadScanAccepted`
- Produces: `make dev-scan`, running `$(MAKE) env=sandbox ENV=dev INSECURE_SKIP_VERIFY=1 smoke_filter=ScanKubescape format=standard-verbose smoke-test` after `scripts/testbed-guard dev`

- [ ] **Step 1: Write `TestScanKubescape`.** Skip unless `ENV=dev`; run `kubescape scan framework allcontrols --keep-local --format json --output <TempDir>/report.json --kubeconfig $KUBECONFIG`; a non-zero exit or a missing file fails. Fail with the undecided list, one per line. Keep the tool's own exit code on findings in mind: it must not read as a run failure unless `--compliance-threshold` is set.
- [ ] **Step 2: Run with nothing accepted**

Run: `k3d cluster start homelab-dev`, wait for every pod Ready, `make dev-scan`.
Expected: FAIL, about 86 controls.

- [ ] **Step 3: Triage.** For each failed control read what it would let someone do, then: accept whole with a reason (limits, labels, probes, signatures, Kyverno's repeats), or accept by resource, or leave for Task 5. Read each C-0012 and C-0259 resource's real value in the render (do not print values) and record why it is or is not a credential; a real one in plain text is a fix, not an acceptance. Raise with the user any dangerous finding with no small fix.
- [ ] **Step 4: Rerun.**
Expected: only what Task 5 fixes remains.
- [ ] **Step 5: See it catch a new one.** Apply a privileged Pod in a temporary namespace, rerun, delete it.
Expected: FAIL with `C-0057 <ns>/Pod/<name>`.
- [ ] **Step 6: Commit** `test(scan): kubescape on the sandbox, and each finding decided`

### Task 4: Secrets encrypted in etcd, and the key's procedure

**Files:**
- Modify: `infra/_modules/metal/flake/profiles/k3s-server.nix` (`environment.systemPackages = [ pkgs.etcd ]`, for `etcdctl`)
- Modify: `test/testbed_test.go` (`TestTestbedSecretsEncrypted`)
- Modify: `cli/internal/rotate/catalog.yaml` (`k3s.encryption-key`, `k3s.token`, both `kind: manual`, no period)

**Interfaces:**
- Consumes: `secretIsEncrypted`, `testbedSSH`, `testbedKubectl`

- [ ] **Step 1: Write `TestTestbedSecretsEncrypted`.** Create a Secret in `kube-system` with a random value, delete it in cleanup, then on a node: `etcdctl --endpoints https://127.0.0.1:2379 --cacert /var/lib/rancher/k3s/server/tls/etcd/server-ca.crt --cert .../server-client.crt --key .../server-client.key get /registry/secrets/kube-system/<name> --print-value-only`. Assert `secretIsEncrypted`.
- [ ] **Step 2: Run.** Run: `make testbed-fresh`, since the node profile changed.
Expected: PASS, the flag being on. `TestSecretIsEncrypted`'s plain-value case is what proves the check can fail (the spec's cheap route).
- [ ] **Step 3: Add the catalog entries.** Reasons from the spec; `k3s.token`'s steps come from k3s's docs and say they were never run. Run `direnv exec . make -C cli test check` and `scripts/ci-check rotation-catalog`.
Expected: PASS. If `lab secrets check` wants `paths` a k3s key lacks, change the entry's kind to fit and ledger it.
- [ ] **Step 4: Rotate once.** On the testbed: `k3s secrets-encrypt rotate-keys` on `homelab-1`, wait for `reencrypt_finished` and "All hashes match", restart k3s on each node in turn, then read a Secret through the API, and run `make testbed` again.
Expected: new key active, `TestTestbedSecretsEncrypted` PASS, no Secret unreadable. Failure here stops the task: raise it, since the guide must not offer a broken procedure.
- [ ] **Step 5: Commit** `test(nodes): Secrets are encrypted in etcd, and the key has a procedure`

### Task 5: The fixes

**Files:**
- Modify: `infra/_modules/metal/flake/profiles/k3s-server.nix`, `infra/_modules/k3d/k3d.yaml` (same flags on both)
- Modify: `system/velero/values.yaml` (`rbac.clusterAdministrator: false`), Create: `system/velero/templates/rbac.yaml`
- Modify: `system/kyverno/values*.yaml` if the Job needs it
- Modify: `.cluster-scan-accepted.yaml`

- [ ] **Step 1: 1.2.30.** Add `--kube-apiserver-arg=service-account-extend-token-expiration=false` on both.
Run: `make testbed-fresh`, `make testbed-scan`, `make testbed-stop`.
Expected: 1.2.30 passes, the target passes. A client losing its token shows in `make testbed` or the later `make dev`.
- [ ] **Step 2: The warnings.** `--seccomp-default`, a pod PID limit (`--kubelet-arg=pod-max-pids=`, a value found from the sandbox's busiest pod, at least 4096) and `audit-log-maxage`. Add each as its own commit. A workload it breaks in the later `make dev` is accepted with the reason and the flag removed.
- [ ] **Step 3: Velero.** Bind what a volume backup and restore needs instead of `cluster-admin`.
Run: `make dev tests='BackupRestore'`, then `make dev-scan`.
Expected: `TestBackupRestore` PASS and C-0185 gone. After two failed attempts at a narrower role, stop and raise it with the user, with what each attempt lacked.
- [ ] **Step 4: `kyverno-migrate-resources`.** Find whether it is a leftover post-upgrade hook or a real failure from the Job's history and the chart. Fix, or accept by resource with the finding.
- [ ] **Step 5: Run.** Run: `make dev-scan`.
Expected: PASS.
- [ ] **Step 6: Commit** each fix on its own, `fix(<area>): ...`

### Task 6: Docs and the whole branch

**Files:**
- Create: `docs/how-to-guides/cluster-scan.md`; Modify: `mkdocs.yml` (nav, after `supply-chain.md`)
- Modify: `CLAUDE.md` (roadmap 15d line; before-production: LUKS's accepted risk and that adding it means reinstalling each disk, kube-bench on hardware, the benchmark not matching 1.37)
- Modify: `docs/how-to-guides/supply-chain.md` if it no longer reads true; `docs/superpowers/deferred-minors.md`

- [ ] **Step 1: Write the guide.** Running each target, reading a failure, the file's two levels, the fourteen warnings' readings with what was decided, what encryption at rest does and does not protect, the rotation's steps and result, the traps of the spike.
- [ ] **Step 2: Run the basic checks.** With testbed and sandbox stopped. Run: `direnv exec . scripts/ci-check all`.
Expected: `ci=0`.
- [ ] **Step 3: Run the whole platform.** Run: `make dev scope=full`, then `make dev-scan`, stop the sandbox; `make testbed-fresh`, `make testbed-scan`, `make testbed-power`, `make testbed-stop`.
Expected: all PASS. `TestNodePowerCut` over 20 minutes with `Bind [::1]:15053` in ztunnel is the known watchdog gap: rerun it alone and say so.
- [ ] **Step 4: Commit** `docs: scanning the cluster, and what encryption at rest protects`
- [ ] **Step 5: Final review** by a fresh reviewer on the most capable model, then the PR into `dev`.
