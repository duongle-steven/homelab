# istio-cni Finds the Kubelet on the Nodes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** On every NixOS node, istio-cni finds a process named `k3s` owned by root, so it runs in native nftables mode. Upstream Istio gets the issue and the pull request that make this stopgap unnecessary.

**Architecture:**
- A hardened systemd unit in the node flake (`profiles/k3s-server.nix`) runs bash through a store symlink named `k3s`, so its `comm` is `k3s`. It runs as root with no capabilities and no network.
- An offline evaluation test pins the unit.
- A Ginkgo spec on the nodes proves istio-cni and ztunnel are Ready everywhere and the named process is there.
- The Istio texts are drafts for the user to review. Nothing is sent from this plan.

**Tech Stack:** NixOS (systemd units, `pkgs.runCommand`), Go tests (`nix eval`, Ginkgo).

**Spec:** `docs/superpowers/specs/2026-11-25-istio-cni-k3s-name-design.md`

## Global Constraints

- `global.nativeNftables` stays `true` in `system/istio-system/values.yaml`, for every environment.
- The process's `comm` is exactly `k3s`, its UID is 0, its capability set is empty, and it has no network.
- The unit's comment names the upstream issue and its removal condition: an Istio release whose kubelet lookup matches `k3s-server`.
- Run the testbed only from the main checkout `/home/nixos/homelab`, on a `measure/*` branch. Start the VMs two minutes apart, `homelab-1` first, and wait for `readyz` ten times in a row before `just testbed`. Never run it beside the sandbox.
- Nothing is posted to GitHub's istio/istio: the drafts go to the user.

## Review Focus

1. **The loop `exec`s its child** (bash's last-command `exec` optimisation): the process would be renamed `sleep` and istio-cni would crash-loop again. Expected: the loop keeps bash alive. Pinned in Task 2 (the spec reads `comm` on the node).
2. **istio-cni starting before the unit**, on a boot: the process must exist first. Expected: the unit is ordered before `k3s.service`, and istio-cni retries. Pinned in Task 1 (`before`) and Task 2 (after `lab cluster upgrade`'s reboot-free switch and the VMs' cold start).
3. **The unit failing or exiting**: istio-cni would crash at its next restart. Expected: `Restart=always`. Pinned in Task 1.
4. **A node where k3s runs rootless**: the UID would be wrong. Expected: the unit's comment says the stopgap holds only for rootful k3s. Covered by the comment, not testable here.
5. **Production on the seed**: `master` does not carry native nftables yet. Expected: this fix reaches `master` together with it. Covered by merging into `dev` before the next release.

---

### Task 1: The unit on every node

**Files:**
- Modify: `infra/_modules/metal/flake/profiles/k3s-server.nix` (a new `systemd.services.k3s-name-for-istio`)
- Test: `test/offline/metal_test.go`

**Interfaces:**
- Produces:
  - unit `k3s-name-for-istio.service`;
  - an executable at `<store path>/bin/k3s`, a symlink to bash;
  - `ExecStart` running `k3s -c 'while :; do sleep 86400; done'`. `sleep` comes from coreutils by absolute path, and the loop never `exec`s.

- [ ] **Step 1: Write the failing test.** Extend `metalNode` and the `apply` expression with
  `k3sName = let s = c.config.systemd.services.k3s-name-for-istio or null; in if s == null then {} else { exec = s.serviceConfig.ExecStart; caps = s.serviceConfig.CapabilityBoundingSet; network = s.serviceConfig.PrivateNetwork; restart = s.serviceConfig.Restart; before = s.before; wantedBy = s.wantedBy; };`. Then:

```go
// istio-cni (native nftables) finds the kubelet by the comm of the process hosting it, and Istio 1.32 matches only
// "k3s", while host-installed k3s runs as "k3s-server": a process named k3s, owned by root like k3s, with nothing else
// (docs/superpowers/specs/2026-11-25-istio-cni-k3s-name-design.md)
func TestMetalK3sNameForIstio(t *testing.T) {
	for name, n := range metalEval(t) {
		require.NotEmpty(t, n.K3sName.Exec, name)
		assert.Regexp(t, `^\S+/bin/k3s -c `, n.K3sName.Exec, "%s: the executable is named k3s", name)
		assert.Contains(t, n.K3sName.Exec, "while :; do", "%s: a loop, which bash never execs away", name)
		assert.Equal(t, "", n.K3sName.Caps, name)
		assert.Equal(t, true, n.K3sName.Network, name)
		assert.Equal(t, "always", n.K3sName.Restart, name)
		assert.Contains(t, n.K3sName.Before, "k3s.service", name)
		assert.Contains(t, n.K3sName.WantedBy, "multi-user.target", name)
	}
}
```

- [ ] **Step 2: Run it.** `cd test && direnv exec .. go test -count=1 ./offline -run TestMetalK3sNameForIstio`. Expected: FAIL, because `Exec` is empty.

- [ ] **Step 3: Implement the unit** in `k3s-server.nix`:
  - the executable is `pkgs.runCommand "k3s-name-for-istio" { } "mkdir -p $out/bin && ln -s ${pkgs.bash}/bin/bash $out/bin/k3s"`;
  - `ExecStart` is that `bin/k3s -c 'while :; do ${pkgs.coreutils}/bin/sleep 86400; done'`;
  - `serviceConfig` sets `CapabilityBoundingSet = ""`, `PrivateNetwork = true`, `ProtectSystem = "strict"`, `ProtectHome = true`, `PrivateTmp = true`, `NoNewPrivileges = true`, `MemoryMax = "16M"` and `Restart = "always"`;
  - `before = [ "k3s.service" ]` and `wantedBy = [ "multi-user.target" ]`;
  - the comment names istio/istio's issue (its number filled in once sent), that the stopgap holds only for rootful k3s, and when to remove it.

- [ ] **Step 4: Run it and the metal set.** `cd test && direnv exec .. go test -count=1 ./offline -run 'TestMetal|TestProductionConfig'`. Expected: PASS.

- [ ] **Step 5: Commit** `feat(metal): a process named k3s so istio-cni finds the kubelet`.

### Task 2: Proven on the nodes, and the notes

**Files:**
- Create: `test/e2e/node_mesh_agents_test.go`
- Modify: `CLAUDE.md` (the mesh's Sandbox note on native nftables; "Before the first production deploy")

**Interfaces:**
- Consumes: Task 1's unit; `metalHosts()`, `testbedSSH`, `kube.Kubectl` (`test/e2e/testbed_test.go`).

- [ ] **Step 1: Write the spec `NodeMeshAgents`** (`Label("testbed", "production", "proof")`):
  - on every node, a process whose `comm` is exactly `k3s` has UID 0. Read it over `testbedSSH`: `for p in /proc/[0-9]*; do [ "$(cat $p/comm 2>/dev/null)" = k3s ] && awk '/^Uid/{print $2}' $p/status; done`. The output contains `0`.
  - the DaemonSets `istio-system/istio-cni-node` and `istio-system/ztunnel` have `numberReady` equal to `desiredNumberScheduled`, which equals the number of nodes. Wait up to 15 minutes.

- [ ] **Step 2: Vet and check the scope.** `cd test && direnv exec .. go vet ./e2e && cd ../cli && direnv exec .. go run ./cmd/lab test scope --check`. Expected: both pass.

- [ ] **Step 3: Write CLAUDE.md.**
  - Next to the native nftables sentence: the process named `k3s` on the nodes (Istio 1.32 matches only `k3s`; host k3s is `k3s-server`), and its removal condition.
  - Under "Before the first production deploy": measure the per-pod add latency and the meshed pods per node on the hardware with the power cut with data. Native nftables and this unit ran on the testbed only.

- [ ] **Step 4: Run it on the testbed.**
  - From the main checkout, `git checkout -B measure/istio-cni <this branch>`.
  - Start the VMs staggered and wait for `readyz` (Global Constraints).
  - Run `just testbed`.
  - Expected: every node upgraded; `NodeMeshAgents`, `TestbedMesh` and the other `proof` specs pass; the mark is written. The SSH key must be loaded: if `scripts/ssh-key-loaded` stops the run, the user loads it.

- [ ] **Step 5: Commit** `test(metal): NodeMeshAgents proves istio-cni and ztunnel on every node`, then run `scripts/ci-check all` (expected: exit 0).

### Task 3: The upstream drafts

**Files:**
- Create: `<scratchpad>/istio-issue.md` and `<scratchpad>/istio-pr.md`. These are not in the repository.

- [ ] **Step 1: Write the issue.**
  - Title: istio-cni (native nftables) cannot find the kubelet on host-installed k3s: the process is `k3s-server`.
  - Body:
    - the error;
    - versions: Istio 1.32.0, k3s v1.37.1+k3s1, NixOS;
    - the cause, with links to `kubeletuid_linux.go` and k3s `cmd/k3s/main.go`;
    - the measured table: the official binary is `k3s-server`, the `rancher/k3s` image is `k3s`;
    - the stopgap that proved it, and why it is no fix (rootless k3s);
    - links to #59187, #61078 and #61079.
- [ ] **Step 2: Write the PR description and patch.**
  - Add `{"k3s-server", false}` and `{"k3s-agent", false}` to `kubeletProcesses`.
  - Add the rows to the table test `kubeletuid_linux_test.go` for both names, plus an unrelated name `k3s-killall` that must not match.
  - Note the `k3s` prefix alternative for the maintainers.
- [ ] **Step 3: Show both to the user.** They send them, or say to, and sign Istio's CLA for the PR. Then fill the issue's number into Task 1's comment and CLAUDE.md, in its own commit.
