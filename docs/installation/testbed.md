# Testbed

The testbed is three NixOS VMs in WSL, installed and configured the way production's machines are: the same node
definition (`infra/_modules/metal`), installed from the network by `nixie`, joined into one k3s cluster by `lab cluster
enroll`, every node a k3s server with etcd, kube-vip holding the API's address. Argo CD deploys what runs on it. It proves
what the one-node [sandbox](sandbox.md) cannot: etcd with three members, addresses moving between nodes, Ceph replicating,
the ambient mesh across nodes, the nodes themselves and their upgrades
(`docs/superpowers/specs/2026-11-02-testbed-design.md`).

It is not like production, and nothing here says it is: a release it proved is printed by `lab infra forgejo verify` as
"only the testbed proved this: no production-like environment did". Someone with spare hardware runs a
[staging](staging.md) instead, and needs no testbed.

## Prerequisites

- Windows with WSL 2 and KVM (`/dev/kvm`), libvirt on the workstation (in `~/nixos-config`:
  `virtualisation.libvirtd.enable = true;` and your user in group `libvirtd`), and an SSH agent
  (`programs.ssh.startAgent = true;`)
- WSL at 13 GB with 4 GB of swap (`.wslconfig`)
- The testbed's SSH key loaded in the agent; its public key is `infra/testbed/metal/authorized_keys` (the testbed only:
  production's nodes accept only `infra/production/metal/authorized_keys`)
- The sandbox stopped (`k3d cluster stop homelab-dev`): `just testbed` refuses while the sandbox runs, and `just dev`
  while the testbed does (`just testbed-stop`)
- 30 GB free on `C:` (WSL's virtual disk, where the VMs' disks grow)

## Build

```sh
just testbed
```

It force-pushes the commit you are on to GitHub branch `testbed`, which the testbed's Argo CD syncs, and applies
`infra/testbed`:

1. `vms`: a libvirt network (`homelab-testbed`, `192.168.100.0/24`, NAT, one fixed DHCP lease per MAC address of
   `infra/testbed/metal/hosts.json`) and three VMs (`homelab-1..3`: 3 GB, 4 vCPUs (3.5 GB and 2 until 2026-11-25, see [CRDs](#crds)), UEFI, a sparse 100 GB raw disk,
   `cache=unsafe`, safe against `virsh destroy`: only a crash of WSL itself loses data)
2. `metal`: `nixie` boots the VMs it has not installed from the network, one at a time, and installs them with
   `nixos-anywhere` ([PXE boot](../concepts/pxe-boot.md)); `scripts/testbed-wake` turns its Wake-on-LAN into a power cycle
   of the VM. Then `lab cluster enroll` joins them (the first, `init_host`, starts the cluster), `lab cluster kubeconfig`
   writes `infra/testbed/kubeconfig.yaml`, and `lab cluster upgrade` rebuilds the nodes already installed whose system
   differs from the flake's, one at a time
3. `argocd`: Argo CD and its ApplicationSets (`system/argocd` with `values-testbed.yaml`)

Then it runs the testbed's specs (`just test e2e --env testbed --labels proof`: the `Testbed*` specs; the first waits for every listed Application to have synced this commit and
be healthy) and, when they pass, writes the commit status `testbed` on the commit it started from, the mark
`lab infra forgejo verify` reads.

`nixie` records what it installed in `.state/testbed/metal/nixie-hosts.json` and never installs those machines again;
`just testbed-fresh` removes the VMs, their disks and that record, then builds from empty disks. `just testbed-clean`
removes them without building, `just testbed-stop` shuts the VMs down (they keep their disks).

The nodes' host keys are trusted on first contact (only on the workstation's own libvirt network);
`just testbed-clean` forgets them with the disks.

!!! warning

    Nothing watches memory during a run: a person stops it. A run stopped for low memory is not restarted without
    understanding why. A testbed that crashes WSL or makes the workstation unusable has failed.

## What runs

`system/argocd/values-testbed.yaml` lists it, by layers, each measured from a fresh install. A layer is stable when
`just testbed`'s suite passes and, once the cluster has settled, 30 minutes go by with no k3s restart, no etcd leader
change, no API server or lease error, and WSL keeps at least 2 GB available. The 30 minutes start at the later of the
last Application turning healthy and the last k3s restart. Restarts while a fresh install settles are counted and
recorded, not failed *(user, 2026-11-26)*: on three nested VMs they come and go from run to run (one to three, see
[CRDs](#crds)), while production keeps the stricter bar on its own hardware. `leader failed to send out heartbeat`
warnings are counted, not failed.

| Layer | Adds | Proves | Here (i5-12500H, WSL 13 GB) |
| --- | --- | --- | --- |
| 1 | Argo CD, rook-ceph | Ceph on three nodes through Argo CD | holds, 2 vCPUs |
| 2 | cert-manager, Istio | the ambient mesh across nodes, the Gateway at its VIP | holds, 2 vCPUs |
| 3 | CloudNativePG, three instances failing over | what otherwise only production runs | does not hold |
| beside 2 | Falco (`system/falco`) | the modern eBPF driver on NixOS's kernel | does not hold |

Measured from `just testbed-fresh`:

| | Layer 1 | Layer 2 | Layer 3, 2 vCPUs | Layer 3, 4 vCPUs |
| --- | --- | --- | --- | --- |
| Tests | pass | pass | pass | Ceph (a test race, fixed), Mesh fail; Postgres cut by go test's hour |
| `TestTestbedCeph`, which starts about a minute after the bootstrap | 12.6 min | 19.7 min | 29.4 min | 22 min |
| k3s exits, install and tests | 0 | 0 | 4 | 1 |
| In the 30 minutes: k3s exits, leader changes | 0, 0 | 0, 0 | 1, 1 | not measured |
| In the 30 minutes: heartbeat warnings | 2 (35 ms, 2.3 s over) | 0 | about 20 (up to 3.2 s over) | not measured |
| CPU idle in each node at the end | 86–93% | 62–82% | 17–67% | not measured |
| WSL's lowest available: window; first sync | 2.38 GB; | 2.50 GB; 1.61–1.85 GB | 2.83 GB; 1.86 GB | not measured; 1.68 GB |
| WSL's swap in use | | 1.95 GB | 2.04 GB | |

Layer 3's failover itself passes, in about 19 minutes at 2 vCPUs. What fails is the control plane around it: under the
install and Postgres on Ceph, k3s lost its leases and left. At 4 vCPUs the nodes were 41–84% idle on average and k3s left
once, so CPU alone does not explain it. The Gateway's address also stopped answering at 4 vCPUs: most likely k3s's ServiceLB writing the
Service's status empty (reproduced at 2 vCPUs, matched at 4 only by its symptom), since fixed by turning it off on the nodes (`TestTestbedGatewayKeepsItsAddress`). So the list stops at layer 2 on
this machine.

Another machine extends the list in `system/argocd/values-testbed.yaml` and its VM size in `infra/testbed/vms/terragrunt.hcl`
(`vcpus`, up to 4 before measuring why more is needed). For layer 3 add `platform/cloudnative-pg` and a `values-testbed.yaml`
beside it with `cloudnative-pg.monitoring.podMonitorEnabled: false`; `TestTestbedPostgresFailover` runs once it is listed.

"Up to everything" is a direction, not a promise: past these layers, components need more than the list (OpenBao and its
units, Forgejo, tests made general for them).

## Power

```sh
just testbed-power
```

On a healthy testbed, from the main checkout, alone: it wrecks the cluster's availability, so it is never part of `just testbed` and gives
no mark. Run it when `lab cluster shutdown|startup`, the node profile or the recovery path change, and before step 12b. `--tests` picks
the specs by label, one run each (`cut-every-node`, `shutdown-startup`, `node-not-returning`: PowerCutEveryNode,
PowerShutdownStartup, PowerNodeNotReturning). A probe on a Ceph volume writes numbered lines, flushing
each, and every number it reported must be in its file afterwards ([what they cover](../how-to-guides/power.md)).

Measured at 2 vCPUs from a fresh install, from the moment the VMs start:

| Test | | Time |
| --- | --- | --- |
| Every node cut (`virsh destroy`) | the API at its VIP, every node Ready on a new boot, etcd whole | 1m07s |
| | Ceph `HEALTH_OK` with every OSD answering, the Gateway at its VIP | 3m02s |
| | 58 acknowledged lines intact, the probe writing again | 5m25s, with the checks |
| Ordered shutdown | the nodes off, after `lab` returned: the one holding the probe's volume cut at 180 s, the others in 20 and 90 s | 3m05s |
| | back on: every node Ready | 1m15s |
| | the marker gone (CronJobs, Argo CD's controller and `noout` undone), Applications Synced | 3m18s |
| A node not returning | CronJobs and Argo CD's controller back, with two nodes | 1m23s |
| | `noout` off and the marker gone, at its limit | 31m23s |

WSL's lowest available across these runs was 1.66 GB.

Two status fields read wrong after a cut, so the tests and `cluster-startup` ask the thing itself. An OSD's pod stays `Ready` and `ceph osd stat`
counts it `up` for a while after its node died (only `ceph tell osd.N version` tells), and a CephCluster's or CloudNativePG `Cluster`'s status
says what the operator saw before.

**What this does not prove.** `virsh destroy` kills QEMU, and with `cache=unsafe` what the guest handed to QEMU survives in WSL's page cache, a
flush forced to no disk. So it proves the software recovers and that writes the guest never flushed do not count as acknowledged, not
that a real disk keeps what it acknowledged. Without a UPS that risk sits entirely in the hardware: step 12b prefers SSDs with power-loss
protection and repeats the cut before real data.

### Falco, one measurement

Once, on layers 1 and 2 after `just testbed` (2026-11-10, 2 vCPUs, the nodes just upgraded and rebooted), with `system/falco` on
the list; it is off the list since. `TestbedFalco` passed: the modern eBPF probe loaded on all three nodes and a dropped binary was
reported. Then 10-minute windows, with Falco, without it, and with it again:

| | With Falco (30 min) | Without Falco | Falco added again |
| --- | --- | --- | --- |
| k3s exits, leader changes | 0, 0 | 0, 0 | 1 exit (homelab-2), the API's VIP briefly gone |
| API or lease errors (`deadline exceeded`) | 3, 12, 0 | 0, 0, 0 | |
| Heartbeat warnings | 0, 30, 0 | 0, 4, 0 | |
| `k3s server`'s CPU, of 2 vCPUs | 50–88% | 17–33% | load average 11–18 |
| CPU idle in each node | 0–20% | 74–77% (one node 12%, Argo CD catching up) | |
| WSL's lowest available | 2.18 GB | | |

Falco itself used 7–12% CPU per node; the cost is the probe on every system call, which `k3s server` makes the most of. So the
driver's proof on NixOS is this run, by hand: add `system/falco` to `system/argocd/values-testbed.yaml` on a branch, run `just
testbed`, then `just test e2e --env testbed --labels falco`, and take it off again. Taking it off the list leaves it running:
`root` keeps the resources of a path it drops (`preserveResourcesOnDeletion`), so delete them too (`kubectl -n argocd delete
application falco`, `kubectl delete namespace falco`). 15h's upgrade found it still on the nodes, where it restarted kube-vip
and lost the API's address during a drain. Production's nodes are larger; its own numbers come from `FalcoEventsDropped` and
the nodes' load in the first days.

### CloudNativePG, one measurement

Once, not on the list and not in the mark (`just testbed-power --tests cut-postgres`, PowerCutPostgres, the operator rendered and applied by hand with its `PodMonitor`
removed, an instance on each node, 2 vCPUs):

| | |
| --- | --- |
| Result | pass: the same primary with the write after 4m20s, two replicas streaming after 5m37s |
| k3s exits | 1, on one node, 49 s after its boot (below) |
| CPU idle in each node, average over 15 minutes (lowest 0) | 10–30% |
| I/O wait, average; highest | 0–3%; 21% |
| MemAvailable in each node, lowest | 1.08–1.21 GB |
| Disk, average per sample of about a minute | written 120–140 MB, read 330–890 MB |
| WSL's lowest available | 1.69 GB |

The nodes ran short of CPU, not of disk or memory, while everything restarted at once, and rode it out. No lease was lost: 12a-2's finding
did not recur, so it says nothing about that and was not repeated at 4 vCPUs. k3s stopped itself twice across the two runs, both on one node shortly after a boot, and systemd restarted it: `failed to start
networking: unable to initialize network policy controller: error getting node subnet` here, and in a first run (failed by a test reading stale status) a nil pointer in Flannel 0.28.4's
`watchVXLANDevice`. Neither stopped the recovery; both go with k3s's own updates.

## CRDs

Each environment applies only the CRDs it needs: those of the components it runs and those of the kinds its renders use
(`system/crds/environments/<env>.txt`, written by `lab crds generate`, kept by the basic check `crds`;
`infra/_modules/argocd/apply-crds.sh`). The testbed applies 49 of the 184 vendored CRDs (the 205 a cluster then served
also counted Argo CD's own, the Gateway API's and rook-ceph's `csi.ceph.io`); production runs every
component, so its list holds them all; the sandbox applies every one. A CRD that leaves a list is never deleted: remove it
by hand once (`kubectl delete crd <name>`, safe when no object of its kind exists), or rebuild with `just testbed-fresh`.

Why (2026-11-25, `docs/superpowers/specs/2026-11-25-testbed-capacity-design.md`): from 14h (2026-11-20) the bootstrap
applied all 205 CRDs everywhere, and the testbed stopped settling: every k3s server keeps and serves them, and every
controller lists them again after a restart, so k3s lost its leader lease and restarted on every node. Each run: the VMs
off, started 90 s apart, sampled for 36 minutes (the host and the VMs every 30 s, CPU and memory per namespace every
2 minutes):

| Run | k3s restarts (three nodes) | CPU pressure in the VMs | WSL swap in / out |
|---|---|---|---|
| 2 vCPUs, 3.5 GB | 9 | 60–95% | ~4 / 4 MB/s |
| 4 vCPUs, 3.5 GB | 8 | 50–77% | ~3 / 3 MB/s |
| 4 vCPUs, 3.5 GB, guests' `vm.swappiness` 180 | 8 | 45–80% | unchanged |
| 4 vCPUs, 3 GB | 11 | 55–83% | ~2 / 2 MB/s |
| 4 vCPUs, 3 GB, 138 of 205 CRDs removed at minute 12 | 2, none after minute 25 | 35–50% | ~1 / 1 MB/s |

Every exit was `leaderelection lost` after etcd timeouts (peer round trips mostly over 100 ms; fsync under 8 ms); KSM
merged nothing. Ceph then takes 2–3.5 cores across the nodes, within the bar.

Then three runs of the whole scenario (a fresh install and `just testbed`'s suite, 60 minutes sampled from the
bootstrap, 2026-11-25 and 26), one change each:

| Run | k3s restarts | Suite | When |
|---|---|---|---|
| Only its own CRDs (64 served) | 2 | 11 of 14 | while `TestbedCeph` wrote across nodes; etcd reads took 13–14 s |
| And k3s first: its own slice at `CPUWeight=1000`, a CPU reserved | 1 | 15 of 16 | while settling; the pods starved instead (Ceph's and Argo CD's probes timed out), the Applications flapped for 55 minutes. Not adopted |
| And istiod at two replicas (its HPA had gone to five) | 3 | 16 of 16 | 17 and 29 minutes in, while settling; none in the 32 minutes after |

From one restart to three is noise on this workstation, so one run per change cannot tell such small changes apart:
the CRDs were the cause, and the bar above counts the settled cluster. Settled, Ceph takes about 0.75 of a core across
the nodes (sampled every 30 s for 3 minutes): its three mons about 0.3, its OSDs about 0.25. Turning off its dashboard
and device health and probing its daemons every 30 s (`system/rook-ceph/values-testbed.yaml`) changed that by less than
the noise; they stay off because nothing on the testbed reads them.

## Memory

The VMs hand the pages they free back to WSL (virtio balloon with free page reporting), but not their page cache: on a VM
only, a timer (`profiles/vm-memory.nix`) reclaims cache each minute until a quarter of the node's memory is free, so that
quarter goes back to WSL (without it each VM grew to 3.0–3.4 GB and WSL ran out). Each node also runs zram swap. QEMU holds
2.8–3.4 GB per VM.

## Differences from production

- VMs on one workstation: no real power loss ([Power](#power)), no real Wake-on-LAN (`scripts/testbed-wake` plays it), one disk per node
  (SCSI, `/dev/sda`) shared by the system and Ceph's OSD
- `slow_control_plane` (`infra/testbed/cluster/config.json`): etcd heartbeat 500 ms and election 5 s, controller leases 60 s.
  Production keeps the defaults, so a stall this environment rides out would be an election there
- Ceph: one mgr, a 1.25 GiB OSD, no CephFS
- UDP tunnel offload off on VMs (`vm-tunnel-offload`): their peers refused it (`bad gso`)
- Certificates from the local CA, no Cloudflare or Telegram (`values-local.yaml`), no image cache, Argo CD's limits in
  `values-testbed.yaml`
- No Kyverno and no monitoring operator: the `PolicyException`s and `PodMonitor`s of the charts it runs are off, listed as
  `not_run` in `infra/testbed/release-paths.yaml`, so a release changing only those needs no testbed run
- ServiceLB is off on the nodes, as in production; the sandbox keeps it
- No Forgejo: the sandbox proves Argo CD reading `ops/homelab` (`TestForgejoArgoCDSyncs`)
