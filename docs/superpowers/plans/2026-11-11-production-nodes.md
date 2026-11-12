# Production's Nodes (12b-1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** NixOS and one k3s cluster of three servers on the three HP mini PCs, installed from the network, and the hardware
proofs: DNS, fio, kube-bench, the VIP moving with Wake-on-LAN, a power cut.

**Architecture:**
- The node flake reads two new `config.json` fields, `disk` and `network.dns`.
- `infra/production/metal` mirrors `infra/testbed/metal`, but `nixie` writes its record into the committed `hosts.json`
  (upstream's way).
- The testbed's node specs also run on production. Two new specs, which stop nodes, carry a label `hardware-proof` that a
  routine production run never selects.
- The install and the proofs are run by hand with the person, who provides the SSH key, the Hyper-V firewall rules,
  Cloudflare's login and the power strip.

**Tech Stack:**
- Nix (the node flake) and Terragrunt/OpenTofu (the metal unit);
- `nixie` and `nixos-anywhere`, `lab cluster`;
- Ginkgo (`test/e2e`), testify (`test/offline`), just.

**Spec:** `docs/superpowers/specs/2026-11-11-production-nodes-design.md`

## Global Constraints

- **Nodes:**

  | Node | IP | MAC |
  |---|---|---|
  | `homelab-1` (`init_host`) | `192.168.0.11` | `38:22:e2:2c:dc:35` |
  | `homelab-2` | `192.168.0.12` | `10:62:e5:16:d2:ca` |
  | `homelab-3` | `192.168.0.13` | `10:e7:c6:0f:63:a0` |

  The API's VIP is `192.168.0.10`.
- **Disk:** `/dev/nvme0n1` for every production node, from `config.json`'s `disk`. Never in `hosts.json`: `nixie` drops
  unknown fields.
- **DNS:**
  - production's `config.json` gets `"network": {..., "dns": ["192.168.0.1"]}`;
  - without `network.dns`, the nodes keep `8.8.8.8` and `1.1.1.1`, so the testbed is unchanged;
  - budget: an NXDOMAIN and an AAAA-less name answer in under 1 s from a node and from a pod.
- **Install record:** `nixie` writes `machine_id_hash` into `infra/production/metal/hosts.json`, and it is committed.
  The testbed keeps `.state/testbed/` unchanged.
- **Host keys:** trusted on first contact for `hosts.json`'s addresses only (`scripts/testbed-trust`, reused).
- **Spec label:** `hardware-proof` marks specs that power off nodes. `just test e2e --env production` with no `--labels`
  runs `production && !hardware-proof && !scan`.
- **fio:** etcd's pattern `--rw=write --ioengine=sync --fdatasync=1 --bs=2300 --size=22m`, with a p99 fdatasync budget of
  10 ms.
- **Never:**
  - install a MAC not in `hosts.json`;
  - leave the Hyper-V rules after the install;
  - run `just dev` beside the testbed or with less than 30 GB free on C:;
  - commit to `dev` or `master`.
- **Commits** end with the session's `Co-Authored-By` and `Claude-Session` lines.

## Review Focus

1. **A routine `just test e2e --env production` never powers off a node.**
   - Expect the default filter to exclude `hardware-proof` and `scan`, and `--labels` to still allow running them on
     purpose.
   - Task 3 pins it with a dry-run (`ginkgo --dry-run`).
2. **A recorded host is never reinstalled.**
   - Expect `nixie` to get the repository's `hosts.json` itself, never a copy.
   - Expect the record committed after the install.
   - Task 2 pins it with an offline test on the unit's hook arguments.
3. **A node's own `disk` still wins over `config.json`'s, and the testbed's VMs stay on `/dev/sda`.** Task 1's fixture
   covers both.
4. **Wake-on-LAN from WSL over Wi-Fi may not reach a wired node.**
   - Expect the VIP spec to fail and name Wake-on-LAN.
   - Expect a woken node with no `nixie` running to fall back to its NVMe.
   - Task 5 runs it on the hardware.
5. **The power cut proves the nodes, not data.** Expect the docs not to claim acknowledged writes survive (12b-2 does);
   Task 6 words it.

---

### Task 1: The flake reads the disk and DNS from `config.json`

**Files:**
- Modify: `infra/_modules/metal/flake/default.nix` (pass `cluster.disk` and `cluster.network` into specialArgs as
  `defaultDisk` and `network`).
- Modify: `infra/_modules/metal/flake/disks.nix`: `device = hostConfig.disk or defaultDisk or "/dev/sda"`.
- Modify: `infra/_modules/metal/flake/profiles/network.nix`: `dns = network.dns or [ "8.8.8.8" "1.1.1.1" ]`.
- Modify: `infra/production/cluster/config.json`: add `"init_host": "homelab-1"`, `"disk": "/dev/nvme0n1"`, and
  `"dns": ["192.168.0.1"]` under `network`.
- Modify: `infra/_modules/metal/variables.tf`: the description of `hosts` no longer names `disk`.
- Modify: `docs/installation/production/configuration.md`: the `hosts.json` example without `disk`; `disk` and
  `network.dns` explained.
- Test: `test/offline/metal_test.go`.

**Interfaces:**
- Produces:
  - `config.json` fields `disk` (string) and `network.dns` (list of strings);
  - a `metalNode` field `DNS []string` (JSON `dns`) in the evaluated fixture.

- [ ] **Step 1: Write the failing tests.**
  - In `metalEval`'s fixture:
    - `node-a` keeps `"disk": "/dev/vda"`; `node-b` and `node-c` lose theirs;
    - `cluster.json` gets `"disk": "/dev/nvme0n1"` and `"network": {..., "dns": ["10.0.0.1"]}`;
    - the `--apply` expression adds `dns = c.config.systemd.network.networks."10-uplink".dns;`.
  - `TestMetalDisk` asserts `node-a` is `/dev/vda`, and `node-b` and `node-c` are `/dev/nvme0n1`.
  - `TestMetalDNS` asserts every node's `DNS` equals `["10.0.0.1"]`.
  - `TestMetalDefaults` (new) evaluates one more copy with `cluster.json` without `disk` and `dns`. It asserts
    `/dev/sda` and `["8.8.8.8", "1.1.1.1"]` (the testbed's case).
  - `TestProductionConfig` (new, in `test/offline/metal_test.go`) reads `infra/production/cluster/config.json` and asserts
    `init_host == "homelab-1"`, `disk == "/dev/nvme0n1"` and `network.dns == ["192.168.0.1"]`.
- [ ] **Step 2: Run** `direnv exec . go -C test test -count=1 ./offline -run 'TestMetal|TestProductionConfig'`.
  Expected: FAIL on the disk, DNS and production assertions.
- [ ] **Step 3: Implement** the files above.
- [ ] **Step 4: Run** the same tests, then `direnv exec . scripts/check-env-config`. Expected: PASS. The new keys need
  no chart.
- [ ] **Step 5: Commit** `feat(metal): the disk and the nodes' DNS from config.json; production's names the Archer`.

### Task 2: The unit `infra/production/metal`

**Files:**
- Create: `infra/production/metal/terragrunt.hcl`, modelled on `infra/testbed/metal/terragrunt.hcl`. The differences:
  - no `vms` dependency;
  - the `nixie` hook runs `scripts/metal-nixie` with `${get_terragrunt_dir()}/hosts.json` itself;
  - `--address` is the workstation's LAN address, from `HOMELAB_PXE_ADDRESS` (required; the hook fails with a message
    when it is unset);
  - `trust` reuses `scripts/testbed-trust`, its comment updated to cover production's install;
  - the `enroll`, `kubeconfig` and `upgrade` hooks use `--environment production` and `infra/production/kubeconfig.yaml`.
- Create: `infra/production/metal/hosts.json` with the three hosts of Global Constraints (`mac_address`, `ip`).
- Create: `scripts/metal-nixie <hosts.json> <nixie args…>`: `sudo -v`, then `sudo "$(command -v nixie)" --hosts <hosts.json>
  "$@"`. No copy, no Wake-on-LAN listener: `nixie`'s own Wake-on-LAN wakes real machines.
- Modify: `infra/production/argocd/terragrunt.hcl`: `dependencies { paths = ["../metal"] }`, as the testbed's argocd.
- Modify: `infra/production/metal/authorized_keys`: the person's new production public key (Task 4's step 1; until then
  the task leaves the placeholder).
- Test: `test/offline/metal_test.go`.

**Interfaces:**
- Consumes: Task 1's `config.json` fields.
- Produces:
  - `infra/production/metal/hosts.json`;
  - `HOMELAB_PXE_ADDRESS`;
  - `scripts/metal-nixie`.

- [ ] **Step 1: Write the failing test `TestProductionMetalUnit`.**
  - It reads `infra/production/metal/hosts.json` and asserts exactly the three hosts of Global Constraints, with no
    `disk` key.
  - It reads `terragrunt.hcl` and asserts the `nixie` hook passes `get_terragrunt_dir()}/hosts.json` (the committed file,
    not a `.state/` copy) and the hooks name `--environment production`.
  - It reads `infra/production/argocd/terragrunt.hcl` and asserts it depends on `../metal`.
- [ ] **Step 2: Run** it. Expected: FAIL (no unit).
- [ ] **Step 3: Implement** the files above. Run `shellcheck scripts/metal-nixie` and `direnv exec . just infra check`
  (`hcl validate` and `fmt`).
- [ ] **Step 4: Run** the test and `TestMetalKeysPerEnvironment`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(metal): infra/production/metal: nixie records installs in the committed hosts.json`.

### Task 3: The specs run on production, and stop nodes only when asked

**Files:**
- Modify: `test/e2e/testbed_test.go`.
  - `testbedHosts()` becomes `metalHosts()`, which reads `infra/<metalEnv()>/metal/hosts.json`.
  - `metalEnv()` returns `testbed` when `ENV=testbed`, and `production` when ENV is empty (`just test e2e --env
    production`).
  - Callers are renamed.
- Modify: `Describe("TestbedNodes")`, `Describe("TestbedSecretsEncrypted")` and `Describe("TestbedSeccomp")` take the
  label `production` too.
- Modify: `test/e2e/scan_test.go`: `ScanBench` takes `production`.
- Create: `test/e2e/production_test.go`:
  - `Describe("NodesResolveQuickly", Label("production"))` (production only: the testbed's VMs also query `8.8.8.8` through NAT and may meet the same interception, a deferred minor, not this step's). On every node over SSH, and from a busybox
    pod in a scratch namespace, it times `nslookup` of `nx-<random>.example.com` (NXDOMAIN) and of `ipv4only.arpa`'s AAAA.
    Each answers in under 1 s. The time is measured inside the node or pod (`date +%s%N` before and after), not around
    SSH.
  - `Describe("ProductionVIPMoves", Label("production", "hardware-proof"), Serial)`.
    - It finds the VIP's holder (lease `plndr-cp-lock`) and records its `bootID`.
    - It runs `systemctl poweroff` over SSH and waits up to 2 minutes for `/readyz` through the VIP, and for the holder
      to change.
    - It then sends Wake-on-LAN to the stopped node's MAC with `wakeOnLAN(mac string) error`: a magic packet to UDP
      `255.255.255.255:9`.
    - It waits up to 10 minutes for a new `bootID` and Ready.
    - On a timeout the message names Wake-on-LAN and asks the person to press the button.
  - `Describe("ProductionPowerCut", Label("production", "hardware-proof"), Serial)`.
    - It records every node's `bootID` and prints "switch the power strip off, wait 30 s, switch it on".
    - It then waits up to 30 minutes for every node to report a new `bootID` and Ready, and for `/readyz` through the VIP.
    - It prints the duration from the first API failure to all Ready.
- Modify: `test/justfile` `e2e`: for `production` with no `--labels`, the filter is
  `production && !hardware-proof && !scan`.
- Modify: `test/scopes.yaml`: add label `hardware-proof` to `labels:`.
- Test: the dry runs below, and `TestE2EKeepsSelection`.

**Interfaces:**
- Consumes: Task 2's `hosts.json`.
- Produces:
  - `metalHosts() (names []string, ips map[string]string)`;
  - `metalMACs() map[string]string`;
  - `wakeOnLAN(mac string) error`.

- [ ] **Step 1: Write the failing check.**
  - The `e2e` recipe's filter computation moves into a recipe `e2e-filter env labels`, which prints the label filter and
    which `e2e` calls.
  - `TestProductionDefaultFilter` (in `test/offline`, beside the e2e selection tests) runs `just test e2e-filter
    production ""` and `just test e2e-filter production hardware-proof`. It asserts the first prints
    `production && !hardware-proof && !scan` and the second `(hardware-proof) && production`.
  - It then lists each filter's specs with `go tool ginkgo --dry-run -v --label-filter=<filter> ./e2e`:
    - `ProductionVIPMoves` and `ProductionPowerCut` appear only in the second;
    - `TestbedNodes` and `NodesResolveQuickly` appear in the first.
- [ ] **Step 2: Run** it. Expected: FAIL.
- [ ] **Step 3: Implement** the files above. `go vet ./e2e`.
- [ ] **Step 4: Run** `TestProductionDefaultFilter` and `TestE2EKeepsSelection`, then `just cli check`. Expected: PASS.
- [ ] **Step 5: Commit** `test(12b-1): the node specs run on production; specs that stop nodes only on purpose`.

### Task 4: The install on the hardware (by hand, with the person)

**Files:**
- Modify: `infra/production/metal/authorized_keys`.
- Modify: `infra/production/metal/hosts.json` (`nixie`'s record).

- [ ] **Step 1: The person.**
  - Makes the key: `ssh-keygen -t ed25519 -f ~/.ssh/homelab-production` with a passphrase, then `ssh-add` it.
  - Pastes the public key into `authorized_keys`, which is committed.
  - Adds the two Hyper-V firewall rules (spec decision 2).
  - Logs in to Cloudflare (`cf auth login`) for production's state.
  - Switches the three machines off.
- [ ] **Step 2: RED.** `just test e2e --env production --labels 'production && !hardware-proof'`. Expected: FAIL (no
  kubeconfig, no nodes).
- [ ] **Step 3: Install.** `HOMELAB_PXE_ADDRESS=<laptop's LAN IP>` and `cd infra/production/metal && terragrunt apply`.
  Expected:
  - `nixie` wakes, installs and records the three machines;
  - `trust`, `enroll` and `kubeconfig` succeed;
  - `kubectl get nodes` shows three Ready servers.

  If a machine does not wake, the person presses F12 on it.
- [ ] **Step 4:** The person removes the Hyper-V rules. Commit `hosts.json` (the record) and `authorized_keys`:
  `feat(12b-1): production's nodes installed`.
- [ ] **Step 5: GREEN.** The same command as step 2. Expected: PASS (nodes, secrets encrypted, seccomp, DNS, kube-bench's
  scan with its accepted list). A new kube-bench FAIL is triaged into `.cluster-scan-accepted.yaml` or fixed.
- [ ] **Step 6:** Rerun `terragrunt apply`. Expected: `nixie` sees every host recorded and does not start; `upgrade`
  finds every node up to date.

### Task 5: The hardware proofs

**Files:**
- Create: `scripts/node-fio <ip>`. Over SSH as root, it runs `fio` from `nix shell nixpkgs#fio`:
  - etcd's pattern in `/var/lib/rancher`;
  - sequential and random 4k on the `rook-data` partition, then `blkdiscard` on it so Ceph finds it empty.

  It prints the p99 fdatasync and the throughputs.
- Modify: `docs/installation/production/deployment.md`: a "Measured" section with the numbers.

- [ ] **Step 1:** `scripts/node-fio` on each node. Expected: a p99 fdatasync for every node, recorded. A node over 10 ms is
  recorded as a finding for 12b-2, not fixed here.
- [ ] **Step 2:** `just test e2e --env production --labels hardware-proof --focus ProductionVIPMoves`. Expected: the VIP
  moves, and the node wakes by Wake-on-LAN and falls back to its NVMe. If it does not wake, record it and the person
  presses the button.
- [ ] **Step 3:** `--focus ProductionPowerCut`, with the person at the power strip. Expected: every node back by itself;
  the duration recorded.
- [ ] **Step 4:** After the cut, check that each node's first boot entry is NixOS's (`efibootmgr` over SSH), and delete
  the stale `Windows Boot Manager` entries (`efibootmgr -b <n> -B`).
- [ ] **Step 5: Commit** `docs(12b-1): the hardware's numbers`.

### Task 6: Docs

**Files:**
- Modify: `docs/installation/production/prerequisites.md`: the BIOS checklist as found on these HP machines.
  - Latest BIOS, Secure Boot off.
  - After Power Loss = Previous State.
  - Wake-on-LAN = Boot to Network.
  - Boot order: M.2, then IPv4; IPv6 off.
  - F12 for a manual network boot.
- Modify: `docs/installation/production/deployment.md`:
  - the Hyper-V rules (add and remove);
  - `HOMELAB_PXE_ADDRESS`;
  - the SSH key;
  - the record in `hosts.json` (commit it; to reinstall a node, delete its `machine_id_hash`).
- Modify: `docs/concepts/pxe-boot.md`: PXE from WSL (mirrored) and why the rules are needed.
- Modify: `CLAUDE.md`:
  - 12b-1 done, with its key facts;
  - the before-production list: settled items are removed or reworded (PXE on a real LAN, Wake-on-LAN, UEFI boot order,
    the disk path, kube-bench on hardware, the power cut of the nodes); what moves to 12b-2 is named (Ceph, the cut with
    data, fio's finding if any).
- Modify: `docs/superpowers/deferred-minors.md`: this branch's minors.

- [ ] **Step 1:** Write them. `direnv exec . mkdocs build --strict` and `direnv exec . scripts/ci-check all`. Expected:
  PASS.
- [ ] **Step 2: Commit** `docs(12b-1): production's nodes`.
