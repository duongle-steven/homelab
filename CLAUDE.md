# Homelab (fork of khuedoan/homelab)

This fork diverges from upstream. It follows the direction of upstream's `nixos` rewrite (PR #197: NixOS + k3s nodes, Istio, OpenBao, Forgejo, `infra/` with OpenTofu/Terragrunt, `apps/*.yaml`) so its code can be reused; match that layout when a step restructures something.

## Roadmap

Each step changes one thing, with its own spec and plan in `docs/superpowers/`.

0. Workstation: NixOS-WSL (done, outside this repo)
1. Sandbox: 1a kind + Cilium foundation (done), 1b identity: secrets, Kanidm via kaniop, Dex (done), 1c SSO for Grafana, ArgoCD and zot (done)
2. Replace ingress-nginx and `Ingress` objects with Gateway API (Cilium Gateway), tested in the sandbox (done)
3. Bump every image to its latest stable release
4. Apps: compact `apps/*.yaml` (app-template values + ApplicationSet, like upstream `nixos`), keep all apps, add opencloud
5. Identity: drop Dex, apps use Kanidm OIDC directly; Forgejo replaces Gitea
6. Network: sandbox on k3d with k3s defaults (Flannel, kube-proxy, ServiceLB, network policy); drop Cilium; Istio ambient (Gateway API, mesh, mTLS)
7. Infra: `infra/` with OpenTofu + Terragrunt (environments sandbox, staging, production), absorbing `metal/` bootstrap and `external/`
8. Secrets: OpenBao behind ESO (`PushSecret` for generated secrets, `ExternalSecret` to read), bootstrap secrets (unseal key, restic, Cloudflare) from OpenTofu, state on Cloudflare R2 with OpenTofu state encryption
9. Git and CI: Forgejo is the source of truth (push mirror to GitHub), Woodpecker, Renovate in the cluster; their Forgejo credentials live in OpenBao
10. Backups: VolSync declared per app, automatic restore on a fresh install (PVC `dataSourceRef`), plus an option for a truly fresh install that never prunes the old repository
11. Nodes: NixOS + k3s + kube-vip + Istio (PXE with upstream's `nixie`, `nixos-anywhere`); staging on NixOS VMs in WSL (KVM; stop the sandbox first), then production hardware

## Rules

- Use the latest stable release of every tool, chart and container image, pinned exactly (no `latest`; pin a digest when an image has no version tags). The versions in this repo are not a reference; check upstream when touching something. Hold a version back only for a compatibility constraint, and say which one next to the pin.
- Branches: create feature branches from `dev` and open PRs into `dev`; `dev` is merged into `master` through periodic PRs. Never commit straight to `dev` or `master`.
- The sandbox only syncs `dev`. On a feature branch run the basic checks (`scripts/render-chart <dir>` and `scripts/render-chart <dir> dev` for every changed chart or `apps/<name>.yaml`, `cd test && go vet ./... && go test -run 'TestSmokeAppsForEnv|TestIdentity(HasGroup|TOTPWaitsForNextStep)|TestGatewayRouteReady' ./...` (the other tests need a cluster), `mkdocs build --strict`); the full sandbox tests (`make dev`) run after the PR is merged into `dev`. If `dev` breaks, fix it with a new PR right away.
- Sandbox config lives next to production config and is selected with `env=dev`. Never fork config into a separate branch or directory.

## Sandbox

- `make dev` builds it, `make dev-clean` removes it. Pages: `https://<app>.127-0-0-1.nip.io`.
- ArgoCD in the sandbox syncs from `github.com/duongle-steven/homelab` branch `dev`; push to see changes.
- Components synced in the sandbox are listed in `system/argocd/values-dev.yaml` (the `root` and `apps` ApplicationSets).
- Apps are app-template values files `apps/<name>.yaml`, deployed by the ApplicationSet `apps`, which pins app-template once and passes `global.domain` and `global.publicGateway` (`homelab-public` in production, `homelab` in the sandbox). Write hosts as `<host>.{{ .Values.global.domain }}`. `apps/matrix/` is the only app chart.
- A system or platform component that needs different settings in the sandbox gets a `values-dev.yaml` next to its `values.yaml`. It is loaded only in the sandbox.
- Helm merges values files but cannot delete a key set in `values.yaml` (`null` only removes chart defaults); override with an inert value instead.
- `scripts/render-chart <dir|apps/<name>.yaml> [dev]` renders a component or app the way ArgoCD does.
- Fixed addresses: Docker network `kind` 172.30.0.0/16, LB pool 172.30.255.200/29, Gateways `homelab` 172.30.255.200 and `homelab-public` 172.30.255.201.
- Certificates come from a local CA (`homelab-dev-ca` in namespace `cert-manager`), issued under the production issuer name `letsencrypt-prod`.
- Identity config is in git: groups and OAuth2 clients are kaniop resources (`platform/kanidm`, `platform/dex/templates/kanidm-client.yaml`). People are created with `scripts/onboard-user`; never put personal data in the repo. Production `KanidmGroup`s must not set `members` (it replaces the whole membership).
- In the sandbox, CoreDNS rewrites `*.127-0-0-1.nip.io` to the `homelab` Gateway (`cilium-gateway-homelab.gateway`) so pods can reach Kanidm and Dex.
- Dex has a sandbox-only client `smoke` used by the identity tests (`make dev` runs `Smoke|Identity|SSO|Gateway`). Kanidm sends short group names in `homelab_groups`, which Dex maps to `groups`; apps match `homelab-admins|editors|users`.
- Go apps (Grafana, Argo CD, zot) and Dex trust the sandbox CA through the ConfigMap `homelab-dev-ca` (key `ca.crt`), which trust-manager copies into every namespace (sandbox only; `SSL_CERT_DIR` for the Go apps, set only in `values-dev.yaml`).
- Apps publish `HTTPRoute`s (Gitea also a `TCPRoute`) on `gateway/homelab` (LAN) or `gateway/homelab-public` (Cloudflare Tunnel); there are no `Ingress` objects. `scripts/render-chart` renders Gateway API kinds as `v1`, like ArgoCD.
- Dex reads client secrets from env at start: after adding a Dex static client to a running cluster, `kubectl -n dex rollout restart deploy/dex`.
- The first sync on a fresh cluster takes about 20 minutes: some apps wait for CRDs (ServiceMonitor, ESO, kaniop) and admission webhooks, then ArgoCD retries.

## Before the first production deploy (sub-project 3)

The sandbox runs Kubernetes 1.36 and only ever installs from scratch, so it does not prove these:

- The Gateway API CRDs are installed by `metal/` before Cilium; on an existing cluster, `cilium-operator` must restart after they are installed (`metal/` restarts Cilium whenever its Helm release changes).
- Major chart upgrades in sub-project 2 were only rendered, never run in production: app-template 2/3 → 5 (blog, jellyfin, ollama, paperless, cloudflared; PVC names kept with `suffix: data`), gitea 10 → 12 (`postgresql-ha`, `valkey-cluster`), woodpecker 1 → 3, dendrite 0.13 → 0.14.
- App images jumped several majors (step 3) and only the sandbox apps ran: paperless-ngx 2 → 3 (consume script arguments and advanced DB settings changed), homepage 0.8 → 2 (`HOMEPAGE_ALLOWED_HOSTS` required; v2 adds optional homepage auth), Jellyseerr replaced by its successor Seerr (`ghcr.io/seerr-team/seerr`), jellyfin 10.8 → 10.11, ollama 0.1 → 0.40, redis 7 → 8.
- The public path is untested: Gateway `homelab-public`, cloudflared `matchSNItoHost` to `cilium-gateway-homelab-public.gateway`, external-dns 1.23 with `gateway-httproute` and the `external-dns.kubernetes.io/` annotation prefix.
- Cilium jumped 1.16 → 1.20; upgrade an existing cluster one minor at a time, and only on a Kubernetes version Cilium 1.20 supports (1.33–1.36). The current k3s 1.30 is too old.
- Charts with changed workload selectors cannot be upgraded in place: delete the Deployment once or sync with `Replace=true`. This covers every app-template 2/3 → 5 upgrade (excalidraw, actualbudget, blog, homepage, jellyfin, ollama, pairdrop, paperless, speedtest, cloudflared; selector `component` → `controller`). ollama's Deployment is also renamed `ollama` → `ollama-main` and only starts once the old pod releases the `ollama-data` volume.
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
