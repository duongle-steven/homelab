# Homelab (fork of khuedoan/homelab)

This fork diverges from upstream. Selected upstream changes are still cherry-picked, so keep the `apps/<name>/{Chart.yaml,values.yaml}` layout.

## Roadmap

Each step changes one thing, with its own spec and plan in `docs/superpowers/`.

0. Workstation: NixOS-WSL (done, outside this repo)
1. Sandbox: 1a kind + Cilium foundation, 1b platform (external-secrets, kanidm, dex, Grafana, zot)
2. Replace ingress-nginx and `Ingress` objects with Gateway API (Cilium Gateway), tested in the sandbox
3. Production: kubeadm instead of k3s, Ubuntu LTS instead of Fedora (rewrite `metal/`)

## Rules

- Use the current stable release of every tool and chart, pinned exactly. The versions in this repo are not a reference; check upstream when touching something.
- Branch `dev` is the integration branch: changes land there, are tested in the sandbox, then merged into `master`.
- Sandbox config lives next to production config and is selected with `env=dev`. Never fork config into a separate branch or directory.

## Sandbox

- `make dev` builds it, `make dev-clean` removes it. Pages: `https://<app>.127-0-0-1.nip.io`.
- ArgoCD in the sandbox syncs from `github.com/duongle-steven/homelab` branch `dev`; push to see changes.
- Components synced in the sandbox are listed in `system/argocd/values-dev.yaml`.
- A component that needs different settings in the sandbox gets a `values-dev.yaml` next to its `values.yaml`. It is loaded only in the sandbox.
- Helm merges values files but cannot delete a key set in `values.yaml` (`null` only removes chart defaults); override with an inert value instead.
- `scripts/render-chart <dir> [dev]` renders a component the way ArgoCD does.
- Fixed addresses: Docker network `kind` 172.30.0.0/16, LB pool 172.30.255.200/29, ingress 172.30.255.200.
- Certificates come from a local CA (`homelab-dev-ca` in namespace `cert-manager`), issued under the production issuer name `letsencrypt-prod`.
- The first sync on a fresh cluster takes about 10 minutes: some apps wait for the ServiceMonitor CRDs and admission webhooks, then ArgoCD retries.

## Before the first production deploy (sub-project 3)

The sandbox runs Kubernetes 1.36 and only ever installs from scratch, so it does not prove these:

- Cilium jumped 1.16 → 1.20; upgrade an existing cluster one minor at a time, and only on a Kubernetes version Cilium 1.20 supports (1.33–1.36). The current k3s 1.30 is too old.
- Charts with changed workload selectors (app-template 2 → 5 for excalidraw) cannot be upgraded in place: delete the Deployment once or sync with `Replace=true`.
- Run `make configure` to replace upstream's domain (`khuedoan.com`), seed repo, timezone and IP ranges. Until then, prod values still carry upstream's settings; the sandbox overrides them in `values-dev.yaml`.
- kanidm CLI in the dev shell is 1.11; bump `platform/kanidm` to match before using `scripts/onboard-user`.

## Ideas (not planned)

- VPN with kanidm users: Headscale with OIDC to kanidm instead of per-device WireGuard keys and third-party Tailscale accounts. Needs kanidm reachable from the internet.
