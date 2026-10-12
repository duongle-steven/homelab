# Sub-project 1a: kind + Cilium sandbox (foundation)

Status: approved design, 2026-10-12. Branch: `dev`.

## Goal

A local sandbox, built with one command, that is the place to test changes on
the `dev` branch before they are merged into `master`. 1a builds the foundation:
cluster, networking, ingress, certificates, GitOps, and metrics. Platform
services (users/authZ, Grafana, registry) come in 1b.

## Context

- The fork (`duongle-steven/homelab`) diverges from upstream. The
  `apps/<name>/{Chart.yaml,values.yaml}` layout stays so upstream changes can
  still be cherry-picked.
- Roadmap: 0 workstation (done) → **1a sandbox foundation** → 1b sandbox platform
  → 2 Gateway API migration → 3 production kubeadm + Ubuntu LTS. Each step
  changes one thing, so a failure points to that change.
- `master` has no working sandbox flow. `metal/k3d-dev.yaml` exists but nothing
  calls it; upstream keeps that flow on its stale `dev` branch.
  `upstream/dev` edits `values.yaml` directly, deploys everything, and depends
  on in-cluster Gitea. This design does not reuse it.
- Workstation: NixOS-WSL with Docker CE and 12 GB RAM. Pages are opened from a
  Windows browser via WSL localhost forwarding.

## Decisions

| Topic | Decision |
|---|---|
| Cluster | kind (kubeadm-based, matches the future production cluster), replaces k3d |
| CNI | Cilium with kube-proxy replacement, LB IPAM + L2 announcements (same as prod) |
| Ingress | ingress-nginx, kept for now (Gateway API is sub-project 2) |
| Branching | `dev` is the integration branch; sandbox config lives on every branch and is selected with `env=dev` |
| Env overrides | optional `values-dev.yaml` next to `values.yaml`, loaded only in dev |
| GitOps source in dev | `github.com/duongle-steven/homelab`, branch `dev` (no Gitea) |
| Versions | every tool and chart touched is moved to its **current stable** release (verified while planning, then pinned exactly) |

## Design

### 1. Cluster layer (`metal/`)

- **`metal/kind-dev.yaml`** (new; delete `metal/k3d-dev.yaml`):
  - cluster name `homelab-dev`, one control-plane node, node image that ships
    with the current stable kind
  - `networking.disableDefaultCNI: true`, `networking.kubeProxyMode: none`
  - `kubeadmConfigPatches` binding etcd, kube-scheduler and
    kube-controller-manager metrics to `0.0.0.0`, so Prometheus can scrape them
    (production kubeadm will need the same)
- **Fixed Docker network.** Before the cluster is created, the `kind` Docker
  network is created with subnet `172.30.0.0/16` (skipped if it exists), so LB
  IPs stay the same across rebuilds.
- **`make -C metal env=dev`** (`default` target depends on `env`; prod keeps
  `boot cluster`):
  1. create the network, then `kind create cluster --config kind-dev.yaml`
     (skipped if the cluster exists), and write `metal/kubeconfig.yaml`
  2. `ansible-playbook -i inventories/dev.yml cluster.yml`. The `metal` group
     in `dev.yml` is empty, so only the localhost Cilium play runs.
  3. start container `homelab-dev-proxy` (`alpine/socat`, `--network host`,
     `--restart unless-stopped`) forwarding `127.0.0.1:80` and `:443` to
     `172.30.255.200` (skipped if it is already running)
- **`make -C metal env=dev clean`**: delete the cluster, the proxy container
  and the `kind` network.
- **Cilium role:**
  - `k8sServiceHost`/`k8sServicePort` become inventory variables
    (`prod.yml`: `127.0.0.1`/`6444`; `dev.yml`:
    `homelab-dev-control-plane`/`6443`)
  - bump to current stable Cilium and fix any changed CRD fields (e.g.
    `CiliumLoadBalancerIPPool.spec.blocks` vs `cidrs`, API version of the L2
    policy)
  - `dev.yml` sets `load_balancer_ip_pool: [172.30.255.200/29]`
- **Flake:** move `nixpkgs` to `nixos-26.05`, replace `kube3d` with `kind`, and
  add `cilium-cli`.

### 2. GitOps layer (`system/`, `apps/`)

- **Bootstrap.** `system/bootstrap.yml` takes `env`. In dev it renders ArgoCD
  with `values.yaml` + `values-dev.yaml` and skips the seed/Gitea switch. All
  dev settings for ArgoCD live in `values-dev.yaml`, not in `values-seed.yaml`:
  after bootstrap, ArgoCD manages itself through the ApplicationSet, which only
  loads `values.yaml` + `values-dev.yaml`.
- **`system/argocd/values-dev.yaml`:**
  - git generator: repo `https://github.com/duongle-steven/homelab`, revision
    `dev`, an allowlist of exact paths: `system/argocd`,
    `system/ingress-nginx`, `system/cert-manager`, `system/monitoring-system`,
    `apps/excalidraw`
  - template source: same repo and revision, plus
    `helm.valueFiles: [values.yaml, values-dev.yaml]` and
    `helm.ignoreMissingValueFiles: true`
  - `global.domain: argocd.127-0-0-1.nip.io`
  - Helm replaces lists instead of merging them, and YAML anchors do not cross
    files, so the dev file restates generator and source in full.
  - Prod `values.yaml` is unchanged.
- **`system/ingress-nginx/values-dev.yaml`:** Service annotation
  `lbipam.cilium.io/ips: 172.30.255.200`.
- **`system/cert-manager`:**
  - `templates/clusterissuer.yaml` branches on a new value `issuer.dev`
    (default `false`)
  - when `true`, it renders a self-signed bootstrap issuer, a CA
    `Certificate` `homelab-dev-ca` (in the cert-manager namespace), and a
    `ClusterIssuer` named **`letsencrypt-prod`** of type `ca` backed by that
    certificate. Apps keep their `cert-manager.io/cluster-issuer: letsencrypt-prod`
    annotation unchanged.
  - The same CA is what 1b mounts into dex and Grafana. It can be imported into
    Windows to get rid of browser warnings (documented, optional).
- **`system/monitoring-system/values-dev.yaml`:**
  - `alertmanager.enabled: false`. Disabling it removes the ntfy relay, which
    would otherwise send real notifications, and its manually created
    `webhook-transformer` secret.
  - `kubeProxy.enabled: false` (Cilium replaces kube-proxy)
  - drop the Loki datasource (no Loki in the sandbox)
- **`apps/excalidraw/values-dev.yaml`:** host `draw.127-0-0-1.nip.io`, and the
  external-dns/Cloudflare annotations set to `null`.
- **Chart versions:** the charts of the allowlisted components (argo-cd,
  argocd-apps, ingress-nginx, cert-manager, kube-prometheus-stack, app-template)
  are bumped to their current stable versions, one commit per chart. Breaking
  value changes are fixed in `values.yaml`, because they affect prod too.

### 3. Workflow, testing, docs

- **Root Makefile:**
  - `make dev` runs `metal system smoke-test` with `env=dev`. It skips
    `external` (Terraform/Cloudflare), `post-install` and the PXE `clean`.
  - `make dev-clean` runs `make -C metal env=dev clean`.
  - Plain `make` is unchanged.
- **Change flow:** commit to `dev`, `git push origin dev`, and ArgoCD in the
  sandbox syncs. After it is verified, merge `dev` into `master`.
- **Smoke test (`test/smoke_test.go`):** the app list depends on `ENV`
  (dev: `argocd-server`, `excalidraw`; prod: unchanged). `make dev` sets
  `ENV=dev` and `INSECURE_SKIP_VERIFY=1`. Requests go to
  `https://<host>.127-0-0-1.nip.io`, so they use the real path: localhost →
  socat → LB IP (L2) → ingress-nginx → app.
- **Docs:**
  - new repo `CLAUDE.md` with the architecture decisions, roadmap, stable-version
    rule, branch model and `values-dev.yaml` convention
  - rewrite `docs/installation/sandbox.md` for kind: 12 GB, `make dev`, push to
    `origin dev`, optional CA import, `make dev-clean`

## Acceptance criteria (on branch `dev`)

1. `make dev` succeeds from scratch, and succeeds again when run a second time.
2. `cilium status` is OK; the ingress-nginx Service has external IP
   `172.30.255.200`.
3. All ArgoCD Applications in the allowlist are Synced and Healthy.
4. Prometheus has no down targets for kubelet, apiserver, etcd, scheduler,
   controller-manager and ingress-nginx.
5. The smoke test passes.
6. `https://draw.127-0-0-1.nip.io` and `https://argocd.127-0-0-1.nip.io` open in
   the Windows browser.
7. `make dev-clean` leaves no cluster, proxy container or `kind` network.
8. Plain `make` (prod) targets and prod values are unchanged in behaviour, apart
   from chart and Cilium version bumps.

## Risks

- **WSL localhost forwarding** for ports below 1024 bound by a container on the
  host network is assumed to work. It is verified early in the plan; if it
  fails, the fallback is binding the socat proxy to `0.0.0.0`.
- **Cilium L2 announcements inside kind** need the right `devices`/interface
  settings. These are verified with an ARP check from the WSL host.
- **Chart bumps** may need value migrations (especially kube-prometheus-stack
  and argo-cd). One commit per chart keeps failures easy to locate.

## Out of scope

- 1b: external-secrets, global-secrets, kanidm, dex, Grafana, zot, CoreDNS
  rewrite of `*.127-0-0-1.nip.io`, `scripts/hacks` changes
- Gitea, Woodpecker, Renovate and Loki in the sandbox
- Gateway API (sub-project 2), kubeadm and Ubuntu for production
  (sub-project 3)
