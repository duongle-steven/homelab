# Cluster scan

What is misconfigured in the cluster itself and in what it runs: the API server's and kubelets' flags, RBAC, and what
workloads may do on a node (step 15d, `docs/superpowers/specs/2026-11-05-cluster-scan-design.md`). Two scanners, run by
hand, each against the environment it can read:

| Target | Tool | Where | Reads |
|---|---|---|---|
| `just testbed-scan`, or `just test e2e --env production --labels scan` | kube-bench (CIS benchmark `k3s-cis-1.11`) | the testbed's or production's three NixOS nodes | k3s's flags, files and permissions on each node |
| `just dev-scan` | kubescape 4.0.17 (`allcontrols`, `--keep-local`) | the sandbox, the only environment with the whole platform | RBAC and workloads through the API |

Run them before step 12b's hardware, and again when k3s, the node profile (`infra/_modules/metal`) or a component's
RBAC changes. Neither is part of `just dev`, `just testbed` or a release's proof. Production's hardware ran the
same Job in step 12b-1 (`just test e2e --env production --labels scan`).

Both read `.cluster-scan-accepted.yaml`: a finding it does not list fails the target. Each fails rather than passes
when the scan did not really run (below).

## Running them

```sh
just testbed            # VMs up (just testbed-stop after)
just testbed-scan       # one kube-bench Job per node, in a temporary privileged namespace, deleted afterwards

k3d cluster start homelab-dev   # every pod Ready first
just dev-scan
```

A failure names what nobody decided on: `homelab-2: undecided, ...: 1.2.30` for kube-bench (one line per node), one
`C-0057 <namespace>/<Kind>/<name>` per line for kubescape. Reports hold command lines and config: they stay in the
test's temporary directory and the failures name only checks, controls and resources. kubescape downloads its controls
at each run, so a new control can fail the target with no change here: read it like any other finding.

## Deciding a finding

Fix it if the fix is small and contained, otherwise accept it in `.cluster-scan-accepted.yaml` with its reason. A
finding that would let someone reach a node, every Secret or the whole API and has no small fix is raised before it is
accepted.

- **kube-bench, by check number** (`kubeBench`). Only FAILs fail the target. WARNs are manual checks, read once below.
- **kubescape, two levels** (`kubescape`):
  - `controls`: noise here, accepted whole (limits, labels, probes, signatures, what Kyverno already checks).
  - `resources`: a group of controls on a group of resources, for one reason. The resource is
    `<namespace>/<Kind>/<name>` (a cluster-scoped one starts with `/`), and a pattern only inside the name
    (`kube-system/DaemonSet/svclb-*`), so a new workload holding the same power fails the target. The dangerous
    controls (cluster-admin, wildcards, impersonation, bind and escalate, privileged, host path, network and PID,
    capabilities, credentials in configuration) can only be accepted here; the file is refused otherwise. So are
    who may read every Secret, exec, create pods or volumes, port-forward or reach the kubelets' proxy
    (`Dangerous` in `test/internal/clusterscan/clusterscan.go`), and a name pattern keeps at least three characters of its own.

Sort by what a finding would let someone do, not by the tool's score.

## Not fooled by the tools

The spike found each of these reporting clean or wrong; the targets now fail instead:

- kube-bench's image has no `journalctl`, which every k3s check calls: 21 false FAILs until the node's `/nix` and
  `/run/current-system` are mounted and on `PATH`. A result whose audit command was `not found` fails the target.
- Without `run --targets master,etcd,controlplane,node,policies` it detects no control plane on k3s and skips three
  sections with 0 fails. Each of sections 1 to 5 must have results on every node, and every node must report.
- kube-bench v0.16.0's image has k3s benchmarks only up to 1.9: `k3s-cis-1.11` is vendored from its `main`
  (`test/kube-bench-cfg/`, the commit in `config.yaml`'s header). Drop the directory when a release has it.
- No benchmark maps to Kubernetes 1.37 (the table ends at 1.35): this is the nearest, not an exact match.
- kubescape must have seen at least 500 resources and `istio-system/DaemonSet/ztunnel`, or the target fails.

Each guard was seen to fail: without the `/nix` mount (`1.2.1: its audit command was not found` on every node), without
`--targets` (`section 1 did not run`), and kubescape found a privileged Pod applied for the purpose
(`C-0057 scan-probe/Pod/scan-probe`).

## What was found and decided (2026-11-05)

**kube-bench**: 64 pass, 5 fail, 49 warn on each node before this step.

| Check | Decision |
|---|---|
| 1.2.30 `service-account-extend-token-expiration` | Fixed: `false` on the nodes and k3d |
| 1.3.7, 1.4.2 `bind-address`, 4.3.1 kube-proxy metrics | Accepted: bound beyond localhost for Prometheus; `nixos-fw` admits only pods and nodes on those ports |
| 4.2.4 `read-only-port` | Accepted, a false report: the journal's line ends `read-only-port=0"`, the check reads `0"`; nothing listens on 10255 |

The fourteen warnings outside section 5's manual policy checks (section 5 is what Kyverno and kubescape read):

| Warning | Reading |
|---|---|
| 1.2.3 `DenyServiceExternalIPs` | Left: no Service sets `externalIPs`; Kyverno could refuse it if one ever does |
| 1.2.9 `EventRateLimit` | Left: an alpha admission plugin needing its own config file |
| 1.2.11 `AlwaysPullImages` | Left: every image is pinned, and pulling at every start would make a power cut's recovery wait on the registries |
| 1.2.17 `audit-log-maxage` | Fixed: 30 days, on the nodes and k3d (the size and backup limits still bound it) |
| 1.2.18 `audit-log-maxbackup` | Left at 3: Alloy ships the audit log to Loki, which keeps 60 days ([Security logs](security-logs.md)) |
| 1.2.20 `request-timeout` | Left at the default |
| 1.3.1 `terminated-pod-gc-threshold` | Left at the default |
| 3.1.1 to 3.1.3 authentication for users | People sign in through Kanidm to the apps; only the admin kubeconfig uses a client certificate, kept on the workstation |
| 3.2.2 audit policy | Read in step 15c (`infra/_modules/metal/flake/audit-policy.yaml`) |
| 4.2.12 kubelet ciphers | Left: Go's defaults are strong; pinning a list would need updating as Go changes them |
| 4.2.13 pod PID limit | Fixed: `pod-max-pids=4096`, on the nodes and k3d |
| 4.2.14 `seccomp-default` | Fixed (step 15h): on, on the nodes and k3d, so a pod naming no profile gets `RuntimeDefault`. Woodpecker's Buildah steps need `unshare`, which it refuses: a native `MutatingAdmissionPolicy` gives them the `Localhost` profile `homelab/buildah.json` (`RuntimeDefault` plus `unshare`, `mount`, `umount2` and `pivot_root`, `infra/_modules/metal/flake/seccomp`), and a `ValidatingAdmissionPolicy` refuses any `Localhost` profile to every other pod in namespace `woodpecker` ([Woodpecker](woodpecker.md)); other namespaces have no such guard, their pods come from git |

**kubescape**: 87 failed controls on the sandbox, about 2,100 control and resource pairs. Fixed:

- Velero was bound to `cluster-admin`, though it moves only volumes: its own ClusterRole now
  (`system/velero/templates/rbac.yaml`). Two tries were short of what it needs: its ServiceAccount plugin lists
  ClusterRoleBindings when it starts, and its controllers watch its own kinds (`datauploads`) at cluster scope, so a
  restore waited for them forever. It may not create volumes: one could be a `hostPath` volume a pod in its own
  namespace mounts, which is the node (restored claims get theirs from the CSI provisioner). `TestBackupRestore` is
  the proof.
- Grafana could read every Secret (the chart's ClusterRole for its sidecars): a Role reading only ConfigMaps in
  `grafana` and `monitoring-system` now.
- kube-state-metrics listed every Secret for `kube_secret_*`, which nothing here reads: that collector is off.

`TestScanNarrowedRBAC` keeps the three narrow. "Credentials in configuration" (C-0012, C-0259) matched names only: each
value was read and none is a credential (the file says what each is). `kyverno-migrate-resources` names a missing
ServiceAccount because both are the chart's post-upgrade hooks and only the ServiceAccount is deleted afterwards.

## Encryption at rest

k3s encrypts Secrets in etcd (`--secrets-encryption`, AES-CBC, in `infra/_modules/metal/flake/profiles/k3s-server.nix`
since step 12a-1). `TestTestbedSecretsEncrypted` (part of `just testbed`) writes a Secret and reads it raw from etcd
with `etcdctl`: it must start with `k8s:enc:aescbc:` and hold no trace of the value. The sandbox (k3d) has no such flag.

It protects a copy of etcd's data taken without the key: an etcd snapshot, a disk image of the database alone. It does
**not** protect against:

- someone with the cluster's credentials or a pod that may read Secrets: the API server decrypts for them;
- a stolen or thrown-away disk: the key is on the same disk
  (`/var/lib/rancher/k3s/server/cred/encryption-config.json`, mode 0600). Disk encryption (LUKS) would; it is set aside
  for now, an accepted risk on the before-production list (adding it later means reinstalling each disk).

Public clouds' "encryption at rest" works the same way at the storage layer, with the key in a separate KMS: it guards
the media, not against valid credentials.

### Rotating the key

Only by a person, when there is a reason (`k3s.encryption-key` in `cli/internal/rotate/catalog.yaml`): k3s warns a
wrong rotation can corrupt the cluster for good, and with stolen disks set aside a schedule guards little. Run once on
the testbed (2026-11-05):

```sh
ssh root@<a server> k3s secrets-encrypt status       # Enabled, All hashes match
ssh root@<a server> k3s secrets-encrypt rotate-keys  # "keys rotated, reencryption finished"
# restart k3s on each server in turn, waiting for it to be Ready
ssh root@<each server> systemctl restart k3s
ssh root@<a server> k3s secrets-encrypt status       # All hashes match, a little after the last restart
```

`rotate-keys` reports `reencrypt_finished` at once, but the hashes differ until every server has restarted: the
status said `hash does not match` for minutes before the restarts and for a little after the last one. Afterwards each
node had one key, dated that run; every Secret in etcd (27) began with the new key's name, every node listed them
through the API, and `TestTestbedSecretsEncrypted` passed.

The k3s token (`k3s.token`) has an entry too, with k3s's documented steps (`k3s token rotate`); it was never run here.
