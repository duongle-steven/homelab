# Deployment

Open the development shell if you haven't already:

```sh
nix develop
```

## The nodes, the first time

`infra/production/metal` installs the machines of `infra/production/metal/hosts.json` the first time (`nixie` wakes
them by Wake-on-LAN, boots them from the network and installs each with `nixos-anywhere`; [PXE boot](../../concepts/pxe-boot.md)),
joins them into one k3s cluster (`lab cluster enroll`), writes `infra/production/kubeconfig.yaml`, then upgrades nodes
already installed in place, one at a time (`lab cluster upgrade`). Before the first run:

1. Make production's own SSH key, with a passphrase, and load it; its public half goes in
   `infra/production/metal/authorized_keys` (commit it):

    ```sh
    ssh-keygen -t ed25519 -f ~/.ssh/homelab-production -C homelab-production
    ssh-add ~/.ssh/homelab-production
    ```

2. Under WSL (mirrored networking), let `nixie`'s ports in: without these rules WSL sees no broadcast from the LAN. In an
   administrator PowerShell:

    ```powershell
    New-NetFirewallHyperVRule -Name nixie-pxe-udp -DisplayName "nixie PXE (UDP)" -Direction Inbound -VMCreatorId '{40E0AC32-46A5-438A-A0B2-2B479E8F2E90}' -Protocol UDP -LocalPorts 67,69,4011
    New-NetFirewallHyperVRule -Name nixie-pxe-tcp -DisplayName "nixie PXE (TCP)" -Direction Inbound -VMCreatorId '{40E0AC32-46A5-438A-A0B2-2B479E8F2E90}' -Protocol TCP -LocalPorts 80,5000
    ```

3. Log in to Cloudflare for production's state (`cf auth login`, Account API Tokens enabled on its consent page).
4. Switch the machines off: `nixie` wakes them.

Then, with your machine's LAN address:

```sh
cd infra/production/metal
HOMELAB_PXE_ADDRESS=192.168.0.224 terragrunt apply
```

`nixie` writes each machine's `machine_id_hash` into `hosts.json`: **commit it**. A recorded machine is never installed
again, and once every machine is recorded the hook neither asks for `sudo` nor needs `HOMELAB_PXE_ADDRESS`, so `just
infra deploy production` runs it every time.

To reinstall a node:

1. Drain it and take it out of the cluster (`kubectl drain <node> --ignore-daemonsets --delete-emptydir-data`, then
   `kubectl delete node <node>`). Its etcd member leaves too: k3s removes a deleted server's member.
2. Power it off: `nixie` wakes only machines that are off.
3. Delete its `machine_id_hash` in `hosts.json` (the key, not just the value), commit.
4. Forget its old host key: `ssh-keygen -R <address>`.
5. Apply as for the first install, with the firewall rules and `HOMELAB_PXE_ADDRESS`.

If the trust step stops with "no SSH from …", it prints ssh's own error: a changed host key needs step 4, and
"Permission denied" a key missing from the agent (`ssh-add ~/.ssh/homelab-production`).

Afterwards, remove the firewall rules:

```powershell
Remove-NetFirewallHyperVRule -Name nixie-pxe-udp, nixie-pxe-tcp
```

The nodes' host keys were trusted on first contact (`scripts/testbed-trust`, for `hosts.json`'s addresses only). To
check one, compare `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` on the machine's console with
`ssh-keygen -lF <address>` on yours.

Then prove the nodes:

```sh
just test e2e --env production --labels proof          # etcd, Secrets encrypted, seccomp
just test e2e --env production --focus NodesResolveQuickly
just test e2e --env production --labels scan           # kube-bench
scripts/node-fio <address>                              # once per node, before Ceph takes rook-data
just test e2e --env production --labels hardware-proof --verbose  # powers nodes off: never once real data is there
```

`--labels hardware-proof` runs `ProductionVIPMoves` (a node powered off, woken by Wake-on-LAN) and `ProductionPowerCut`
(you switch the power strip off and on). A routine `just test e2e --env production` never runs them, nor kube-bench.

### Reinstalling every node

Some changes k3s takes only on every server at once, such as flannel's backend (G12: `lab cluster upgrade` refuses it
node by node, since a mixed cluster splits the pod network). On a cluster with no data, reinstall all of them rather
than switch, as G12 did on 2026-11-27:

1. Measure what the change could cost first (for G12, `scripts/node-iperf production`).
2. Power every node off (`ssh root@<address> systemctl poweroff`): `nixie` wakes only machines that are off. There is no
   drain or `kubectl delete node`: the whole cluster goes.
3. Delete every `machine_id_hash` in `hosts.json` (the keys), commit.
4. Forget their host keys: `ssh-keygen -R <address>` for each.
5. Apply as for the first install, with the firewall rules and `HOMELAB_PXE_ADDRESS`. `lab cluster enroll` starts the
   cluster on `init_host` and joins the others; the kubeconfig is written again. Commit the new `hosts.json`.
6. Prove the nodes as above.

A cluster holding data is not reinstalled this way: its switch is designed and tested when one is needed.

### Measured on the hardware (2026-11-11)

Three HP EliteDesk Minis (one 800 G4, two 800 G3), one 256 GB NVMe each, on a TP-Link Archer C64:

- **Install:** the three in parallel in about 3 minutes, from `nixie` waking them to the record, over the laptop's Wi-Fi.
- **DNS** through the Archer (DoH): an NXDOMAIN and an AAAA-less name in 110–140 ms on the nodes, 60–110 ms in a pod.
  Through `8.8.8.8` on UDP, the ISP modem's interception made the NXDOMAIN take 5 s.
- **fio, etcd's pattern** (p99 fdatasync, budget 10 ms): `homelab-2` and `homelab-3` (Samsung MZAL8256HDJD) 5.1–5.7 ms;
  `homelab-1` (SK hynix BC901) 10.7, 16.4 and 14.1 ms in three runs, over the budget every time. Its etcd's own WAL
  fsync averaged about 4 times the others'. A finding for step 12b-2.
- **fio on rook-data** (173.5 GB per node): about 2.5 GiB/s sequential write, 3 GiB/s sequential read, 185–210k random 4k
  write IOPS and 260–277k random read IOPS on every node.
- **kube-bench:** no finding outside `.cluster-scan-accepted.yaml`.
- **The VIP:** the API answered through it 3–4 s after its holder powered off; Wake-on-LAN brought the node back in
  2m43s (it tries the network first, finds no `nixie`, then boots its NVMe).
- **A power cut** of the three nodes and the Archer together: every node booted by itself and the API answered at its
  VIP 1m11s after it went. Data written before a cut is checked in 12b-2, with Ceph.

### WireGuard between the nodes (2026-11-27)

G12 (`docs/reference/architecture/networking.md`, "What crosses the LAN"): the three nodes reinstalled from VXLAN to
flannel's `wireguard-native` as above, in about 7 minutes from the apply to three Ready nodes. `scripts/node-iperf
production` (iperf3 between pods on `homelab-1` and `homelab-2`, 30 s with 4 streams each way), the same job before and
after:

| | VXLAN | WireGuard |
| --- | --- | --- |
| `homelab-2` to `homelab-1` | 810 Mbit/s | 792 Mbit/s |
| `homelab-1` to `homelab-2` | 898 Mbit/s | 876 Mbit/s |
| CPU busy, the two nodes | 11–14% | 14–17% |

About 2% of the link and 3 points of CPU: the 1 GbE link, not the CPU, limits it here, unlike the testbed's VMs
(`docs/installation/testbed.md`). Afterwards `NodePorts`, `NodeWireGuard`, `TestbedNodes`, `TestbedSecretsEncrypted` and
`TestbedSeccomp` passed (`--labels proof --focus ...`); `TestbedCeph` and `NodeMeshAgents`, also in `proof`, need
12b-2's Ceph and Istio and fail until then.

## Adding a node

The nodes accept etcd, the kubelet and flannel's WireGuard only from each other's addresses (G2,
`docs/superpowers/specs/2026-11-24-node-ports-design.md`), and each node learns those addresses when it is built. Add the
node to `infra/<env>/metal/hosts.json` (its MAC and address), commit, and run `just infra deploy production`: the metal unit installs it
(nixie), then rolls the installed nodes with `lab cluster upgrade` so their firewalls accept its address (the new node,
built from the same flake, is current and skipped), and only then enrolls it. `just infra deploy production` is the
whole release gate (`lab infra forgejo verify`: `origin/master`, its checks and the release proof); between releases,
`just infra deploy production --force` applies only. Run by hand in another order, the new node cannot reach the
others' etcd: k3s may already have added it as an etcd member (a learner) before peer traffic fails, so check
`etcdctl member list` on another node and remove a member that never started before trying again. A run that failed
after the install but before the enroll is safe to repeat: `lab cluster upgrade` rebuilds a node that was never
enrolled without draining it.

## The rest

Build the lab:

```sh
just infra deploy production
```

It runs the nodes' unit above (nothing to install once every machine is recorded), then the rest of the deploy below.

Yes it's that simple!

!!! note

    `just infra deploy production` deploys. It refuses anything but `origin/master` checked out without changes whose
    check `checks` passed and whose pull request's check `sandbox` passed (`lab infra forgejo verify`), applies
    `infra/production`, provisions Forgejo's org `ops` (`lab infra forgejo sync`: the users `deploy` and `argocd`,
    `master`'s protection, their tokens in OpenBao), pushes the commit to `ops/homelab` (`lab infra forgejo push`),
    then applies the Argo CD bootstrap again. Argo CD does not manage itself, so a change to `system/argocd` reaches
    the cluster this way too. The first install is the same command: Argo CD reads the seed repository until its
    read-only token reads `ops/homelab`'s `master`, then switches to Forgejo and never back (not even while Forgejo
    restarts). `lab infra forgejo push` alone deploys without touching the infrastructure. Only `deploy` can push to
    `master`, and nobody can force-push or delete it, Forgejo's site admins (`forgejo_admin`, `homelab-admins`)
    included; a site admin can still edit or remove the branch protection itself, a deliberate bypass the next
    `lab infra forgejo sync` (every `just infra deploy production`) undoes. Roll back with a revert through a pull
    request. `--force` applies the infrastructure only and deploys
    nothing (a state migration from another commit).

!!! example

    <script id="asciicast-xkBRkwC6e9RAzVuMDXH3nGHp7" src="https://asciinema.org/a/xkBRkwC6e9RAzVuMDXH3nGHp7.js" async></script>

It will take a while to download everything,
you can read the [architecture document](../../reference/architecture/overview.md) while waiting for the deployment to complete.
