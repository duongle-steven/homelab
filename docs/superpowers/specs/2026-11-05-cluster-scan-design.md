# Step 15d: the cluster's configuration, scanned

Status: designed in chat 2026-11-05, after a spike on the testbed and the sandbox (numbers below). Builds on 12a (the testbed), 15b (Kyverno)
and 15e (the Trivy Operator, whose config, RBAC and compliance scanners are off because this step covers them). Decisions marked *(user)* were
made in chat; *(Claude's call)* marks the builder's own, open to review.

## Intent

Before the first production deploy, find out what is misconfigured in the cluster itself and in what it runs, handle each finding by its weight
*(user)*, and notice when something new appears. The threat this step answers is someone reaching the cluster over the network, so what matters is
the API server's and the kubelets' settings, RBAC and what workloads may do on a node.

The step was named "secrets encryption at rest", but `--secrets-encryption` has been on the nodes since 12a-1 (`profiles/k3s-server.nix`, kept from
upstream). What is missing there is proof and a procedure, not the setting.

## Goals

1. `make testbed-scan` runs kube-bench on every testbed node and `make dev-scan` runs kubescape on the sandbox. Each fails on a finding nobody has
   decided on, and each fails rather than passes when the tool could not really check.
2. Every finding open today is decided: fixed, or accepted in one file with its reason.
3. The fixes below are made and proven.
4. A test proves Secrets are encrypted in etcd on the nodes, and the key has a procedure that ran once.

## Non-goals

- **LUKS, or any disk encryption** *(user)*: stolen machines and thrown-away disks are set aside for now, and it does nothing against someone who gets
  in while the cluster runs. It goes on the before-production list as an accepted risk, saying that adding it later means reinstalling each disk.
- A scanner running in the cluster (the Kubescape operator) and scans on GitHub Actions.
- Fixing all of kubescape's 86 failed controls: most are noise for a homelab (below) and get one line of reason.
- Rotating the encryption key on a schedule: with disk loss set aside it guards little, and k3s's docs warn a wrong rotation can corrupt the cluster
  for good. Its 90 days would add risk, not safety.
- Kyverno's Enforce (15b-2), and a scan as part of a release's proof. `make testbed`'s mark does not depend on these targets.

## What the spike found

**kube-bench** v0.16.0 with `k3s-cis-1.11`, three nodes, the same on each: 64 pass, 5 fail, 49 warn (35 of them section 5's manual policy checks).

| Fail | Reading | Decision |
| --- | --- | --- |
| 1.2.30 `service-account-extend-token-expiration` | Real: not set, so the default | Fix: set it `false` |
| 1.3.7, 1.4.2 `bind-address`; 4.3.1 kube-proxy metrics | Ours on purpose, for Prometheus; `nixos-fw` admits only pods and nodes on those ports | Accept |
| 4.2.4 `read-only-port` | False: the journal's line ends `read-only-port=0"`, the check sees `0"`; nothing listens on 10255 | Accept as a false report |

Fourteen warnings outside section 5 need a reading: 1.2.3, 1.2.9, 1.2.11, 1.2.17, 1.2.18, 1.2.20, 1.3.1, 3.1.1, 3.1.2, 3.1.3, 3.2.2, 4.2.12, 4.2.13, 4.2.14.

Four traps, each of which gave a clean or a wrong result:

- v0.16.0's image has k3s benchmarks only up to 1.9. `k3s-cis-1.11` is on `main`, in no release yet (2026-11-05).
- The image has no `journalctl`, which every k3s check calls: 21 false FAILs until the node's `/nix` and `/run/current-system` are mounted and on `PATH`.
- Without `run --targets master,etcd,controlplane,node,policies` it detects no control plane on k3s and skips three sections, reporting 0 fails.
- No benchmark maps to Kubernetes 1.37 (`main`'s table ends at 1.35), so this is the nearest, not an exact match.

**kubescape** 4.0.8 (nixpkgs), `allcontrols`: testbed 80 failed controls and 132 resources, sandbox 86 and 311. Its compliance score was 61 on both. Worth
reading on the sandbox:

- `velero-server` is bound to `cluster-admin` (C-0185), though Velero here backs up volumes only.
- Wildcards and impersonation: Velero and Argo CD's controller (C-0187, C-0065).
- `kyverno-migrate-resources` names a ServiceAccount that does not exist (C-0307).
- "Credentials in configuration" in 7 resources (C-0012) looks name-matched (`...KEY`, `...SECRETS`). Only names were read so far: read each, `openbao-config` first.

The rest is mostly limits, labels, probes and image signatures, or repeats Kyverno.

## Design

### Where each tool runs *(user)*

- **kube-bench on the testbed:** it reads the NixOS nodes, which the sandbox does not have. Production's hardware runs it in 12b.
- **kubescape on the sandbox:** the only environment with the whole platform. On the testbed it adds little beyond rook-ceph *(Claude's call)*.

Both run by hand: before 12b, and when k3s, the node profile or a component's RBAC change. Neither joins `make dev` or `make testbed`.

### One file of decisions

`.cluster-scan-accepted.yaml` at the root, beside `.trivyignore.yaml`. Every entry has a `reason`.

- **kube-bench, by check number.** Any FAIL not listed fails the target. WARNs do not fail it: they get one reading in this step, recorded in the guide.
- **kubescape, two levels** *(Claude's call)*:
  - Noisy controls are accepted whole, by control ID (limits, labels, probes, signatures).
  - Dangerous controls are accepted by resource, so a new workload there fails the target: at least `cluster-admin`, wildcards, impersonation and
    bind/escalate, privileged, host path, host network and PID, capabilities, credentials in configuration (C-0185, C-0187, C-0035, C-0065, C-0191, C-0057,
    C-0045, C-0048, C-0041, C-0038, C-0046, C-0012, C-0259). A control not listed at all fails the target.

Which of the remaining controls is noisy or dangerous is decided while triaging, by what it would let someone do. Sort by effect, not by the tool's score.

### Not fooled by the tool

The targets fail when a check did not really run, because the spike showed these tools report clean otherwise:

- kube-bench: every one of sections 1 to 5 must be present in the JSON on every node, and no result may contain `not found` from its audit command.
- kubescape: the scan must have seen the resources (a floor on the count, and a known bad resource such as `istio-system/ztunnel` present in the results).

### How they run

- Go tests in `test/`, as `make testbed-power` runs its tests, named so `test/scopes.yaml`'s `full` regex does not select them.
- kube-bench is a Job per node in a temporary namespace (`pod-security.kubernetes.io/enforce: privileged`), deleted afterwards whatever the result. It needs `hostPID`, read-only mounts of
  `/var/lib/rancher`, `/etc/rancher`, `/var/log/journal`, `/etc/machine-id`, `/etc/systemd`, `/nix` and `/run/current-system`.
- Versions, by the repository's rule, latest stable and pinned: kube-bench's image at its release, with `k3s-cis-1.11`'s files vendored from a named `main` commit and the reason beside them (no
  release has them), dropped when one does. kubescape at v4.0.17 or later from its release, since nixpkgs has 4.0.8. Check upstream again when building.
- kubescape always with `--keep-local`: nothing leaves the workstation. Its controls download at each run and are not pinned *(Claude's call)*: a new failing control is then a thing to read, which is
  what the target is for, at the cost of a target that can fail with no change here.

### Fixes in this step

- Set `--service-account-extend-token-expiration=false` on the nodes and on k3d, unless the sandbox's suite shows a client that needs it.
- Velero's `cluster-admin`: find what a restore needs and bind that. `TestBackupRestore` is the proof. If it truly needs nearly everything, accept it by resource with what was tried.
- `kyverno-migrate-resources`' ServiceAccount: find out whether it is a chart's leftover hook or a real failure, then fix or accept.
- Cheap warnings, if measured harmless: `--seccomp-default` (4.2.14), a pod PID limit (4.2.13), audit log age (1.2.17). `make dev` on the whole platform and `make testbed` are the measure, with the
  flags on both k3d and the nodes. Changing k3d's arguments rebuilds the sandbox's cluster. One that breaks a workload is accepted with the reason, not forced.

Everything else found while triaging: fix it here if it is small and contained; otherwise accept it and add a line to `deferred-minors.md`. A dangerous finding that cannot be fixed small is raised
with the user before it is accepted.

### Encryption at rest

- `TestTestbedSecretsEncrypted`, part of `make testbed`: create a Secret with a known value, read its key raw from etcd on a node, and require the prefix `k8s:enc:aescbc:` and no plain value. How to reach etcd is the builder's
  (the client certificates are under `/var/lib/rancher/k3s/server/tls/etcd`, and k3s ships no `etcdctl`). It guards against the flag being lost. The sandbox has no such flag and needs none.
- `k3s secrets-encrypt rotate-keys` runs once on the testbed, with Secrets readable afterwards on all three nodes and through a restart of k3s. The steps and the result go in the guide. It is not a permanent test.
- `cli/internal/rotate/catalog.yaml` gets the encryption key as `manual`, no period, reason as above. The k3s token is not in the catalog either: it gets an entry too, with k3s's documented steps, marked as never run *(Claude's call)*.

### Docs

- `docs/how-to-guides/cluster-scan.md`, in mkdocs' nav: running each target, reading a failure, the file's two levels, what encryption at rest does and does not protect, the rotation.
- `CLAUDE.md`: the roadmap's 15d line, and the before-production list: LUKS's accepted risk, kube-bench to run on hardware, the benchmark not matching 1.37.
- `docs/how-to-guides/supply-chain.md` already points here; check it still reads true.

## Done when

- `make testbed-scan` and `make dev-scan` pass with every finding decided, and each fails when a finding is added to the cluster or removed from the file.
- Each "not fooled" guard was seen to fail (the image without `journalctl`, no `--targets`).
- `TestTestbedSecretsEncrypted` fails without the flag or against a plain value. If removing the flag cannot be tried cheaply, a check on a plain value proves the test can fail.
- `make dev` and `make testbed` pass with the fixes.

## What this does not prove

- CIS 1.11 for k3s is not written for Kubernetes 1.37, and the testbed's `slow_control_plane` differs from production's flags.
- The sandbox's RBAC and workloads are not exactly production's: production-only apps (`matrix`, `jellyfin`, `cloudflared` and others) and paperless and OpenCloud unless `apps=all`. `make dev-scan` scans
  whatever the sandbox has running and installs nothing, and an accepted resource that is absent fails nothing. Production's first scan belongs after 12b.
- Passing a benchmark is not being secure: it checks known settings, not whether the design is sound.

## Left to the builder

File and test names beyond those above, how the Job is rendered, the floor on resources, the order of triage, and the wording of each reason.
