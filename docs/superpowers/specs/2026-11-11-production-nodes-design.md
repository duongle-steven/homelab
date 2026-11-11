# Step 12b-1: production's nodes on the hardware

Status: designed in chat 2026-11-11, with a PXE spike on the hardware the same day. Decisions marked *(user)* were made in chat;
*(Claude's call)* marks the builder's own, open to review. Builds on 12a (`docs/installation/testbed.md`,
`docs/superpowers/specs/2026-11-02-testbed-design.md`, `docs/superpowers/specs/2026-11-04-power-design.md`): the same node
flake (`infra/_modules/metal`), `nixie`, `lab cluster enroll|kubeconfig|upgrade|shutdown|startup`, now on real machines.

## Intent

Step 12b brings production up on the three mini PCs, split so each part changes one thing *(user)*:

- **12b-1 (this spec): the nodes.** NixOS installed from the network, one k3s cluster of three servers behind the API's VIP,
  and the machine-level proofs that only real hardware gives: the disks' fsync latency, a node going away, every node losing
  power at once.
- 12b-2: the platform through Argo CD (Ceph, Istio, OpenBao, Kanidm, monitoring, backups to R2), with the power cut repeated
  with Ceph before any real data.
- 12b-3: every app, the public path (Cloudflare Tunnel), the first `just infra deploy production`, then `lab backup enable`.

Production is "ok" when every app in the plan runs there, including those the sandbox and the testbed never ran for lack
of resources (Matrix, Jellyfin, cloudflared, WireGuard, Tailscale, Renovate, the blog) *(user)*. Immich, the teaching app
(step 18), a NAS or a second disk come after *(user)*.

## The hardware *(user, read by the spike)*

All three are cabled to the TP-Link Archer C64 (`192.168.0.0/24`, its DHCP `.100`–`.249`), on mains power, no UPS.

| Node | Machine | CPU | RAM | Disk | MAC (`eno1`) |
|---|---|---|---|---|---|
| `homelab-1` (`192.168.0.11`, `init_host`) | HP EliteDesk 800 G4 DM 35W | i5-8500T, 6 cores | 16 GB | `/dev/nvme0n1`, SK hynix BC901 256 GB | `38:22:e2:2c:dc:35` |
| `homelab-2` (`192.168.0.12`) | HP EliteDesk 800 G3 DM 35W | i5-7500T, 4 cores | 16 GB | `/dev/nvme0n1`, Samsung MZAL8256HDJD 256 GB | `10:62:e5:16:d2:ca` |
| `homelab-3` (`192.168.0.13`) | HP EliteDesk 800 G3 DM 35W | i5-7500T, 4 cores | 16 GB | not read yet (Secure Boot refused the installer) | `10:e7:c6:0f:63:a0` |

Names, addresses and `init_host` are *(Claude's call)*: the G4 starts the cluster because it carries it alone for its first
minutes; afterwards the three are equal servers. Both disk models are DRAM-less OEM NVMe drives without power-loss
protection.

One disk per node stays for now; a second disk is decided together with the NAS idea *(user)*. The layout stays upstream's
(`infra/_modules/metal/flake/disks.nix`): ESP 1 GB, root 64 GB (NixOS, k3s, etcd, images, logs), the rest (about 190 GB) a
raw partition `rook-data` for Ceph in 12b-2. No LVM: Ceph takes a raw block device.

## Goals

- `just infra deploy production`'s metal path, i.e. the unit `infra/production/metal` (written in this step, like
  `infra/testbed/metal`), installs the three machines from the network with `nixie`, enrolls them into one k3s cluster and
  writes `infra/production/kubeconfig.yaml`. In this step it runs alone (`terragrunt apply` in that unit), not the whole
  deploy.
- The nodes resolve names quickly despite the ISP modem's DNS interception (below), and so do pods through CoreDNS.
- The fio numbers that decide whether etcd and Ceph can share one disk.
- A node going away moves the API's VIP; the cluster keeps answering.
- Every node cut at once comes back by itself.
- kube-bench on the hardware (`just testbed-scan`'s Job, on production's kubeconfig), the before-production list's item.

## Non-goals

- Ceph, Argo CD and everything above k3s: 12b-2 *(Claude's call: production's Argo CD syncs the whole platform at once,
  with no layer allow-list like the testbed's, and adding one only for this step would fork production's config)*.
- A UPS (its own step) and a second disk or NAS.
- Secure Boot with signed boot (`lanzaboote`): later, if wanted; with disks unencrypted it protects little.
- A BIOS administrator password: none *(user agreed after the trade-off)*. Anyone at the machine can take the NVMe out and
  read it (LUKS set aside in 15d), so the password guards little and a forgotten HP password is hard to reset.

## Anti-goals

- Wiping a disk that is not one of these three: `nixie` serves PXE on the home LAN, so only `hosts.json`'s MACs may be
  installed, and the run is started by hand.
- Leaving the workstation's Hyper-V firewall open after the install.
- A docs line implying the testbed proved the hardware path, or that the cut proves Ceph (12b-2 repeats it).

## Decisions

### 1. Before the install: the BIOS, on every machine

A checklist in the installation docs, done by hand:

- BIOS updated to HP's latest for the model (G4: `Q21`, G3: `P21`). Found on `homelab-3`: `P21 Ver. 02.15` (2018-01-31),
  which kept no change made in F10 (Secure Boot, Power State); updating it is the first fix, a CR2032 battery the second.
- Secure Boot off ("Legacy Support Disable and Secure Boot Disable"): `nixie`'s iPXE is unsigned and the firmware refused it
  ("Selected boot image did not authenticate"). `homelab-1` and `homelab-2` had it off already.
- Network boot (UEFI IPv4) and Wake-on-LAN on *(user: done)*, Wake-on-LAN booting to the network and the NVMe first in the
  normal boot order (decision 6).
- After Power Loss: Previous State (Last State), so a cut machine boots when power returns (12a-3).
- F12 starts a network boot by hand on these HP machines, if Wake-on-LAN does not (the startup menu ignored one USB keyboard).

### 2. PXE from the workstation *(spike 2026-11-11)*

`nixie` runs in WSL (mirrored networking) on the laptop, which reaches the Archer over Wi-Fi. It answers as ProxyDHCP beside
the Archer's DHCP (UDP 67, 69, 4011; TCP 80 and its API on 5000). WSL sees no LAN broadcast until Hyper-V firewall rules
for WSL (VM creator `{40E0AC32-46A5-438A-A0B2-2B479E8F2E90}`) allow those ports inbound; with them, a ProxyDHCP server
booted the flake's installer into RAM on `homelab-1` and `homelab-2`, about 500 MB over Wi-Fi in under two minutes.

- The docs give the two `New-NetFirewallHyperVRule` commands (UDP 67, 69, 4011; TCP 80, 5000) before the install and
  `Remove-NetFirewallHyperVRule` after it, run by the person in an administrator PowerShell.
- `nixie` gets the laptop's LAN address (`--address`), as the testbed gives it the bridge's.
- One machine at a time, as on the testbed. The person starts the run; `nixie` wakes the machines.

### 3. The disk path

`hosts.json` names each node's disk (`"disk": "/dev/nvme0n1"`); `disks.nix` already reads `hostConfig.disk`. `nixie` keeps only
`mac_address`, `ip` and `machine_id_hash` when it records an install, so it works on a copy (`scripts/testbed-nixie`'s way):
the unit's wrapper for production does the same, and the repository's `hosts.json` keeps `disk`. This settles the
before-production note about disks other than `/dev/sda`. Whether the wrapper is shared with the testbed's or its own is the
builder's call.

### 4. DNS *(Claude's call)*

The ISP's ZTE modem intercepts every UDP/53 query leaving the LAN: negative answers come back REFUSED after 6–13 s. The Archer
(`192.168.0.1`) forwards over DoH and answers in about 50 ms. `network.nix` gives the nodes `8.8.8.8` and `1.1.1.1` over UDP,
and k3s's CoreDNS forwards to the node's resolvers, so both nodes and pods would hit the 6-second answers.

- The nodes' DNS comes from `config.json` (a field such as `network.dns`, defaulting to the gateway), so production uses the
  Archer and the testbed keeps its own. The testbed's NAT has no interception.
- CoreDNS reads the node's `resolv.conf`, so it follows.
- The proof is a measurement: an NXDOMAIN and an AAAA-less name answer in under a second from a node and from a pod.
- Fallback, if the Archer's single DoH upstream proves unreliable: DNS over TLS from the nodes (systemd-resolved) and
  CoreDNS forwarding over TLS. Not built now.

### 5. The production SSH key

`infra/production/metal/authorized_keys` gets production's own key: a new ed25519 key with a passphrase, made by the person
(`ssh-keygen`), loaded into the agent (`ssh-add`) for the install, `lab cluster` and the specs. It replaces the placeholder.

### 6. Boot order, the install record, and never reinstalling a live node *(Claude's call)*

- **Boot order** *(upstream's option 2, `docs/installation/production/prerequisites.md`)*:
  - Wake-on-LAN boots from the network (HP: S4/S5 Wake on LAN = "Boot to Network"), and the normal boot order puts the NVMe
    first. `nixie` wakes the machines it has not recorded and installs them with no one pressing F12. A machine switched on
    by its button, or by power returning after a cut, boots its NixOS.
  - A woken node that finds no `nixie` must fall back to its NVMe. The VIP spec below wakes a node and checks that it does,
    which is the thorough test upstream asks for.
- **The install record:** `nixie`'s record (`machine_id_hash`) stays local in `.state/production/metal/`, as the
  testbed's does (it is not secret, and `nixie` rewrites it).
- **Losing the record:** if the record is lost, the wrapper must still not reinstall a live node. Before it gives `nixie`
  a host, it checks the host's address over SSH with production's key. A node that answers as an installed NixOS is
  recorded and skipped, never reinstalled. Reinstalling a node is its own explicit command.

### 7. Production's SSH host keys *(Claude's call)*

The keys are trusted on first contact, as on the testbed, but only during the install and only for `hosts.json`'s
addresses on the home LAN. The accepted risk is someone on the LAN impersonating a node in the minutes between the
install and the first contact. Generating host keys ahead of time and installing them with `nixos-anywhere` would add key
material to keep and rotate, which this does not justify. The docs say how to check a key's fingerprint on the machine's
console later (`ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`).

### 8. What runs on the cluster in this step

k3s with this repository's flags, kube-vip for the API's VIP (`192.168.0.10`), nothing from Argo CD. Production's
`config.json` has no `slow_control_plane`: the hardware runs k3s's defaults. That is the point of measuring here.

## Proofs

- **The install:** `terragrunt apply` in `infra/production/metal` installs the three nodes and writes the kubeconfig, and all
  three are Ready. The first run is the RED: nothing is installed.
- **The nodes:** the testbed's node specs run on production: the nodes, secrets encrypted in etcd, and the seccomp
  profile's file and flag. They are labelled `testbed` today. The builder decides how they run against production: shared
  specs labelled for both, or production specs reusing the same helpers.
- **The VIP moving** *(Claude's call)*: production's version of `TestbedVIPMoves` stops a node over SSH, and Wake-on-LAN
  from the workstation starts it again. That proves Wake-on-LAN over the laptop's Wi-Fi, a before-production item.
  - If the node does not wake, the spec fails and names Wake-on-LAN as the cause; the person presses the power button.
  - It and the power cut stop nodes, so they carry a label that `just test e2e --env production` never selects by
    default (for example `hardware-proof`). Once 12b-2 and 12b-3 hold real data, a routine run against production must
    never power off a node.
- **DNS:** the measurement above, as a spec.
- **fio,** by hand and recorded in the docs, per node:
  - etcd's pattern (`fio --rw=write --ioengine=sync --fdatasync=1 --bs=2300 --size=22m`), whose 99th percentile fdatasync
    must stay under 10 ms;
  - sequential and random 4k throughput on the `rook-data` partition, before Ceph takes it.

  A node over 10 ms is a finding: 12b-2 then decides between etcd's tuning, a second disk, or Ceph's limits before sharing.
- **kube-bench:** `just testbed-scan`'s Job against production, its FAILs against `.cluster-scan-accepted.yaml`.
- **A power cut:** the person switches off the three machines' power strip, and switches it on again. Every node boots by
  itself (Last State), etcd and the API at its VIP come back, and `lab cluster` sees three Ready nodes. Measured: how long.
  Acknowledged writes are checked in 12b-2, with Ceph and a database.

## Docs

- `docs/installation/production/`:
  - the BIOS checklist;
  - the Hyper-V firewall commands;
  - the SSH key;
  - `hosts.json` with `disk`;
  - the DNS field;
  - the fio numbers.
- `docs/concepts/pxe-boot.md`: PXE from WSL (mirrored) and the firewall rules.
- `CLAUDE.md`:
  - 12b-1 done;
  - the before-production items this settles: PXE on a real LAN with a router's DHCP, Wake-on-LAN, UEFI boot order, the
    disk path, kube-bench on hardware;
  - what stays for 12b-2.

## Open

- `homelab-3`'s BIOS: update it, then turn Secure Boot off; if its settings still do not stick, replace the CMOS battery.
  The install waits for it.
- The other two machines' BIOS versions, to update them as well.
