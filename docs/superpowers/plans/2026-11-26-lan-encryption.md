# What Crosses the LAN, Encrypted (G12) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The nodes carry pod traffic through flannel's `wireguard-native` instead of VXLAN. Every flow that crosses the LAN is measured and written down before and after. Production is reinstalled with the new backend once the cost on its hardware is measured.

**Architecture:**
- **Node flake.** One k3s flag in `infra/_modules/metal`'s server profile. The firewall takes UDP 51820 from the nodes and drops 8472.
- **Upgrade guard.** `lab cluster upgrade` refuses to roll a backend change node by node.
- **Proof.** Two by-hand scripts measure what crosses (`tcpdump`) and what it costs (`fio` on Ceph). The specs check the backend on the nodes.
- **Order.**
  - The testbed's VXLAN numbers come from starting the installed testbed.
  - Its WireGuard numbers come from one fresh install.
  - Production is measured before and after its reinstall.

**Tech Stack:** NixOS (k3s module), Go (`cli/`, Ginkgo in `test/e2e`, testify in `test/offline`), POSIX sh, `tcpdump`, `fio`.

**Spec:** `docs/superpowers/specs/2026-11-26-lan-encryption-design.md`

## Global Constraints

- The k3s flag is exactly `--flannel-backend=wireguard-native`, on every server; all three nodes are servers.
- UDP 51820 is accepted only from the nodes' addresses, one rule per node, like G2's rules. No rule names 8472.
- The sandbox (k3d, `infra/_modules/k3d`) keeps k3s's default VXLAN.
- Nothing goes in OpenBao or the rotation catalog for WireGuard's keys: flannel makes them at each boot (`/run/flannel/wgkey`). Confirm it on the testbed in Task 5. If it is false, stop and report.
- Only by-hand scripts produce the capture and the cost; their raw output never enters the repository.
- **Stop and ask the user:**
  - if production's throughput between nodes (`scripts/node-iperf`) drops by more than half;
  - if the testbed misses its bar with WireGuard (give the numbers next to the current runs);
  - before powering off or wiping production.
- The testbed's bar is #113's: the suite passes, then 30 minutes with no k3s restart once settled.

## Review Focus

1. **Running a `just infra deploy production` before the reinstall.** Expected: `lab cluster upgrade` refuses on the first enrolled node and says to reinstall. It changes no node. Pinned in Task 2's test.
2. **An unenrolled joiner during the guard.** Expected: still rebuilt without draining (#114's path). The guard looks only at enrolled nodes. Pinned in Task 2.
3. **A firewall that still opens 8472, or opens 51820 to everyone.** Expected: the offline test fails. G2's "every accept names an allowed source" still holds. Pinned in Task 1.
4. **A node whose WireGuard has fewer peers than nodes** (a node's key not yet learned). Expected: the new spec waits, then fails naming the node. Pinned in Task 3.
5. **The capture missing a flow** because it ran while nothing used it. Expected: the capture covers the whole suite, and the script prints the number of packets per flow. Pinned in Task 4's usage, and checked in Task 5.

---

### Task 1: The backend and its firewall in the node flake

**Files:**
- Modify: `infra/_modules/metal/flake/profiles/k3s-server.nix`:
  - `extraFlags` gets `--flannel-backend=wireguard-native`;
  - the per-node UDP rule moves from `--dport 8472` to `--dport 51820`;
  - the comment above it says why: WireGuard authenticates each packet with the node's key, and the spec's name.
- Modify: `test/offline/metal_test.go`:
  - `TestMetalClusterPortsFromNodes`: the per-node UDP rule names 51820, and no line names 8472;
  - new `TestMetalFlannelWireGuard`.
- Modify: `infra/_modules/k3d/k3d.yaml`: nothing. The sandbox keeps VXLAN, which `TestMetalFlannelWireGuard` asserts.

**Interfaces:**
- Produces: the flag string `--flannel-backend=wireguard-native` in `n.K3s.ExtraFlags`. Task 2 reads it from the built system.

- [ ] **Step 1: Write the failing tests.**
  - In `TestMetalClusterPortsFromNodes`, replace the 8472 assertion with `assert.Contains(t, n.Firewall.Extra, "-s "+ip+" -p udp --dport 51820 -j nixos-fw-accept", name)`, and add `assert.NotContains(t, n.Firewall.Extra, "8472", name)`.
  - Add `TestMetalFlannelWireGuard`. For every fixture node, `assert.Contains(t, n.K3s.ExtraFlags, "--flannel-backend=wireguard-native", name)`. The k3d config `infra/_modules/k3d/k3d.yaml` contains no `flannel-backend`.
- [ ] **Step 2:** Run `cd test && direnv exec .. go test -count=1 ./offline -run 'TestMetal'`. Expected: FAIL on the two new assertions.
- [ ] **Step 3:** Change `k3s-server.nix` as above.
- [ ] **Step 4:** Run the same command. Expected: PASS, G2's source check included.
- [ ] **Step 5:** Commit `feat(metal): flannel's WireGuard between the nodes, UDP 51820 from the nodes only`.

### Task 2: `lab cluster upgrade` refuses a backend change node by node

**Files:**
- Modify: `cli/internal/cluster/upgrade.go`:
  - add `backend(context.Context, host) (running, wanted string, err error)` to the `upgrader` interface;
  - call it for an enrolled node that is not current, before `healthy`.
- Modify: `cli/internal/cluster/upgrade_test.go`.

**Interfaces:**
- Consumes: Task 1's flag.
- Produces: the error text `flannel backend changes from <running> to <wanted>: k3s needs every node switched at once; reinstall the cluster (docs/installation/production/deployment.md)`.

- [ ] **Step 1: Write the failing tests.**
  - `fakeUpgrader` gains `backends map[string][2]string` (running, wanted). The default is equal: `"vxlan","vxlan"`.
  - `TestUpgradeRefusesABackendChange`: node-a is not current, with backends `{"vxlan","wireguard-native"}`. Expect the error to contain `flannel backend changes from vxlan to wireguard-native`, and the calls to be exactly `["current node-a"]`: no `healthy`, no `drain`, no `rebuild`.
  - `TestUpgradeRebuildsAnUnenrolledJoiner` still passes unchanged. The guard is not asked for an unenrolled node.
- [ ] **Step 2:** Run `cd cli && direnv exec .. go test ./internal/cluster/ -run Upgrade`. Expected: FAIL (the method is missing).
- [ ] **Step 3: Implement.**
  - In `upgrade`, after the enrolled check and before the gates, call `nodes.backend`. Return the error above when they differ.
  - `commands.backend`:
    - **running:** over ssh, `wireguard-native` when `ip link show flannel-wg` succeeds, `vxlan` when `ip link show flannel.1` succeeds, else an error.
    - **wanted:** `nix eval --raw` of the node's `config.services.k3s.extraFlags`, applying a function that returns the `--flannel-backend=` value or `vxlan`.
- [ ] **Step 4:** Run the tests. Expected: PASS. Also run `go vet ./internal/cluster`.
- [ ] **Step 5:** Commit `feat(cluster): upgrade refuses to change flannel's backend one node at a time`.

### Task 3: The specs see the backend on the nodes

**Files:**
- Modify: `test/e2e/node_ports_test.go`:
  - "accepts VXLAN only from the nodes" becomes "accepts WireGuard only from the nodes";
  - it counts the lines naming `51820`, expecting one per node with `-s <ip>/32 `;
  - it asserts no line names `8472`.
- Create: `test/e2e/node_wireguard_test.go`, with `Describe("NodeWireGuard", Label("testbed", "production", "proof"), Ordered, ContinueOnFailure, …)`. It uses `metalHosts()` and `testbedSSH()` as `NodePorts` does.

**Interfaces:**
- Consumes: Task 1's backend on the nodes.

- [ ] **Step 1: Write the spec `It("carries pod traffic through WireGuard on every node")`.**
  - For each node, `Eventually` (2 minutes, every 10 s): `wg show flannel-wg peers` over ssh has `len(names)-1` lines.
  - `ip link show flannel.1` fails: there is no VXLAN device.
  - `ip -o link show flannel-wg` shows `mtu 1420`.
  - The failure message names the node and the peer count.
- [ ] **Step 2:** Run `cd test && direnv exec .. go vet ./e2e`, then `go test -count=1 ./offline -run 'TestE2E'`. Expected: PASS. The specs themselves run on the testbed in Task 5.
- [ ] **Step 3:** Commit `test(metal): NodeWireGuard and NodePorts see flannel's WireGuard on the nodes`.

### Task 4: The two measuring scripts

**Files:**
- Create: `scripts/lan-flows <env> <seconds>`.
  - Over ssh on every node of `infra/<env>/metal/hosts.json`, it runs `tcpdump -nn -i <uplink> -w -` for that long. The uplink is the interface holding the node's address, as `NodePorts` finds it.
  - It excludes ssh to and from the workstation, and stops each capture with `-G`/`timeout`.
  - It prints one line per flow `<src> <dst> <proto>/<dport> <packets>`, merged across nodes and sorted.
  - "Flow" means the server port: the lower of the two ports when both are ephemeral is good enough.
- Create: `scripts/ceph-fio <env>`.
  - It applies a namespace `ceph-fio` and a 2Gi PVC on the cluster's default Ceph RBD class.
  - It runs a Job with `fio`, the image pinned by version and digest. The Job writes, then reads, 1 GiB in 1 MiB blocks, then 4 KiB random read/write for 60 s, all `--direct=1`.
  - It samples every node's CPU busy (`/proc/stat` over ssh) every 10 s during the Job.
  - It prints the fio throughput and IOPS and the average CPU per node, then deletes the namespace and waits until it is gone, so a `just testbed` right after it does not stop on a terminating namespace (the race #114 deferred for `NodePorts`).
- Modify: `scripts/lint` passes on both (shellcheck). Add each script's usage line to the docs in Task 5.

- [ ] **Step 1:** Write both scripts. Run `direnv exec . scripts/lint`. Expected: no findings.
- [ ] **Step 2:** Commit `feat(scripts): lan-flows and ceph-fio, by-hand measurements for G12`.

### Task 5: The testbed, before and after

**Files:**
- Modify: `docs/reference/architecture/networking.md`: a section "What crosses the LAN" with the flow table. Each flow: endpoints, port, encrypted by what or accepted with its reason.
- Modify: `docs/installation/testbed.md`: the cost table (VXLAN against WireGuard), with the scenario.
- Modify: `docs/superpowers/specs/2026-11-14-threat-model-design.md`: G12's result.

- [ ] **Step 1: The VXLAN numbers.** In the main checkout, on branch `measure/g12` at `origin/dev`:
  - start the installed testbed: `just testbed`;
  - while the suite runs, run `scripts/lan-flows testbed 1800`;
  - after it, run `scripts/ceph-fio testbed`;
  - save both outputs in the session's scratchpad.
- [ ] **Step 2: The WireGuard numbers.**
  - On this branch: `just testbed-fresh` with the measuring sampler alongside, as in #113. Capture with `scripts/lan-flows testbed 1800` during its suite, then run `scripts/ceph-fio testbed`.
  - Confirm `/run/flannel/wgkey` exists on a node, and that the Node annotation `flannel.alpha.coreos.com/backend-data` holds the public key.
  - Expected:
    - the suite passes;
    - the bar holds;
    - pod traffic between nodes shows only `udp/51820`.
    - Anything else in clear goes in the flow table with its reason.
  - If the bar fails, stop and report (Global Constraints).
- [ ] **Step 3:** Write the three documents above and commit `docs(g12): what crosses the LAN, and WireGuard's cost on the testbed`.

### Task 6: Production, measured and reinstalled

Runs after Tasks 1–5's pull request is merged into `dev` (the user approves the merge), from that commit.

**Files:**
- Modify: `docs/installation/production/deployment.md`:
  - a section "Reinstalling every node" for this once-in-a-cluster's-life change, built on the existing "To reinstall a node" steps: all three nodes, every `machine_id_hash` removed, `init_host` first;
  - the cost numbers on the hardware.
- Modify: `CLAUDE.md`: "Before the first production deploy" gets the backend's proof on the hardware.

- [ ] **Step 1: Ask the user to power production on, and confirm.** `kubectl --kubeconfig infra/production/kubeconfig.yaml get nodes` shows three Ready nodes, and Ceph is `HEALTH_OK`. Production ran no Ceph on 2026-11-26: the user chose `scripts/node-iperf` as the gate.
- [ ] **Step 2:** Run `scripts/node-iperf production` and `scripts/lan-flows production 600` on the VXLAN cluster. Save the outputs.
- [ ] **Step 3: Ask the user before the reinstall.** Then follow the new section to reinstall all three from the merged commit, using `just infra deploy production --force`. It applies only: the release gate is for releases.
- [ ] **Step 4:** Run `scripts/node-iperf production`. If the throughput is less than half the VXLAN number, stop and ask (Global Constraints). Then run `scripts/lan-flows production 600`.
- [ ] **Step 5:** Run `just test e2e --env production --labels proof`. Expected: PASS, `NodePorts` and `NodeWireGuard` included.
- [ ] **Step 6:** Write the numbers and the proof into the docs above. Commit `docs(g12): production reinstalled on WireGuard, its cost on the hardware`.
