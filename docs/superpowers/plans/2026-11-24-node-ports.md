# Node Ports, SSH and IPv6 (G2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The nodes' cluster ports (2379, 2380, 10250, UDP 8472) accept only the nodes (10250 also the pods). SSH signs in by key only. The nodes' LAN interface has no IPv6. All of it is proven on the testbed.

**Architecture:** Three changes to the node flake (`infra/_modules/metal/flake/profiles`), each pinned by an offline evaluation test in `test/offline/metal_test.go`:
- the firewall in `k3s-server.nix`;
- SSH in `configuration.nix`;
- the uplink in `network.nix`.

One new Ginkgo spec, `NodePorts`, proves them on the running nodes from the workstation. A guide section says how to add a node now.

**Tech Stack:** NixOS (`networking.firewall` with iptables `extraCommands`, systemd-networkd, OpenSSH), Go tests (`nix eval`, Ginkgo).

**Spec:** `docs/superpowers/specs/2026-11-24-node-ports-design.md`

## Global Constraints

- Sources: the nodes are `k3s.nodeIPs`, from the environment's `hosts.json`. The pod network is `10.42.0.0/16`.
- 6443, 80 and 443 stay open to the LAN. SSH (22) stays open to the LAN, by key only. Do not limit them: that is G6.
- The firewall stays NixOS's iptables (`networking.firewall`, `extraCommands`), like the metrics ports' rule.
- Run the testbed only from the main checkout `/home/nixos/homelab`, on a `measure/*` branch, never beside the sandbox. Keep at least 30 GB free on `C:`.
- `scripts/ci-check all` passes before the PR.

## Review Focus

1. **Adding a node under the new rules**: the existing nodes refuse the new one's etcd until they are rebuilt with its address. Expected: the guide gives the order (`hosts.json`, then `lab cluster upgrade` of the others, then install). Pinned in Task 2 (the docs step).
2. **A pod scraping a node's kubelet across nodes** (Prometheus on another node). Expected: it gets an HTTP answer. Pinned in Task 2 (the spec's cross-node 10250 check).
3. **The API server reaching each kubelet** (`kubectl logs`, `exec`) once 10250 is limited. Expected: it works on every node. Pinned in Task 2.
4. **A LAN device reaching the nodes over IPv6.** Expected: the uplink has no IPv6 address, link-local included. Pinned in Task 1 (the networkd settings) and Task 2 (`ip -6` on the uplink).
5. **A firewall reload losing the rules.** NixOS rebuilds `nixos-fw` on every reload. Expected: the rules come back with the chain. Pinned by `extraCommands`, which NixOS runs on every start and reload (Task 1), and by Task 2 running after `lab cluster upgrade`'s switch.

---

### Task 1: The node flake: cluster ports, SSH and IPv6

**Files:**
- Modify: `infra/_modules/metal/flake/profiles/k3s-server.nix` (the `networking.firewall` block)
- Modify: `infra/_modules/metal/flake/configuration.nix` (`services.openssh`)
- Modify: `infra/_modules/metal/flake/profiles/network.nix` (`networkConfig` of `10-uplink`)
- Test: `test/offline/metal_test.go`

**Interfaces:**
- Produces: the nodes' rules, which Task 2 checks on the running nodes.
  - Each node address may reach TCP `2379,2380,10250` plus the metrics ports, and UDP `8472`.
  - `10.42.0.0/16` may reach TCP `10250` plus the metrics ports.
  - The listed ports are `6443, 80, 443` (TCP) and none (UDP).
  - sshd sets `passwordauthentication no`, `kbdinteractiveauthentication no` and `permitrootlogin without-password` (sshd -T's spelling of `prohibit-password`).
  - The uplink has no IPv6.

- [ ] **Step 1: Write the failing tests**

Extend `metalNode` and the `apply` expression in `metalEval`:
- `firewall = { tcp = c.config.networking.firewall.allowedTCPPorts; udp = c.config.networking.firewall.allowedUDPPorts; extra = c.config.networking.firewall.extraCommands; };`
- `ssh = c.config.services.openssh.settings;` (decode `PasswordAuthentication`, `KbdInteractiveAuthentication` and `PermitRootLogin`)
- `uplink = c.config.systemd.network.networks."10-uplink".networkConfig;` (decode `DHCP`, `IPv6AcceptRA` and `LinkLocalAddressing` as strings or booleans as Nix gives them)

Tests, each across every fixture node (node IPs `10.0.0.11`, `10.0.0.12`, `10.0.0.13`):

```go
// G2 (docs/superpowers/specs/2026-11-24-node-ports-design.md): the cluster's own ports from the nodes only (the
// kubelet also from the pods, where Prometheus scrapes it); the rest of the LAN keeps 6443, 80 and 443
func TestMetalClusterPortsFromNodes(t *testing.T) {
	for name, n := range metalEval(t) {
		assert.ElementsMatch(t, []int{6443, 80, 443}, n.Firewall.TCP, name)
		assert.Empty(t, n.Firewall.UDP, name)
		for _, ip := range []string{"10.0.0.11", "10.0.0.12", "10.0.0.13"} {
			assert.Regexp(t, `-s `+ip+` -p tcp -m multiport --dports [0-9,]*\b2379,2380,10250\b`, n.Firewall.Extra, name)
			assert.Contains(t, n.Firewall.Extra, "-s "+ip+" -p udp --dport 8472 -j nixos-fw-accept", name)
		}
		pods := regexp.MustCompile(`-s 10\.42\.0\.0/16 -p tcp -m multiport --dports (\S+) `).FindStringSubmatch(n.Firewall.Extra)
		require.NotNil(t, pods, name)
		assert.Contains(t, strings.Split(pods[1], ","), "10250", name)
		assert.NotContains(t, strings.Split(pods[1], ","), "2379", "pods never reach etcd: %s", name)
		assert.NotContains(t, n.Firewall.Extra, "10.42.0.0/16 -p udp", name)
	}
}

func TestMetalSSHKeysOnly(t *testing.T) // PasswordAuthentication false, KbdInteractiveAuthentication false, PermitRootLogin "prohibit-password", on every node

func TestMetalNoIPv6(t *testing.T) // uplink: DHCP "no", IPv6AcceptRA false, LinkLocalAddressing "no", on every node
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd test && direnv exec .. go test -count=1 ./offline -run 'TestMetalClusterPortsFromNodes|TestMetalSSHKeysOnly|TestMetalNoIPv6'`
Expected: FAIL. `TCP` lists 10250, 2379 and 2380; UDP lists 8472; the ssh settings are unset; `DHCP` is `"ipv6"`.

- [ ] **Step 3: Implement**

`k3s-server.nix`:
- `allowedTCPPorts = [ 6443 80 443 ]` and `allowedUDPPorts = [ ]`.
- `extraCommands` adds three kinds of rule:
  - per node address: TCP `--dports 9100,2381,10249,10257,10259,2379,2380,10250`, plus UDP `-p udp --dport 8472`;
  - for `10.42.0.0/16`: TCP `--dports 9100,2381,10249,10257,10259,10250`.
- Comment why: etcd's and the kubelet's client certificates do not cover VXLAN, which is neither encrypted nor authenticated.

`configuration.nix`: set `services.openssh.settings` with `PasswordAuthentication = false`, `KbdInteractiveAuthentication = false` and `PermitRootLogin = "prohibit-password"`.

`network.nix`: set `DHCP = "no"`, `IPv6AcceptRA = false` and `LinkLocalAddressing = "no"`. With no IPv6 configured, networkd sets `disable_ipv6` on the link itself. Add a comment on why, pointing at G2.

- [ ] **Step 4: Run the tests and the whole metal set**

Run: `cd test && direnv exec .. go test -count=1 ./offline -run 'TestMetal|TestProductionConfig'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add infra/_modules/metal/flake/profiles/k3s-server.nix infra/_modules/metal/flake/configuration.nix infra/_modules/metal/flake/profiles/network.nix test/offline/metal_test.go
git commit -m "feat(metal): the cluster's ports from the nodes only, SSH by key, no IPv6 on the uplink"
```

### Task 2: Proven on the nodes, and the guide

**Files:**
- Create: `test/e2e/node_ports_test.go`
- Modify: `docs/installation/testbed.md` and `docs/installation/production/deployment.md` (adding a node)
- Modify: `CLAUDE.md` (the "Before the first production deploy" note), `docs/superpowers/deferred-minors.md` if anything is deferred

**Interfaces:**
- Consumes: Task 1's rules, and `metalHosts()`, `testbedSSH(ctx, ip, command)`, `waitNodeReady` and `kube.Kubectl` (`test/e2e/testbed_test.go`).

- [ ] **Step 1: Write the spec `NodePorts`** (`Label("testbed", "production", "proof")`, `Ordered`), with these items:
- **"the LAN reaches only what stays open":** for every node, `net.DialTimeout("tcp", ip+":6443", 5s)` succeeds (the control), and dialling `:2379`, `:2380` and `:10250` fails.
- **"VXLAN only from the nodes":** on every node, every line of `iptables -S nixos-fw` that names `8472` carries `-s <a node IP>/32`, and there is one such line per node.
- **"no IPv6 on the uplink":** on every node, `ip -o -6 addr show scope global` is empty, and `ip -o -6 addr show dev <uplink>` is empty. The uplink is the interface holding the node's IPv4 address: `ip -o -4 addr show | awk -v ip=<ip> '$4 ~ "^"ip"/" {print $2}'`.
- **"SSH by key only":** `sshd -T` on every node shows `passwordauthentication no`, `kbdinteractiveauthentication no` and `permitrootlogin without-password`.
- **"etcd's members are healthy":** `etcdctl endpoint health --cluster` on one node, with k3s's client certificates as `TestbedSecretsEncrypted` passes them, reports `is healthy` once per node.
- **"the API server and the pods reach every kubelet":**
  - in a throwaway namespace `node-ports-test` (deleted in `DeferCleanup`), one pod per node (`nodeName`), image `docker.io/curlimages/curl:8.23.0` (Argo Rollouts' `images.curl`), non-root, command `sleep 3600`;
  - `kubectl logs` and `kubectl exec -- curl -sk -o /dev/null -w '%{http_code}' https://<next node's IP>:10250/healthz` each succeed;
  - the code is `401` or `403`: an answer, not a timeout.

- [ ] **Step 2: Vet the spec and check the scope**

Run: `cd test && direnv exec .. go vet ./e2e && cd ../cli && direnv exec .. go run ./cmd/lab test scope --check`
Expected: no output from vet, and the scope check passes. The labels already exist; the spec falls under the metal path group.

- [ ] **Step 3: Write the guide**

Write a short "Adding a node" in `docs/installation/testbed.md` and `docs/installation/production/deployment.md`:
- add the node to `hosts.json`;
- `lab cluster upgrade` the existing nodes, so they accept its address;
- only then install and enroll it.

Also add a line to CLAUDE.md's "Before the first production deploy": the ports, SSH and IPv6 were proven on the testbed, and on the hardware only by `just test e2e --env production --labels proof` after the rollout.

- [ ] **Step 4: Run it on the testbed**

From the main checkout:
```bash
git -C /home/nixos/homelab fetch -q /home/nixos/homelab-np docs/node-ports-design
git -C /home/nixos/homelab checkout -B measure/node-ports FETCH_HEAD
just testbed
```

Expected:
- `lab cluster upgrade` switches every node;
- `NodePorts` passes, with the other `proof` specs (`TestbedNodes`, `TestbedMesh`, `TestbedCeph`, …);
- the mark is written.

If the SSH key is not loaded, `scripts/ssh-key-loaded` stops the run. The user loads it (`ssh-add`); never handle the passphrase.

- [ ] **Step 5: Commit**

```bash
git add test/e2e/node_ports_test.go docs/installation/testbed.md docs/installation/production/deployment.md CLAUDE.md
git commit -m "test(metal): NodePorts proves the cluster's ports, SSH and IPv6 on the nodes"
```

- [ ] **Step 6: Basic checks**

Run: `scripts/ci-check all` in `/home/nixos/homelab-np`, with `direnv exec .`.
Expected: exit 0.
