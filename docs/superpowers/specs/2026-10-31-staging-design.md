# Step 12a: staging

Status: designed in chat 2026-10-31. Branch: `feat/staging` from `dev`. Decisions marked *(user)* were made in chat;
the rest are *(Claude's call)*.

## Why

The sandbox is k3s in Docker on one node (k3d). Production is k3s on NixOS on three nodes. What only exists with
several real nodes has never run: etcd with three members, kube-vip moving the API and Service addresses, Ceph
replicating across nodes, CloudNativePG's failover, a power cut of every node at once, and the NixOS nodes themselves
(their install, their k3s configuration). Staging is three NixOS VMs in WSL, as close to production as the
workstation allows *(user)*, so a release that changes the infrastructure is proven there before production.

## Goals

- **One node definition for staging and production** *(user)*. Upstream `nixos`'s layout replaces `metal/`
  (Ansible, still written for Fedora): `infra/_modules/metal` (a NixOS flake: `configuration.nix`, `disks.nix` with
  disko, profiles `k3s-server`, `kube-vip`, `network`, `rook-ceph`, and the `installer`), a Terragrunt unit
  `infra/<env>/metal` with `hosts.json`, and `lab cluster enroll` and `lab cluster kubeconfig` ported from upstream.
  `metal/`, `nix develop .#metal` and `make metal` are removed in this step, not kept until 12b *(user)*.
- **Installed the way production is: `nixie`** *(user)*. The VMs boot from the network (PXE), `nixie` serves its
  installer from the workstation, finds each machine by its MAC address and installs it with `nixos-anywhere`; then
  `lab cluster enroll` joins the nodes and `lab cluster kubeconfig` writes `infra/staging/kubeconfig.yaml`.
- **Three VMs of 3.5 GB each** *(user)*, measured as the work goes *(user)*: memory is the constraint (the
  workstation has 16 GB, WSL 12 GB).
- **Ceph first, fewer apps** *(user)*: staging proves rook-ceph on three nodes; apps follow as far as memory allows
  ("the more apps the better if resources allow, fewer if not" *(user)*).
- **Staging stays installed and is upgraded in place** *(user)*, like the sandbox: `make staging` brings the running
  staging to the current commit, so a release is proven as production will take it (a running cluster with etcd,
  Ceph and CloudNativePG data moving to new NixOS, k3s, Istio and rook versions); `make staging-fresh` reinstalls the
  VMs with `nixie` from empty disks. Nodes are upgraded one at a time (drain, `nixos-rebuild` from the flake over
  SSH, wait for the node and Ceph to be healthy, uncordon): upstream has no such path, and production needs the same
  one, so it is part of `infra/_modules/metal` and `make infra env=production` too.
- **`make staging`** upgrades staging to the current commit, runs the staging tests and marks the commit
  as proven; `lab infra forgejo verify` refuses a release touching `infra/`, nodes, k3s, kube-vip, Istio, storage or
  the deploy path without that mark, unless `--skip-staging=<reason>` (recorded) (the roadmap's 12a).
- **An ordered shutdown and a power cut of all three VMs**, each measured (time to recover) and checked (data written
  before is intact), as the roadmap's step 12 describes: `lab cluster shutdown` / `lab cluster startup`, NUT's
  low-battery shutdown, and `virsh destroy` of every VM at once.

## Non-goals

- Real hardware, the UPS itself, BIOS settings, Wake-on-LAN on real machines: step 12b.
- Step 15d (secrets encryption at rest checked, kube-bench, kubescape): it runs on staging after this step.
- Reaching staging's pages from the Windows browser: the tests run in WSL. *(Claude's call; a localhost proxy like
  the sandbox's can come later.)*
- Running staging in GitHub Actions.

## Anti-goals

- A staging that differs from production where it matters: separate node definitions, another install method, k3d.
- A staging that crashes WSL (memory, or `C:` filling up: `memory/wsl-memory-limit`): it runs only with the sandbox
  stopped and checks `C:`'s free space first.

## Constraints

- WSL has 12 GB and 3 GB of swap; the sandbox uses about 9 GB under its suite, so staging and the sandbox never run
  together: `make staging` refuses while the k3d cluster runs (and `make dev` while staging runs).
- KVM works inside WSL (`/dev/kvm`, nested virtualisation).
- `nixie` runs as root (ports 67/69/4011/80) and needs a DHCP server on the network it serves (it answers as a
  proxyDHCP).
- `C:` keeps 30 GB free: staging's disks are sparse qcow2 files in WSL's virtual disk.
- Latest stable pins everywhere (CLAUDE.md), so not upstream's pins as they are (Gateway API v1.6.3, not v1.4.0).

## Design

### Nodes (`infra/_modules/metal`)

Ported from upstream `nixos` (`infra/_modules/metal/flake/`), then adapted:

- k3s keeps this repository's choices where they differ from upstream's: ServiceLB stays on and kube-vip serves only
  its class `kube-vip.io/kube-vip-class` (the Gateway's and WireGuard's addresses from `config.json`), the API
  server's audit policy and flags (step 15c: `metal/roles/k3s/files/audit-policy.yaml` moves into the module, and the
  k3d sandbox mounts it from there), `--secrets-encryption`, the control plane's metrics, the packaged Gateway API
  CRDs disabled and the latest installed through k3s's manifests, `disable-helm-controller`.
- Each environment's `infra/<env>/cluster/config.json` keeps its current format and gains what the nodes need
  (upstream's `init_host`; the network is already there). `infra/<env>/metal/hosts.json` holds the machines (name,
  MAC, address, disk). Production's MAC addresses stay placeholders until 12b.
- Upgrades: kured stays (cluster side); a node changes only through `make infra` / `make staging`, which rebuild it
  from the flake one node at a time (above) *(user: in place)*; no unattended `system.autoUpgrade` *(Claude's call)*.
- zram swap on every node *(Claude's call: it lets 3.5 GB nodes keep more in memory)*.

### Staging's VMs (`infra/staging`)

- Three VMs (`homelab-1..3` or this repo's names), 3.5 GB, 2 vCPUs each *(Claude's call)*, a system disk and the
  `rook-data` partition from `disks.nix`'s layout (or a second disk: the plan picks what disko and rook need), on one
  bridge in WSL with fixed MAC addresses, booting from the network first.
- libvirt manages them *(user)*: a network with DHCP and NAT on the bridge, static leases by MAC, `virsh destroy` as
  the roadmap's power cut, created by an OpenTofu unit in `infra/staging`. The workstation's `~/nixos-config` (outside
  the repository) gains `virtualisation.libvirtd.enable` and the user in group `libvirtd`; the exact change is shown
  to the user before it is made.
- The sandbox's patterns where staging also needs them: a local CA for certificates, the k3d pull-through caches
  reused if the VMs can reach them, OpenTofu state local and encrypted like the sandbox's (`.state/staging/`).

### What runs on staging

- Production's values first; a `values-staging.yaml` only where staging must differ (local CA, Ceph's memory,
  replica counts that do not fit, the reduced app set). Ceph: `osd_memory_target` about 1 GB, one mgr.
- If the core does not fit, in this order *(user)*: drop apps (the core stays: k3s with etcd, Istio, Argo CD,
  cert-manager, ESO, OpenBao, rook-ceph); squeeze the core (Ceph's OSD toward 900 MB, Prometheus off); raise each VM
  to 3.75 GB with WSL at 12 GB; last, WSL to 13 GB. Three nodes stay (etcd's and Ceph's quorum and replicas) and so
  does Istio.
- The app set grows by measurement, in this order *(Claude's call)*: Argo CD, Istio, cert-manager, ESO and OpenBao,
  rook-ceph, Kanidm, Forgejo with CloudNativePG's three instances, Velero (an S3 stand-in like the sandbox's), then
  Prometheus with a short retention. Each addition is measured (each node's memory, idle and under the tests); one
  that does not fit is left out and written down.
- Argo CD follows production's deploy path: the bootstrap, then `ops/homelab` on staging's Forgejo with the commit
  under test (`lab infra forgejo sync|push`), so the switch the sandbox cannot prove runs here.

### `make staging` and the mark

- `make staging` (main checkout only, like `make dev`): refuses while the sandbox runs or when `C:` has less than
  30 GB free; applies `infra/staging` (VMs and `nixie` only when a VM is new or empty, enroll, the nodes' rolling
  upgrade, Argo CD); deploys the current commit; runs the staging tests; marks the commit. `make staging-fresh`
  destroys the VMs' disks first.
- The mark must still hold for the release: the commit on `master` is a merge commit, not the one staging proved.
  *(Claude's call, the plan decides the mechanism: a GitHub commit status `staging` on the proved commit, accepted by
  `verify` when the release's infrastructure paths equal the proved commit's.)*

### Shutdown and power cut

- `lab cluster shutdown`: pause the external checks, suspend CronJobs, wait for running Jobs, Ceph `noout`, then the
  nodes; `lab cluster startup` undoes it and waits for etcd, Ceph and CloudNativePG to be healthy (roadmap step 12).
- NUT through NixOS's `power.ups`: `upsd` on one node, `upsmon` on the others. Staging has no UPS: NUT's `dummy-ups`
  driver plays one and the test sets its low battery *(Claude's call)*.
- The tests: an ordered shutdown and startup, and `virsh destroy` of all three VMs at once then start, each
  measuring the recovery and checking data written before (Forgejo, OpenBao, a CloudNativePG write), like
  `TestNodePowerCut` does for one node.

## Order of work *(Claude's call)*

Three pull requests, like 6d or 15b:

1. **12a-1 nodes:** the `metal` module and flake, `metal/` removed, staging's VMs, `nixie`, enroll, kubeconfig,
   the nodes' rolling upgrade, k3s on three nodes with kube-vip, rook-ceph; measured. The first task proves `nixie` on a WSL bridge.
2. **12a-2 platform:** what runs on staging, the deploy path through Forgejo, `make staging`, the mark and
   `verify`'s check.
3. **12a-3 power:** `lab cluster shutdown|startup`, NUT, the shutdown and power-cut tests.

## Left to the builder

- Node and network names and addresses, the libvirt network's range, VM disk sizes.
- How `lab cluster enroll`/`kubeconfig` are ported (upstream's code, adapted to this `cli/`).
- The test layout (staging tests beside the sandbox's in `test/`, selected by environment).
- Docs: `docs/installation/` (staging, production's install by `nixie`), CLAUDE.md's roadmap and the "before the
  first production deploy" list (what staging now proves).
