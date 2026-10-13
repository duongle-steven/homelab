# Homelab (fork of khuedoan/homelab)

This fork diverges from upstream. Selected upstream changes are still cherry-picked, so keep the `apps/<name>/{Chart.yaml,values.yaml}` layout.

## Roadmap

Each step changes one thing, with its own spec and plan in `docs/superpowers/`.

0. Workstation: NixOS-WSL (done, outside this repo)
1. Sandbox: 1a kind + Cilium foundation (done), 1b identity: secrets, Kanidm via kaniop, Dex (done), 1c SSO for Grafana, ArgoCD and zot (done)
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
- Identity config is in git: groups and OAuth2 clients are kaniop resources (`platform/kanidm`, `platform/dex/templates/kanidm-client.yaml`). People are created with `scripts/onboard-user`; never put personal data in the repo. Production `KanidmGroup`s must not set `members` (it replaces the whole membership).
- In the sandbox, CoreDNS rewrites `*.127-0-0-1.nip.io` to ingress-nginx so pods can reach Kanidm and Dex.
- Dex has a sandbox-only client `smoke` used by the identity tests (`make dev` runs `Smoke|Identity|SSO`). Kanidm sends short group names in `homelab_groups`, which Dex maps to `groups`; apps match `homelab-admins|editors|users`.
- Go apps (Grafana, Argo CD, zot) trust the sandbox CA through `SSL_CERT_DIR` and their own ingress certificate's `ca.crt`, set only in `values-dev.yaml`.
- Dex reads client secrets from env at start: after adding a Dex static client to a running cluster, `kubectl -n dex rollout restart deploy/dex`.
- The first sync on a fresh cluster takes about 20 minutes: some apps wait for CRDs (ServiceMonitor, ESO, kaniop) and admission webhooks, then ArgoCD retries.

## Before the first production deploy (sub-project 3)

The sandbox runs Kubernetes 1.36 and only ever installs from scratch, so it does not prove these:

- Cilium jumped 1.16 → 1.20; upgrade an existing cluster one minor at a time, and only on a Kubernetes version Cilium 1.20 supports (1.33–1.36). The current k3s 1.30 is too old.
- Charts with changed workload selectors (app-template 2 → 5 for excalidraw) cannot be upgraded in place: delete the Deployment once or sync with `Replace=true`.
- Run `make configure` to replace upstream's domain (`khuedoan.com`), seed repo, timezone and IP ranges. Until then, prod values still carry upstream's settings; the sandbox overrides them in `values-dev.yaml`.
- `scripts/hacks` is gone (sub-project 1b). It used to create Gitea access tokens and OAuth apps that `platform/renovate` (`gitea.renovate`) and `platform/woodpecker` (`gitea.woodpecker`) read, and Gitea's Dex login. Recreate that setup (ideally as kaniop/Gitea config in git) before deploying those apps.
- Argo CD has `exec.enabled: "true"` (needed for the editor role), so admins and editors can open terminals in pods.
- The Grafana chart now comes from `https://grafana-community.github.io/helm-charts`.
- zot refuses anonymous pulls: `apps/blog` (`registry.khuedoan.com/blog`) needs pull credentials (a read-only htpasswd account plus an `imagePullSecret`) or an anonymous `read` policy for its repository. `registry-admin-secret` (`admin`) no longer works: `admin` is not in zot's htpasswd.
- zot exits at start when Dex is unreachable (OIDC discovery), so a Dex or Kanidm outage plus a zot restart also stops `ci` and API-key pulls and pushes.
- Removing someone from Kanidm or a group does not revoke their zot API keys (zot keeps the groups from their last login); delete their keys in zot when offboarding.
- Argo CD editors may exec only into apps listed in `system/argocd/values.yaml` (`apps/*`); add new apps there.

## Ideas (not planned)

- VPN with kanidm users: Headscale with OIDC to kanidm instead of per-device WireGuard keys and third-party Tailscale accounts. Needs kanidm reachable from the internet.
