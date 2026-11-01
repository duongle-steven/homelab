# Step 12a-1: staging nodes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Three NixOS VMs in WSL, installed by `nixie` from one node definition shared with production, joined into a
three-server k3s cluster with kube-vip and rook-ceph, upgraded in place one node at a time, and measured; `metal/`
(Ansible) removed.

**Architecture:** Upstream `nixos`'s `infra/_modules/metal` flake (NixOS configuration, disko, profiles) and its
Terragrunt unit `infra/<env>/metal` (hooks: `nixie`, `lab cluster enroll`, `lab cluster kubeconfig`) are ported and
adapted to this repository's k3s choices. Staging's machines are libvirt VMs created by an OpenTofu unit
`infra/staging/vms` on a libvirt NAT network whose DHCP gives fixed leases by MAC; `nixie` answers their PXE boot as a
proxyDHCP. A new `lab cluster upgrade` rebuilds the nodes in place, one at a time.

**Tech Stack:** NixOS 26.05 (k3s 1.37 from nixos-unstable's `k3s_1_37`), disko, nixie, nixos-anywhere, OpenTofu +
Terragrunt, libvirt (provider `dmacvicar/libvirt` 0.9.9), Go (`cli/`, upstream's `internal/cluster`), rook-ceph.

**Spec:** `docs/superpowers/specs/2026-10-31-staging-design.md`

## Global Constraints

- One node definition for staging and production (`infra/_modules/metal/flake`); `metal/`, `nix develop .#metal`,
  `make metal` and `make clean`'s PXE line are removed in this plan.
- Installed by `nixie` (PXE), never by `nixos-anywhere` alone or a prebuilt image.
- VMs: 3 × 3.5 GB (3584 MiB), 2 vCPUs, zram swap. If the core does not fit: drop apps → squeeze the core (OSD toward
  900 MB) → 3.75 GB per VM with WSL at 12 GB → WSL 13 GB last. Three nodes always.
- Staging never runs with the sandbox: `make staging` refuses while `k3d-homelab-dev-server-0` runs, `make dev`
  refuses while a staging VM runs; both refuse when `C:` has less than 30 GB free.
- Latest stable, pinned exactly: Gateway API v1.7.0 (sandbox's k3d too), kube-vip v1.3.1, libvirt provider 0.9.9,
  nixie by commit in `flake.lock`; k3s 1.37 (`k3s_1_37`, matching the sandbox's `rancher/k3s:v1.37.1-k3s1`).
- This repository's k3s choices, not upstream's: ServiceLB on; kube-vip serves the API VIP and only Services of class
  `kube-vip.io/kube-vip-class`; audit policy and flags (15c); `--secrets-encryption`; control-plane metrics
  (`etcd-expose-metrics`, controller-manager/scheduler `bind-address=0.0.0.0`, kube-proxy `metrics-bind-address`);
  `disable: traefik, local-storage, gateway-api-crd`; `disable-helm-controller`.
- `infra/<env>/cluster/config.json` keeps its format and gains `init_host`; machines live in
  `infra/<env>/metal/hosts.json` (`mac_address`, `ip`, `disk`). Production's MACs stay placeholders.
- Changes to `~/nixos-config` (outside the repo) are shown to the user and made only after their yes.
- Never `make dev`/`make staging` from a worktree; never print tokens (the k3s join token goes over SFTP, as upstream).

## Review Focus

- A VM that PXE-boots but whose MAC is not in `hosts.json`: nixie must ignore it, not install something — Task 3's
  test boots no foreign VM, so the review checks nixie's flags.
- `make staging` with a node already installed: nothing reinstalls it (`machine_id_hash` / state), only the upgrade
  runs — Task 4's Step 3 and Task 5's Step 3.
- An upgrade that leaves a node NotReady or Ceph unhealthy: `lab cluster upgrade` stops before the next node —
  Task 5's `TestUpgradeStopsOnUnhealthy`.
- WSL memory: a run that would push WSL into swap thrash — Task 7 records free memory and swap per phase.
- The VIP while its holder is down: the API answers through the VIP from another node — Task 6's `TestStagingVIPMoves`.

---

### Task 1: The node flake (offline)

**Files:**
- Create: `infra/_modules/metal/flake/{flake.nix,flake.lock,default.nix,configuration.nix,disks.nix,authorized_keys}`,
  `profiles/{installer,k3s-server,kube-vip,network,rook-ceph,zram}.nix` (from `git show upstream/nixos:infra/_modules/metal/flake/...`)
- Create: `infra/_modules/metal/{main.tf,variables.tf,versions.tf}` (upstream's: writes `flake/hosts.json` and
  `flake/cluster.json`, both git-ignored)
- Move: `metal/roles/k3s/files/audit-policy.yaml` → `infra/_modules/metal/flake/audit-policy.yaml`
- Modify: `infra/sandbox/cluster/terragrunt.hcl:11`, `infra/_modules/k3d/{main.tf:33,k3d.yaml:38}` (new path, Gateway
  API `v1.7.0`), `test/security_logs_test.go:28-29,194-197`, `test/scopes.yaml:71`
- Test: `test/metal_test.go`

**Interfaces:**
- Produces: `nixosConfigurations.<host>` for each `hosts.json` entry and `nixosConfigurations.installer`; specialArgs
  `hostConfig` (`ip`, `mac_address`, `disk`), `network` (`prefix_length`, `gateway`), `k3s` (`clusterInit`, `vip`,
  `nodeIPs`). The flake reads `cluster.json` = the env's `config.json` (fields `init_host`, `vip`, `network`).

- [ ] **Step 1: Write the failing tests** in `test/metal_test.go` (offline, like `TestAuditPolicyRender`): evaluate the
  flake with a fixture `hosts.json`/`cluster.json` (3 hosts, `init_host` the first) through
  `nix eval --json path:<tmp copy>#nixosConfigurations.<host>.config.services.k3s`:
  - `TestMetalK3sFlags`: `clusterInit` true only on `init_host`; others `serverAddr == "https://<vip>:6443"`;
    `extraFlags` contain `--secrets-encryption`, `--node-ip=<ip>`, `--tls-san=<vip>`, every flag of the audit set
    (the list `TestAuditPolicyRender` asserts today for `metal/roles/k3s`); `disable` equals
    `["traefik","local-storage","gateway-api-crd"]` (servicelb absent); `package.version` starts with `1.37.`.
  - `TestMetalKubeVip`: the kube-vip manifest's image is `ghcr.io/kube-vip/kube-vip:v1.3.1`, env `svc_enable=true`,
    and the loadBalancerClass filter `lb_class_only=true` with class `kube-vip.io/kube-vip-class`; no
    `kube-vip-cloud-controller` manifest.
  - `TestMetalZram`: `zramSwap.enable` true on every host.
  - `TestMetalDisk`: disko's `rook-data` partition exists, unformatted, on `hostConfig.disk`.
- [ ] **Step 2:** `cd test && go test -run 'TestMetal' ./...` → FAIL (no flake).
- [ ] **Step 3: Port and adapt.** Copy upstream's flake; nixpkgs `nixos-26.05` plus `nixpkgs-unstable` for
  `services.k3s.package = k3s_1_37`; replace upstream's `extraFlags`/`disable` with the constraints' set (audit flags
  from `metal/roles/k3s/defaults/main.yml:30-37`, the policy file through `environment.etc."rancher/k3s/audit-policy.yaml"`,
  log dir `/var/log/kubernetes/audit`); kube-vip: drop the cloud controller and its pool, add `lb_class_only`/
  `lb_class_name`; Gateway API manifest v1.7.0 with its hash; `profiles/zram.nix` (`zramSwap.enable = true`,
  `memoryPercent = 50`); `authorized_keys` holds the workstation's public key (`~/.ssh/id_ed25519.pub`, a public
  key, not a secret). `.gitignore`: `infra/_modules/metal/flake/{hosts,cluster}.json`.
- [ ] **Step 4:** `go test -run 'TestMetal|TestAuditPolicyRender' ./...` → PASS; `scripts/ci-check go infra lint`
  → pass.
- [ ] **Step 5: Commit** `feat(metal): the NixOS node flake from upstream nixos, with this repository's k3s`.

### Task 2: `metal/` removed, the dev shell's tools

**Files:**
- Delete: `metal/`
- Modify: `flake.nix` (drop `devShells.metal`; add inputs `nixie` and tools `nixie`, `nixos-anywhere`,
  `nixos-rebuild`, `libvirt` (for `virsh`)), `Makefile:23,87-88,131`, `.trivyignore.yaml:8-9`,
  `test/scopes.yaml:157-170` (one group `infra/_modules/metal/**`, `infra/staging/**` → tests `Staging`; the sandbox
  group keeps `audit-policy.yaml` at its new path), `cli/internal/scope/check_test.go:47`,
  `infra/production/argocd/terragrunt.hcl:5`, `infra/_modules/openbao/main.tf:60`
- Modify docs: `docs/concepts/pxe-boot.md` (nixie), `docs/installation/production/{deployment,configuration}.md`,
  `docs/reference/architecture/overview.md`, `docs/reference/faq.md`

- [ ] **Step 1:** `grep -rn 'metal/\|make -C metal\|devShells.metal' --exclude-dir=.git . | grep -v '^./docs/superpowers/'`
  lists every reference; each is removed or pointed at `infra/_modules/metal`.
- [ ] **Step 2:** `nix develop -c sh -c 'nixie --help && nixos-anywhere --help && virsh --version'` → all print.
- [ ] **Step 3:** `scripts/ci-check all` (sandbox stopped) → pass; `make -C cli check` passes with the new scopes.
- [ ] **Step 4: Commit** `feat!: metal/ (Ansible) removed; nodes come from infra/_modules/metal`.

### Task 3: `lab cluster enroll` and `lab cluster kubeconfig`

Task 4's hooks call these, so they come first.

**Files:**
- Create: `cli/internal/cluster/*.go` and `cli/internal/cli/cluster_{enroll,kubeconfig}.go` with their tests, from
  `upstream/nixos` (module path `github.com/duongle-steven/homelab/cli`), `go.mod` gains `github.com/pkg/sftp` and
  `golang.org/x/crypto/ssh` at their latest versions.

**Interfaces:**
- Produces: `lab cluster enroll --environment staging|production`, `lab cluster kubeconfig --environment <env> --output
  <path>`; package `cluster`: `loadConfig(environment string) (config, error)` reading `init_host`/`vip` from
  `infra/<env>/cluster/config.json` and `ip` from `infra/<env>/metal/hosts.json`.

- [ ] **Step 1:** Port upstream's tests unchanged except paths; add `TestLoadConfigReadsThisRepoFormat`: a
  `config.json` with this repo's extra fields (`service_vips`, `domain`, `zone`, `timezone`, `seed_repository`) and
  `init_host` loads, seed = `init_host`, two joiners.
- [ ] **Step 2:** `make -C cli test` → FAIL, then port the code → PASS; `make -C cli check` → pass.
- [ ] **Step 3: Commit** `feat(cli): lab cluster enroll and kubeconfig from upstream nixos`.

### Task 4: Staging's VMs and the nixie install (the risk: the first live task)

**Files:**
- Outside the repo (user approves the diff first): `~/nixos-config`: `virtualisation.libvirtd.enable = true;`,
  `users.users.nixos.extraGroups = [ "docker" "libvirtd" ];`, then `sudo nixos-rebuild switch`.
- Create: `infra/_modules/staging-vms/{main.tf,variables.tf,versions.tf}`, `infra/staging/vms/terragrunt.hcl`,
  `infra/staging/metal/{terragrunt.hcl,hosts.json}`, `infra/staging/cluster/config.json`
- Modify: `infra/root.hcl` (staging: local encrypted state like the sandbox)

**Interfaces:**
- Produces: libvirt network `homelab-staging` (NAT, `192.168.122.0/24`-style range of the builder's choice, DHCP with
  one fixed lease per `hosts.json` MAC, no PXE options: nixie adds them), domains `<host>` (3584 MiB, 2 vCPUs, boot
  order network then disk, a sparse qcow2 system disk ≥ 80 GB and the `rook-data` partition on it per `disks.nix`).
  `infra/staging/cluster/config.json` = production's format + `init_host`, staging's VIP and `service_vips` in the
  libvirt network, `domain` a `*.nip.io` name for the VIP.
- `infra/staging/metal/terragrunt.hcl` = upstream's (hooks `nixie`, `lab cluster enroll --environment staging`,
  `lab cluster kubeconfig --environment staging --output infra/staging/kubeconfig.yaml`), depending on `vms`.

- [ ] **Step 1: One VM first.** With the sandbox stopped (`k3d cluster stop homelab-dev`) and `hosts.json` holding one
  host, `cd infra/staging && terragrunt run --all apply`: the VM PXE-boots nixie's installer, nixie installs it, the
  enroll hook passes for a one-node cluster. If nixie cannot answer PXE on the libvirt bridge, stop and bring the
  failure to the user before working around it (the install method is the user's decision).
- [ ] **Step 2:** `ssh admin@<ip> hostname` → the host's name; `ssh admin@<ip> systemctl is-active k3s` → `active`.
- [ ] **Step 3:** Three hosts in `hosts.json`; apply again → `kubectl --kubeconfig infra/staging/kubeconfig.yaml get
  nodes` lists three `Ready` control-plane nodes; a second apply reinstalls nothing (nixie's recorded
  `machine_id_hash`).
- [ ] **Step 4: Commit** `feat(staging): three libvirt VMs installed by nixie and joined into k3s`.

### Task 5: `lab cluster upgrade` (in place, one node at a time)

**Files:**
- Create: `cli/internal/cluster/upgrade.go`, `upgrade_test.go`, `cli/internal/cli/cluster_upgrade.go`
- Modify: `infra/staging/metal/terragrunt.hcl` (an `after_hook "upgrade"` after `enroll`), and production's unit the
  same (`infra/production/metal/`, created here with placeholder `hosts.json`)

**Interfaces:**
- Produces: `lab cluster upgrade --environment <env>`; `Upgrade(ctx, environment string, progress io.Writer) error`;
  internally an interface `upgrader` with `drain(node)`, `rebuild(node)` (`nixos-rebuild switch --flake
  path:infra/_modules/metal/flake#<name> --target-host root@<ip>`), `ready(node)`, `healthy()` (every node Ready and,
  once a `CephCluster` exists, its `status.ceph.health == "HEALTH_OK"`), `uncordon(node)`.
- Behaviour: skips a node whose `/run/current-system` already equals the flake's build for it (`nix build` the
  toplevel, compare store paths over SSH); otherwise drain → rebuild → ready (10 min) → healthy (15 min) → uncordon;
  stops at the first failure, leaving that node cordoned and naming it.

- [ ] **Step 1: Failing tests** with a fake `upgrader`: `TestUpgradeSkipsCurrentNodes` (no drain when current),
  `TestUpgradeOneAtATime` (drain/rebuild/ready/healthy/uncordon per node, the seed first, then the
  joiners by name), `TestUpgradeStopsOnUnhealthy` (healthy fails on node 1 → node 2 untouched, error names node 1).
- [ ] **Step 2:** FAIL → implement → PASS (`make -C cli test check`).
- [ ] **Step 3: Live:** change a harmless option (`environment.etc."homelab-upgrade-probe".text`), `cd infra/staging && terragrunt run --all apply` →
  the three nodes are drained and rebuilt in turn, `ssh admin@<ip> cat /etc/homelab-upgrade-probe` on each; a second
  run upgrades nothing. Revert the probe.
- [ ] **Step 4: Commit** `feat(cli): lab cluster upgrade rebuilds the nodes in place, one at a time`.

### Task 6: `make staging`, rook-ceph, the staging tests

**Files:**
- Modify: `Makefile` (targets `staging`, `staging-fresh`, `staging-clean`; `dev` gains the reverse guard)
- Create: `scripts/staging-guard` (refuses: sandbox running / any staging VM running for `dev`; `C:` < 30 GB via
  `df --output=avail -BG /mnt/c` when `/mnt/c` exists)
- Create: `system/rook-ceph/values-staging.yaml` (OSD `osd_memory_target` 1 GiB, one mgr, three mons, the
  `rook-data` partition as the OSD device)
- Create: `test/staging_test.go` (build tag or a `stagingOnly(t)` skip reading `HOMELAB_ENV=staging`, like
  `meshSandboxOnly`)

`make staging` (main checkout only, `sandbox-checkout`'s worktree refusal reused): guard → `cd infra/staging &&
terragrunt run --all apply` → rook-ceph from `scripts/render-chart system/rook-ceph staging | kubectl apply
--server-side` (Argo CD is 12a-2's) → `make -C test filter=Staging env=staging`. `staging-fresh` = `staging-clean`
then `staging`; `staging-clean` destroys `infra/staging` (VMs and disks).

- [ ] **Step 1: Tests:** `TestStagingNodes` (3 nodes Ready, all control-plane; etcd has 3 members: `kubectl get --raw
  /metrics` on each node or `etcdctl member list` over SSH), `TestStagingVIPMoves` (find the VIP holder's node through
  kube-vip's lease, `virsh shutdown` it, `/readyz` through the VIP answers within 60 s, start it again, wait Ready),
  `TestStagingCeph` (`CephCluster` `HEALTH_OK`, 3 OSDs up, a PVC on the RBD class written then read back from a pod on
  another node).
- [ ] **Step 2:** `make staging` → the three tests PASS; `make staging` with `k3d-homelab-dev-server-0` running →
  refuses before touching anything.
- [ ] **Step 3: Commit** `feat(staging): make staging, rook-ceph on three nodes, the staging tests`.

### Task 7: Measure, then docs

**Files:**
- Create: `docs/installation/staging.md` (requirements: libvirtd, the sandbox stopped, 30 GB on `C:`; `make
  staging`, `staging-fresh`, `staging-clean`; the measured table)
- Modify: `CLAUDE.md` (roadmap 12a-1 done; the "before the first production deploy" list: what staging now proves —
  three-node etcd, kube-vip, rook-ceph, the NixOS nodes and their install — and what it does not yet), `docs/
  installation/sandbox.md` (`make dev` refuses while staging runs), `docs/superpowers/deferred-minors.md`

- [ ] **Step 1:** During Task 6's run, log every 30 s: WSL's `free -m` and swap, each VM's guest `free -m` over SSH,
  `C:` free. Record idle (after the tests) and peak. If a VM's available memory stays under 300 MiB or WSL swaps more
  than 1 GB, apply the constraints' fallback order and say which step was taken.
- [ ] **Step 2:** Write the table (per node: k3s, etcd, Istio absent yet, rook pods, free; WSL total) into
  `docs/installation/staging.md`.
- [ ] **Step 3:** `scripts/ci-check all` (sandbox and staging stopped) → pass.
- [ ] **Step 4: Commit** `docs(staging): the staging guide, measured; roadmap 12a-1`.
