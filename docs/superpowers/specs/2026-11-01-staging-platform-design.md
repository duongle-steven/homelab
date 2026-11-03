**Replaced by `docs/superpowers/specs/2026-11-02-testbed-design.md`:** this staging ran out of CPU on the workstation before the core was up.

# Step 12a-2: staging's platform, deploy path and release mark

Status: designed in chat 2026-11-01. Builds on 12a-1 (`docs/superpowers/specs/2026-10-31-staging-design.md`,
`docs/installation/staging.md`): three NixOS k3s VMs in WSL (3.5 GB, 2 vCPU each), kube-vip, rook-ceph with AES256K
CephX keys, `make staging`, `lab cluster enroll|kubeconfig|upgrade`. Decisions marked *(user)* were made in chat;
*(Claude's call)* marks the builder's own, open to review.

## Intent

Staging exists to prove, before a release, what the one-node sandbox cannot: production's components on three nodes
with Ceph, and production's deploy path. **Ideally staging is production** *(user)*: the same components, the same
values, the full suite. A workstation that cannot hold all of it runs less, and the reduction is a property of that
machine, not of staging's design *(user)*: someone with more memory, or real machines, empties the reduction and gets
production's parity and the full test run, with no other change.

When it must shrink, it drops first what the sandbox already proves the same way, so no work is duplicated *(user)*.

## Goals

- Argo CD, the platform and as many apps as the workstation holds, on staging, deployed the way production is.
- Production's deploy path end to end: the bootstrap from the seed repository, Forgejo on staging, `lab infra forgejo
  sync|push`, Argo CD switching to `ops/homelab` (the switch the sandbox never proves).
- `make staging` marks the commit it proved; `lab infra forgejo verify` refuses a release that changes anything
  staging runs without that mark.
- Reuse the existing sandbox tests against staging where they apply; no second copy of a test.

## Non-goals

- Cloudflare: the Tunnel, `cloudflared`, `external-dns`, the public Gateway, ACME. Staging has a local CA like the
  sandbox. Renovate (production only).
- The power work: `lab cluster shutdown|startup`, NUT, the power-cut tests (12a-3).
- Running staging and the sandbox at once.

## Anti-goals

- A staging that crashes WSL (memory, or `C:` below 30 GB free).
- A gate that blocks a release over something staging does not run, or lets through a change to something it runs.
- Staging-only copies of production config beyond what must differ (`values-staging.yaml` only where it must).

## Ollama

Removed from the homelab *(user)*, in its own small pull request before 12a-2: its app file, egress entry, volume
group, catalog entries and docs. It can come back later.

## What runs on staging

The default is production's set. The reduction is one list of excluded components and apps
*(Claude's call: a list in `system/argocd/values-staging.yaml` that the `root` and `apps` ApplicationSets leave out,
with a comment naming the machine it was sized for; a larger machine empties it in a pull request together with the
VMs' size in `infra/staging/vms`)*.

What staging must run (the sandbox cannot prove it) *(user agreed, "tạm thời vậy")*:

- rook-ceph, Istio on three nodes (the Gateway's VIP through kube-vip), Argo CD syncing from Forgejo
- OpenBao, Kanidm, Forgejo with CloudNativePG's three instances (data on Ceph, failover)
- Velero with real Ceph snapshots (an S3 stand-in like the sandbox's versitygw for the off-site copy)
- Loki with Alloy reading the NixOS nodes' pod logs and audit file; Prometheus scraping the nodes and the control
  plane
- cert-manager (local CA), ESO, Kyverno (every `PolicyException` needs its CRD), trust-manager

Dropped first when memory runs short (the sandbox proves them the same way): stateless apps (excalidraw, pairdrop,
speedtest, homepage, blog), Kiali, ntfy; then the heavy apps (jellyfin, matrix, paperless, OpenCloud). Woodpecker's
step pods in user namespaces on NixOS are unproven: it stays if it fits.

Cannot run on staging: cloudflared, external-dns, the public Gateway, Renovate.

If even the must-run set does not fit after the whole fallback order, 12a-2 still ships with what fits *(user)*:
whatever is left out (a whole component, or one switched off inside its values, such as Prometheus) goes in the
exclusion list, so the gate knows staging did not prove it.

Memory, measured as each part joins (each node's memory and QEMU's in WSL, idle and under the tests); the fallback
order is the 12a spec's *(user)*: drop apps; squeeze the core (Ceph's OSD toward 900 MB, Prometheus off); 3.75 GB per
VM with WSL at 12 GB; WSL at 13 GB last. Three nodes and Istio stay. An estimate *(Claude's, unmeasured)*: the core
platform adds about 1 GB per node, which brings QEMU near 3.3 GB per VM; expect the fallback to be needed.

## Values layers *(user)*

Every environment that is not production shares a layer, so staging gets the local CA and the tests' fixtures without
copying them and without the sandbox's shrinks:

| Environment | Loads, in order |
| --- | --- |
| production | `values.yaml` |
| staging | `values.yaml`, `values-local.yaml`, `values-staging.yaml` (where it exists) |
| sandbox | `values.yaml`, `values-local.yaml`, `values-dev.yaml` |

- `values-local.yaml`: the local CA and its trust (`homelab-dev-ca`, trust-manager, `SSL_CERT_DIR`, `global.devCA`),
  the tests' fixtures (Kanidm's `smoke` client and `dev-admin`, Woodpecker's admin, the rotation CronJob on lab's image
  built from the checkout) and anything else both need.
- `values-dev.yaml`: only what the one-node 12 GB sandbox shrinks or swaps (Prometheus's one day, Loki's 2Gi and 7
  days, Kyverno's workers, versitygw, the CoreDNS rewrite of `*.127-0-0-1.nip.io`, the hostpath CSI).
- `values-staging.yaml`: only where staging must differ (rook-ceph's memory today).
- The split moves content out of the 17 `values-dev.yaml` files; `scripts/env-values`, `scripts/render-chart` and both
  ApplicationSets learn the layer. The sandbox's renders must be identical before and after (checked by rendering
  every component both ways).

## Deploy path

- `infra/staging` gains the units production has where staging needs them: `openbao` (the unseal key), `secrets`
  (OpenTofu's secrets for staging: no Cloudflare), `argocd` (the bootstrap: `system/argocd` with `values-staging.yaml`,
  the seed first, then `ops/homelab` once Argo CD's token reads it, as `infra/_modules/argocd/choose-values.sh` does).
  State stays local in `.state/staging/`, encrypted like the sandbox's.
- `make staging` pushes the commit under test to staging's Forgejo with `lab infra forgejo sync|push` (staging as an
  environment, like the sandbox: a commit with HEAD's tree on top of what Forgejo holds), then waits for Argo CD.
- Certificates from a local CA under the production issuer name, like the sandbox; domain `192-168-100-2.nip.io`
  (`infra/staging/cluster/config.json`). The workstation trusts staging's CA for the tests as it does the sandbox's.

## The mark and the release gate

- `make staging`, when every staging test passed, marks the commit it proved: a GitHub commit status `staging` on that
  commit *(12a spec, Claude's call)*.
- **The gate follows what staging runs** *(user)*. A release must carry the mark when it changes:
  - the infrastructure staging proves: the paths in `infra/staging/release-paths.yaml` *(user: not in
    `test/scopes.yaml`, whose job is the sandbox's tests)* (the node flake, `infra/`, k3s, kube-vip, Istio, storage,
    the deploy path);
  - any component or app staging deploys: production's set minus staging's exclusion list, derived, never listed by
    hand.

  A change only to an app staging leaves out needs no mark: the `dev` → `master` pull request already runs it in a
  fresh sandbox. With an empty exclusion list every release needs the mark.
- When: `make staging` runs on `dev`'s head before a release *(user)*. `verify` accepts a mark on an earlier commit
  whose gated paths equal the release's, so a docs merge into `dev` after the run needs no rerun *(Claude's call)*.
- What a change is compared against *(Claude's call)*: the commit production runs, `ops/homelab`'s `master` on
  Forgejo (read when `verify` runs; on a first install, with no Forgejo yet, everything counts as changed). The gated
  set comes from the proved commit's exclusion list: what staging actually ran.
- Without the mark `verify` refuses, naming the gated paths that changed; `--skip-staging=<reason>` lets the release
  through and the reason is recorded where `--break-glass`'s is.
- A test checks every path in `release-paths.yaml` exists, so the list cannot go stale unnoticed.

## Tests

- The existing suite runs against staging (`ENV=staging`) where a test applies there; a test that cannot (sandbox
  only: k3d, the hostpath CSI, the localhost proxy) skips with its reason. `TestStaging*` stay.
- `make staging` runs `TestStaging*` plus the whole suite for what staging deployed, the destructive tests included
  (`TestBackupRestore`, `TestNodeRestart`, `TestRotation`, like the sandbox's `scope=full`) *(user)*: about an hour
  more; a run that wrecks staging's state is recovered with `make staging-fresh`. The mark needs all of them green.

## Constraints

- Everything the rules in `CLAUDE.md` say: pinned latest versions, `config.json` for environment settings, the secrets
  catalog (`environments` gains `staging` where an entry applies), the backup labels, PolicyExceptions, mesh policies.
- Staging and the sandbox never run together (`scripts/staging-guard`); `make staging` from the main checkout only.

## Left to the builder

- How `lab infra forgejo` takes staging as an environment, and how the bootstrap's seed selection works there.
- Which existing tests apply to staging and how they skip.
- The exact mechanism of the commit status (who writes it, with which credential) and of the derived app set.
- The measurement points and what to drop at each step, within the order above.
- The pull request split: one, or platform then the gate.
