# Staging

Staging is three NixOS VMs in WSL, installed and configured the way production's machines are: the same node
definition (`infra/_modules/metal`), installed from the network by `nixie`, joined into one k3s cluster by `lab cluster
enroll`, every node a k3s server with etcd, kube-vip holding the API's address, rook-ceph on each node's disk. It
proves what the one-node [sandbox](sandbox.md) cannot: etcd with three members, the address moving between nodes,
Ceph replicating, the nodes themselves and their upgrades
(`docs/superpowers/specs/2026-10-31-staging-design.md`).

## Prerequisites

- Windows with WSL 2 and KVM (`/dev/kvm`), libvirt on the workstation (in `~/nixos-config`:
  `virtualisation.libvirtd.enable = true;` and your user in group `libvirtd`), and an SSH agent
  (`programs.ssh.startAgent = true;`)
- Staging's SSH key loaded in the agent: `ssh-add ~/.ssh/homelab-staging`; its public key is
  `infra/staging/metal/authorized_keys` (staging only: production's nodes accept only
  `infra/production/metal/authorized_keys`)
- The sandbox stopped (`k3d cluster stop homelab-dev`): WSL's 12 GB hold one of them. `make staging` refuses while
  the sandbox runs, and `make dev` while staging does (`make staging-stop`)
- 30 GB free on `C:` (WSL's virtual disk, where the VMs' disks grow)

## Build

```sh
make staging
```

It applies `infra/staging`:

1. `vms`: a libvirt network (`homelab-staging`, `192.168.100.0/24`, NAT, one fixed DHCP lease per MAC address of
   `infra/staging/metal/hosts.json`) and three VMs (`homelab-1..3`: 3.5 GB, 2 vCPUs, UEFI, a sparse 100 GB disk)
2. `metal`: `nixie` boots the VMs it has not installed from the network and installs them with `nixos-anywhere`
   ([PXE boot](../concepts/pxe-boot.md)); `scripts/staging-wake` turns its Wake-on-LAN into a power cycle of the VM, so
   a VM boots only once `nixie` answers. Then `lab cluster enroll` joins them (the first, `init_host`, starts the
   cluster), `lab cluster kubeconfig` writes `infra/staging/kubeconfig.yaml`, and `lab cluster upgrade` rebuilds the
   nodes already installed whose system differs from the flake's, one at a time (drain, `nixos-rebuild switch`, a
   reboot when the kernel changed, back Ready, uncordon, the cluster healthy again: its Ceph mon and OSD running,
   `HEALTH_OK`)

Then it applies rook-ceph (`system/rook-ceph`, with `values-staging.yaml`) and runs the staging tests (`TestStaging*`).
Argo CD and the platform come in step 12a-2.

`nixie` records what it installed in `.state/staging/metal/nixie-hosts.json` and never installs those machines again;
`make staging-fresh` removes the VMs, their disks and that record, then builds from empty disks. `make staging-clean`
removes them without building, `make staging-stop` shuts the VMs down (they keep their disks).

The nodes' host keys are trusted on first contact (only on the workstation's own libvirt network);
`make staging-clean` forgets them with the disks.

!!! note

    The first install downloads `linux-firmware` (820 MiB) into the Nix store, about 30 minutes on a slow link;
    later installs take about 4 minutes for the three nodes. An upgrade that changes no kernel takes under 2 minutes.

## Resource usage

Measured during `make staging` (rook-ceph, the tests) on a 16 GB workstation with WSL at 12 GB:

| | Each node | WSL |
| --- | --- | --- |
| Kubernetes, rook-ceph and the system (`used`) | 0.9–1.3 GB | |
| Page cache kept by the node | 1.3 GB | |
| Free inside the node (the reclaim timer's floor: a quarter) | 0.8–1.2 GB | |
| QEMU's memory in WSL | 2.4–3.0 GB | 7.5–8.1 GB for the three |
| Available in WSL (lowest, two runs) | | 1.5–2.1 GB |
| WSL's swap in use (highest) | | 1.6 GB |

The VMs hand the pages they free back to WSL (virtio balloon with free page reporting), but not their page cache:
on a VM only, a timer (`profiles/vm-memory.nix`) reclaims cache each minute until a quarter of the node's memory is
free, so that quarter goes back to WSL (without it each VM grew to 3.0–3.4 GB and WSL ran out). Each node also runs
zram swap. If staging grows past this, the order is: fewer apps; a smaller core (Ceph's OSD toward
900 MB); 3.75 GB per VM with WSL at 12 GB; WSL at 13 GB last.

## Differences from production

- VMs on one workstation: no real power loss, no real Wake-on-LAN (`scripts/staging-wake` plays it), one disk per
  node (SCSI, `/dev/sda`) shared by the system and Ceph's OSD
- Ceph: one mgr, a 1.25 GiB OSD, no CephFS, and Ceph v20's warnings about CephX keys that are not AES256K muted (their
  CSI clients need Linux 7.0)
- No Argo CD, platform or apps yet (step 12a-2)
