# The nodes' cluster ports, SSH and IPv6 (G2)

Status: designed in chat 2026-11-24, a small change. *(user)* marks what was decided in chat; *(Claude's call)* marks the
builder's own choices, open to review. It settles the threat model's G2
(`docs/superpowers/specs/2026-11-14-threat-model-design.md`), before the first production deploy (12b-3).

## Intent

The nodes share a flat LAN with the family's phones, laptops and IoT devices. Today any of them reaches the cluster's
own ports on every node: etcd (2379, 2380) and the kubelet (10250) ask for client certificates, but flannel's VXLAN
(UDP 8472) is neither encrypted nor authenticated, so a device that sends to it injects packets into the pod network as
if from a pod. This step closes those ports to everything but the nodes (and, for the kubelet, the pods), and closes
two doors that are open by default rather than by choice: SSH's password sign-in and the nodes' IPv6.

## Goals

1. **Cluster ports from the nodes only.** 2379, 2380, 10250 and UDP 8472 accept only the nodes' addresses
   (`k3s.nodeIPs`, from the environment's `hosts.json`); 10250 also accepts the pod network (`10.42.0.0/16`), where
   Prometheus scrapes the kubelet from. They leave `allowedTCPPorts`/`allowedUDPPorts` for rules like the metrics
   ports' (`infra/_modules/metal/flake/profiles/k3s-server.nix`'s `extraCommands`). Inside the cluster, `system/egress`
   already lets only `monitoring-system` reach a node's 10250: the firewall is the layer for what is outside it.
2. **SSH by key only, explicitly.** `PasswordAuthentication` and `KbdInteractiveAuthentication` off and root by key only
   (`PermitRootLogin prohibit-password`). Today only the absence of passwords stops a password sign-in.
3. **No IPv6 on the nodes' LAN interface** *(user)*. `network.nix` takes DHCPv6 and router advertisements (from
   upstream), so a node can hold a global IPv6 address, reachable from wherever the router's IPv6 firewall allows, and
   `allowedTCPPorts` opened every listed port on IPv6 too. Nothing in the cluster uses IPv6 (node addresses, pods
   (`cni.ambient.ipv6: false`), the VIPs, kubeconfigs and SSH are IPv4). The interface stops asking for it and drops
   it; the loopback keeps `::1`, which some programs bind to.
4. **Proven** on the testbed (it gates releases), and by the same specs on production's hardware (label `production`).

## Non-goals

- 6443 (the API server) and SSH open to the whole LAN: limiting them to the workstation needs a fixed admin address,
  which comes with G6.
- 80 and 443 (the Gateway's VIP): the LAN apps' way in.
- Encrypting what crosses the LAN: G12, the next step (measured on the testbed, then Ceph's msgr2 encryption).
- Segmenting the network (G5).

## Anti-goals

- A cluster that works on the testbed but loses something only a port carried: etcd's peers, `kubectl exec`/`logs`
  (the API server to the kubelet), the kubelet's scrape, pods talking across nodes, Ceph.
- Opening the node subnet instead of the node addresses: on a flat LAN that is everyone.

## Constraints

- **Adding a node takes an order** *(Claude's call, stated in the guide)*: the existing nodes learn its address only
  when they are rebuilt, so `hosts.json` gains the node, `lab cluster upgrade` rolls the others, and only then is the new
  node installed and joins etcd. The metrics ports already work this way.
- The firewall stays NixOS's iptables (`networking.firewall`, `extraCommands`), as the metrics ports are.
- `infra/_modules/metal` serves both the testbed and production; the testbed is never run beside the sandbox.

## Approaches

- **Chosen: the node addresses, per port** *(user)*, as the threat model proposed and as the metrics ports already do.
- **Flannel's `wireguard-native` backend as well** *(set aside)*: it would encrypt and authenticate the pod network
  between nodes, but it encrypts the mesh's mTLS a second time and costs the i5s CPU on Ceph's replication; what crosses
  the LAN unencrypted is G12's question, answered by measuring first (Ceph's own encryption covers the sensitive part).
- **The node subnet** *(set aside)*: simpler to add nodes, but the whole flat LAN is that subnet.

## How it is proven

A spec (labels `testbed`, `production`, `proof`), run from the workstation, which on the testbed sits on the VMs' bridge
like any LAN device and on production is one:

- from the workstation, 2379, 2380 and 10250 on each node give no TCP connection, and each node's ruleset accepts
  UDP 8472 only from the node addresses (a VXLAN drop cannot be seen from outside: the rules and their counters on the
  node say it);
- each node has no IPv6 address but `::1`, and `sshd -T` shows password and keyboard-interactive sign-in off;
- the cluster still works through every port it closed: etcd reports every member healthy, `kubectl logs` and `exec`
  reach a pod on each node, a pod reaches another node's kubelet on 10250 (an HTTP answer, 401 or 403, not a timeout), and
  the existing testbed specs pass (`TestbedMesh`: pods across nodes; `TestbedCeph`).

## Left to the builder

- How the IPv6 drop is written for systemd-networkd (`DHCP = "no"`, `IPv6AcceptRA = false`, `LinkLocalAddressing`, a
  sysctl on the interface) and whether the firewall still needs IPv6 rules for what stays open.
- Whether the rules move into a NixOS option or stay in `extraCommands`, and whether the offline render tests of the node
  flake can check them.
- Where the guide says how to add a node (`docs/installation/production` and the testbed's guide).
