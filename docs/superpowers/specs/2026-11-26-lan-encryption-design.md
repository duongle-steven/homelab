# What crosses the LAN, encrypted (G12)

Status: designed in chat 2026-11-26. *(user)* marks what was decided in chat; *(Claude's call)* marks the builder's own
choices, open to review. The threat model's G12 (`docs/superpowers/specs/2026-11-14-threat-model-design.md`), and what
G2's final review left to it.

## Intent

The nodes share a flat LAN with the family's devices. G2 closed the cluster's ports to everything but the nodes. It does
not stop two things:

- **A device listening.** Inside the mesh, pods talk mTLS (HBONE). Everything else crosses the LAN as it is: Ceph (OSD
  to OSD, and every volume's reads and writes from the nodes' kernel RBD client), pods to CoreDNS, CloudNativePG's
  operator to its instances.
- **A device forging a node's address.** flannel's VXLAN is UDP and unauthenticated: a packet to UDP 8472 from a node's
  address passes G2's rule, and flannel delivers it into the pod network.

This has to happen before 12b-3 *(user)*. Once Ceph holds the family's data, changing how the nodes carry pod traffic
means downtime on a cluster that matters.

Priorities *(user, 2026-11-26)*:

- security first;
- no work that duplicates another step: no traffic encrypted twice where it can be helped, and no overlap with G5's
  segmentation;
- the shortest development and test loop.

## Decisions

- **flannel's `wireguard-native` backend, not Ceph's msgr2 encryption** *(user, 2026-11-26)*.
  - Ceph runs on the pod network, so its OSD traffic and the RBD client both cross the nodes inside flannel. WireGuard
    covers them, and msgr2 on top would encrypt Ceph twice.
  - WireGuard authenticates each packet with the sending node's key, so a forged node address no longer reaches the pod
    network.
  - It also covers the other flows outside the mesh: CoreDNS, CloudNativePG, anything added later.
  - The mesh's traffic between nodes is encrypted twice (HBONE inside WireGuard). This is accepted: apps' traffic is
    small next to Ceph's, and the cost is measured.
  - Same backend, same flannel: k3s's flag `--flannel-backend=wireguard-native`, with the kernel's WireGuard (the nodes
    run 7.0).
- **Measure first and after** *(user, 2026-11-24)*.
  - `tcpdump` between the testbed's nodes lists every flow that crosses, before and after the change.
  - Each flow left in clear is accepted with its reason or fixed.
- **G5 is not this step**. Segmentation decides who reaches the nodes at all: SSH, 6443, the LAN Gateway, and a
  compromised node's reach into the home. Encryption does not. Neither undoes the other.

## Goals

1. **Before: what crosses.** On the testbed with layers 1 and 2 running, capture what crosses between the nodes and
   from the nodes to the rest of the LAN.
   - Use `tcpdump` on each node's uplink, during `just testbed`'s suite.
   - Write a table of every flow: source, destination, port, encrypted or not, what it is.
   - The table goes in `docs/reference/architecture/networking.md`. The raw capture stays out of the repository.
2. **The backend.**
   - `infra/_modules/metal`'s k3s server profile sets `--flannel-backend=wireguard-native`.
   - Its firewall accepts UDP 51820 only from the nodes' addresses, as G2 does for its ports. UDP 8472 is closed.
   - The sandbox (one k3d node, no traffic between nodes) keeps k3s's default VXLAN.
3. **After: what crosses.** The same capture again.
   - Pod traffic between nodes must show only UDP 51820.
   - Each remaining flow in clear is in the table with its reason or its fix: the metrics ports on the host network,
     the nodes' DNS to the router until LAN DNS, and whatever the capture finds.
4. **The cost** *(user, 2026-11-26)*.
   - **The gate is on production's hardware.** Production runs no Ceph yet (12b-2 has not put it there), so the gate
     is the path Ceph's replication would take *(user, 2026-11-26)*: TCP between two pods on two nodes, by pod IP, and
     the nodes' CPU, measured with `iperf3` (`scripts/node-iperf`):
     - once on the running production cluster before its reinstall (VXLAN);
     - once after the reinstall (WireGuard);
     - the same job both times.
   - If the throughput drops by more than half, stop and ask the user before anything goes further.
   - `fio` through Ceph (`scripts/ceph-fio`) runs on the testbed before and after, and on production once 12b-2 puts
     Ceph there (WireGuard's number only).
   - **The testbed's numbers are a comparison only.** The three VMs share one workstation, so WireGuard is limited by
     CPU there, not by a 1 GbE link as on the hardware.
   - If the testbed misses its bar with WireGuard, stop and report to the user with the numbers next to the current
     runs. Do not accept it as the testbed's limit alone.
   - Both sets of numbers are recorded: the hardware's in `docs/installation/production/deployment.md`, the testbed's
     in `docs/installation/testbed.md`.
5. **Production reinstalled, not switched** *(user, 2026-11-26)*.
   - k3s changes the backend only when every server has it and every node restarts. A mixed cluster splits the pod
     network.
   - Production's three nodes run VXLAN today (12b-1, 12b-2) and hold no real data. They are reinstalled from scratch
     with the new backend, rather than given a switch procedure.
   - Switching a cluster that holds data is not designed now. If it is ever needed, it is designed and tested then.
   - How a recorded machine is reinstalled (`hosts.json` keeps nixie from reinstalling one) is written in
     `docs/installation/production/deployment.md`.
   - `lab cluster upgrade` refuses a backend change node by node *(Claude's call)*. Until the reinstall, a
     `just infra deploy production` would otherwise roll the new backend one node at a time onto production.
6. **Proven.**
   - Offline: the flag, the firewall rule (51820 from the nodes only, no 8472, every accept with a source as G2's test
     has it).
   - On the testbed:
     - `NodePorts` updated: 51820 refused from outside the nodes, 8472 gone.
     - A new spec checks every node has interface `flannel-wg` with a peer for each other node, and no `flannel.1`.
     - The suite passes on a fresh install, against the testbed's bar.
   - On production after its reinstall: `just test e2e --env production --labels proof` (`NodePorts` and the new spec
     included), and the `iperf3` gate above.
7. **Docs.**
   - The threat model's G12 says what is covered and what is accepted.
   - `networking.md` holds the flow table.
   - CLAUDE.md's "Before the first production deploy" gets the backend, proven on the testbed's VMs only.

## Non-goals

- Ceph's msgr2 encryption: it would encrypt Ceph a second time.
- TLS for the metrics ports on the host network (node-exporter 9100, kube-proxy 10249, etcd's 2381). They carry no
  secrets, and TLS would need a proxy in front of each exporter. Accepted, with the reason in the table.
- The nodes' own DNS to the router: that is LAN DNS's step.
- Segmentation (G5), and limiting SSH and 6443 to an admin address (G6).
- The sandbox: one node, nothing crosses between nodes.

## Anti-goals

- A flow in clear that nobody wrote down: the after-capture is the list, not the threat model's guess.
- UDP 51820 open to the LAN: it takes only the nodes' addresses, like G2's ports.
- Production's reinstall started before its before-numbers are recorded.

## Constraints

- k3s's flannel options must be the same on every server; all three nodes are servers.
- WireGuard keys are flannel's own: by default a private key in `/run/flannel/wgkey` (tmpfs, so a new one at each
  boot), its public half published in the Node's annotation `flannel.alpha.coreos.com/backend-data`, from which the
  other nodes learn it; no preshared key. So nothing goes in OpenBao or the rotation catalog, and a reinstalled node
  needs no step (the builder check, from flannel's design; confirmed on the testbed while building). If a key turns out
  to live where the repository makes it, it needs a catalog entry, as every secret does.
- Only traffic to the pod network (10.42.0.0/16) goes through WireGuard. Traffic between the nodes' own addresses stays
  on the uplink as today: etcd, the kubelet and 6443 (TLS), and pods scraping metrics on another node's address
  (masqueraded, plain). The after-capture lists them.
- Pods' MTU drops from 1450 to 1420. Every pod is recreated when the nodes restart, so none keeps the old one.
- `lab cluster upgrade`'s refusal compares the backend the node runs with the one it would build, and says to reinstall.
- The testbed's bar is #113's (the suite passes, then 30 minutes with no restart once settled), on 3 GB, 4-vCPU VMs.
  One fresh install with the suite takes about 1h45. The plan should need two at most, the
  before and after captures riding on runs that happen anyway.

## Approaches

- **Chosen: flannel `wireguard-native`.** One flag. Every flow outside the mesh is covered and authenticated, and Ceph
  is encrypted once. The mesh's traffic is encrypted twice.
- **Ceph's msgr2 `secure`** *(set aside)*. Ceph only. It leaves the forged-address injection, CoreDNS and
  CloudNativePG in clear. It was the 2026-11-24 proposal, before G2's review showed the injection.
- **Both** *(set aside)*: Ceph encrypted twice, for nothing WireGuard misses.
- **Cilium with its own WireGuard** *(set aside, user's question 2026-11-26)*: the same encryption, plus replacing the
  CNI, kube-proxy and the network policies. Istio's identity mTLS stays the mesh either way. A rewrite of the network for
  no security gain here.
- **G5 segmentation instead** *(set aside)*: new hardware, and a device on the homelab's own segment (or a compromised
  node) could still listen.

## Left to the builder

- How the captures run (a script under `scripts/`, run by hand next to the suite), and how flows are grouped in the table.
- The `fio` job's size and pattern, within the testbed's memory.
- How production is reinstalled (the `hosts.json` records and the disks), as documented steps.
