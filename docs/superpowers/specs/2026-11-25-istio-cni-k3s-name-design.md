# istio-cni finds the kubelet on the nodes (k3s-server)

Status: designed in chat 2026-11-24/25, a small change. *(user)* marks what was decided in chat; *(Claude's call)* marks
the builder's own choices, open to review. Found by G2's testbed run
(`docs/superpowers/specs/2026-11-24-node-ports-design.md`), which it blocks.

## Intent

Since 2026-11-14 istio-cni writes pods' redirection with native nftables (`global.nativeNftables`, Istio 1.32), the
fix for the mesh never recovering after a node restart with more than about 33 meshed pods on a node. The sandbox
proved it; the nodes never ran it. On the testbed's NixOS nodes istio-cni crash-loops on every node:

```
failed to get the kubelet uid: no kubelet process found in /host/proc
```

So no pod gets a network: Ceph's daemons, the Gateways and ztunnel stay down. Production would do the same at its first
deploy of this commit (it is not on `master` yet).

## The cause (verified 2026-11-24)

- In nftables mode istio-cni identifies the kubelet's health probes by the UID of the process that hosts the kubelet
  (`meta skuid`), since nftables has no equivalent of iptables' `-m owner --socket-exists` (Istio's own comment in
  `cni/pkg/nftables/nftables.go`). It finds that process in `/proc` by its `comm`
  (`cni/pkg/nftables/kubeletuid_linux.go`): `kubelet` or `kubelite` (with "kubelet" in its arguments), or, since
  istio/istio#61079 (released in 1.32.0 on 2026-11-09), exactly `k3s`.
- k3s installed on a host runs through its launcher, which executes `<data-dir>/bin/k3s-server` (k3s
  `cmd/k3s/main.go`), so the process is named `k3s-server`. Measured: the official k3s v1.37.1+k3s1 binary (sha256
  checked) run as `k3s server` has comm `k3s-server`; the `rancher/k3s:v1.37.1-k3s1` image that k3d runs has comm
  `k3s`. So Istio's k3s support works in k3d, which is why the sandbox never failed, and on no host install.
- Proven: a root process named `k3s` on one testbed node made that node's istio-cni Running, and it stayed so.
- No Istio release fixes it (1.32.0 is the latest; `master` still matches exactly `k3s`), and istio-cni has no setting
  to give the UID or skip the lookup.

## Decisions

- **Keep native nftables on every environment** *(user)*. Going back to iptables on the nodes was weighed and set
  aside: the testbed's power-cut recovery in iptables mode (12a-3) ran with few meshed pods per node, so it says nothing
  about production's density, and the add latency on the hardware is unmeasured.
- **A process named `k3s` on every node, until Istio is fixed** *(user)*: a systemd unit in the node flake
  (`infra/_modules/metal`) running a minimal process whose `comm` is `k3s`, as root, since istio-cni only reads the
  matching process's UID and k3s runs as root (UID 0). It is a stopgap and says so where it is defined: it is right
  only because k3s is rootful (a rootless k3s would need its own user's UID).
- **Fix it upstream** *(user)*: an issue and a pull request to istio/istio adding `k3s-server` and `k3s-agent` (exact
  names, with the existing table-driven test extended), mentioning a `k3s` prefix match as the alternative; the user
  reviews both texts before they are sent, and signs Istio's CLA for the pull request. The unit is removed once a
  release carries the fix.
- **Measure the hardware** *(user)*: "Before the first production deploy" gains the per-pod add latency and the meshed
  pods per node on the hardware, measured with the power cut with data.

## Goals

1. The unit (*Claude's call* on the details):
   - an executable named `k3s` built from a store symlink to bash, running a loop that never `exec`s (an `exec` would
     rename the process);
   - `User=root` with no capabilities (`CapabilityBoundingSet=` empty), no network (`PrivateNetwork`), a read-only
     system (`ProtectSystem=strict`), `NoNewPrivileges`, and a small memory limit: it only needs to exist;
   - `WantedBy=multi-user.target` and ordered before `k3s.service`, so it is there before istio-cni starts;
   - its comment names the Istio issue and when to remove it.
2. Proven offline: the node flake's evaluation (`test/offline/metal_test.go`) shows the unit on every node, its
   executable's name `k3s`, `User=root`, an empty capability set and no network.
3. Proven on the testbed: istio-cni Ready on every node (a new check in the testbed's specs, which fails today), and
   `TestbedMesh` (a pod redirected through ztunnel across nodes); then G2's run.
4. CLAUDE.md: the stopgap and its removal condition under the mesh's Sandbox notes and "Before the first production
   deploy", with the hardware measurement decided above.

## Non-goals

- iptables mode on the nodes.
- Patching or rebuilding istio-cni.
- The node-restart deadlock's own Istio issue (after G2 and G12).

## Anti-goals

- A fake process that holds anything: no capabilities, no network, no files written.
- A workaround nobody remembers: the unit, CLAUDE.md and the memory name the issue that removes it.

## Approaches

- **Chosen: a named process on the nodes** while upstream is fixed.
- **iptables on the nodes** *(set aside, above)*.
- **Run k3s without its launcher**, so its own process is named `k3s` *(set aside)*: NixOS's k3s module starts the
  launcher; replacing its `ExecStart` with the data directory's binary depends on k3s's extraction layout and every k3s
  bump could break it, on the process every node depends on.
- **A patched istio-cni image** *(set aside)*: an image of our own to build, sign and bump on every Istio release.
