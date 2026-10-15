# Homelab (fork of khuedoan/homelab)

This fork diverges from upstream. It follows the direction of upstream's `nixos` rewrite (PR #197: NixOS + k3s nodes, Istio, OpenBao, Forgejo, `infra/` with OpenTofu/Terragrunt, `apps/*.yaml`) so its code can be reused; match that layout when a step restructures something.

## Roadmap

Each step changes one thing, with its own spec and plan in `docs/superpowers/`.

0. Workstation: NixOS-WSL (done, outside this repo)
1. Sandbox: 1a kind + Cilium foundation (done), 1b identity: secrets, Kanidm via kaniop, Dex (done, Dex removed in step 5a), 1c SSO for Grafana, ArgoCD and zot (done)
2. Replace ingress-nginx and `Ingress` objects with Gateway API (Cilium Gateway), tested in the sandbox (done)
3. Bump every image to its latest stable release (done)
4. Apps: compact `apps/*.yaml` (app-template values + ApplicationSet, like upstream `nixos`), keep all apps (done; opencloud moved to step 5)
5. Identity: 5a drop Dex, apps use Kanidm OIDC directly, SSO for Actual Budget and paperless (done); 5b Forgejo replaces Gitea (done); 5c add opencloud (Kanidm OIDC, in the sandbox) (done)
6. Network: 6a sandbox on k3d with k3s defaults (Flannel, kube-proxy, ServiceLB, network policy), Cilium dropped, Istio serves the Gateways (done); 6b Istio ambient mesh with strict mTLS (done); 6c default-deny AuthorizationPolicy and NetworkPolicy with explicit allows
7. Infra: `infra/` with OpenTofu + Terragrunt (environments sandbox, staging, production), absorbing `metal/` bootstrap and `external/`
8. Secrets: OpenBao behind ESO (`PushSecret` for generated secrets, `ExternalSecret` to read), bootstrap secrets (unseal key, restic, Cloudflare) from OpenTofu, state on Cloudflare R2 with OpenTofu state encryption
9. Git and CI: Forgejo is the source of truth (push mirror to GitHub), Woodpecker, Renovate in the cluster; their Forgejo credentials live in OpenBao
10. Backups: VolSync declared per app, automatic restore on a fresh install (PVC `dataSourceRef`), plus an option for a truly fresh install that never prunes the old repository
11. Nodes: NixOS + k3s + kube-vip + Istio (PXE with upstream's `nixie`, `nixos-anywhere`); staging on NixOS VMs in WSL (KVM; stop the sandbox first), then production hardware

## Rules

- Use the latest stable release of every tool, chart and container image, pinned exactly (no `latest`; pin a digest when an image has no version tags). The versions in this repo are not a reference; check upstream when touching something. Hold a version back only for a compatibility constraint, and say which one next to the pin.
- Branches: create feature branches from `dev` and open PRs into `dev`; `dev` is merged into `master` through periodic PRs. Never commit straight to `dev` or `master`.
- The sandbox only syncs `dev`. On a feature branch run the basic checks (`scripts/render-chart <dir>` and `scripts/render-chart <dir> dev` for every changed chart or `apps/<name>.yaml`, `cd test && go vet ./... && go test -run 'TestSmokeAppsForEnv|TestIdentity(HasGroup|TOTPWaitsForNextStep|PKCE)|TestGatewayRouteReady|TestGatewayNodeAddressFromInspect' ./...` (the other tests need a cluster), `mkdocs build --strict`, `scripts/check-mesh-exclusions`); the full sandbox tests (`make dev`) run after the PR is merged into `dev`. If `dev` breaks, fix it with a new PR right away.
- Sandbox config lives next to production config and is selected with `env=dev`. Never fork config into a separate branch or directory.

## Sandbox

- `make dev` builds it, `make dev-clean` removes it. Pages: `https://<app>.127-0-0-1.nip.io`.
- ArgoCD in the sandbox syncs from `github.com/duongle-steven/homelab` branch `dev`; push to see changes.
- Components synced in the sandbox are listed in `system/argocd/values-dev.yaml` (the `root` and `apps` ApplicationSets).
- Apps are app-template values files `apps/<name>.yaml`, deployed by the ApplicationSet `apps`, which pins app-template (in its source in `system/argocd/values.yaml`, restated in `values-dev.yaml` and `values-seed.yaml` because Helm replaces lists) and passes `global.domain` and `global.publicGateway` (`public` in production, `gateway` in the sandbox). Write hosts as `<host>.{{ .Values.global.domain }}`. `apps/matrix/` is the only app chart.
- A system or platform component that needs different settings in the sandbox gets a `values-dev.yaml` next to its `values.yaml`. It is loaded only in the sandbox.
- Helm merges values files but cannot delete a key set in `values.yaml` (`null` only removes chart defaults); override with an inert value instead.
- `scripts/render-chart <dir|apps/<name>.yaml> [dev]` renders a component or app the way ArgoCD does.
- k3d cluster `homelab-dev` (one k3s server, `metal/k3d-dev.yaml`; k3s's packaged Gateway API CRDs are disabled, `metal/` installs the latest). ServiceLB serves the Gateway `istio-system/gateway` on the node; a localhost proxy (`metal/Makefile`, recreated by `make dev`) forwards `127.0.0.1:80/443/22` there for the browser and SSH, and the tests dial the node IP (`docker inspect`). k3d's own port publishing is not used: Windows cannot reach it under WSL mirrored networking.
- Certificates come from a local CA (`homelab-dev-ca` in namespace `cert-manager`), issued under the production issuer name `letsencrypt-prod`.
- Identity config is in git: groups and OAuth2 clients are kaniop resources. Every app has its own `KanidmOAuth2Client` in its own namespace (a chart's `templates/kanidm-client.yaml`, or `rawResources.kanidm-client` in an app file); add the namespace to `oauth2ClientNamespaces` in `platform/kanidm/values.yaml`. Apps read the kaniop Secret `<client>-kanidm-oauth2-credentials` (`CLIENT_SECRET`); Kanidm requires PKCE. People are created with `scripts/onboard-user`; never put personal data in the repo. Production `KanidmGroup`s must not set `members` (it replaces the whole membership).
- In the sandbox, the ConfigMap `kube-system/coredns-custom` (from `system/istio-system`) makes k3s's CoreDNS rewrite `*.127-0-0-1.nip.io` to `gateway-istio.istio-system` so pods can reach Kanidm.
- Kanidm has a sandbox-only client `smoke` used by the identity tests (`make dev` runs `Smoke|Identity|SSO|Gateway|Forgejo|Mesh`). Admin tools (Grafana, Argo CD, zot, Forgejo) get short role group names in the claim `homelab_groups` from their client's `claimMap` and match `homelab-admins|editors|users`. Apps with personal data admit only an access group through `scopeMap`: `homelab-budget` (Actual Budget), `homelab-documents` (paperless; `homelab-admins` become superusers; a start script in `apps/paperless.yaml` gives the paperless group `homelab-documents` its permissions, and Kanidm groups replace paperless groups at each login), `homelab-files` (OpenCloud, public client `opencloud`; its `proxy.yaml` ConfigMap maps the claim `homelab_groups` to roles, `homelab-admins` → Admin, others → User; OpenCloud reads the claim from Kanidm's userinfo, not the access token).
- Apps trust the sandbox CA through the ConfigMap `homelab-dev-ca` (key `ca.crt`), which trust-manager copies into every namespace (sandbox only): Go apps (Grafana, Argo CD, zot, Forgejo) through `SSL_CERT_DIR` in `values-dev.yaml` (OpenCloud in its app file, only when `global.devCA` is set); app files mount it as an optional volume and set `NODE_EXTRA_CA_CERTS`/`REQUESTS_CA_BUNDLE` only when the `apps` parameter `global.devCA` is set.
- Apps publish `HTTPRoute`s (Forgejo also a `TCPRoute` for SSH) on `istio-system/gateway` (LAN) or `istio-system/public` (Cloudflare Tunnel), served by Istio (`system/istio-system`, like upstream); there are no `Ingress` objects. `scripts/render-chart` renders Gateway API kinds as `v1`, like ArgoCD.
- Every namespace Argo CD manages joins Istio's ambient mesh with strict mTLS (label from the shared `managedNamespaceMetadata`; `istio-system` and `rook-ceph` get `none` from the `root` `templatePatch`; `kube-system` is not managed). Pods the API server calls (webhooks) must carry the pod label `istio.io/dataplane-mode: none`; `scripts/check-mesh-exclusions` checks every chart. Istio's `k3s` platform profile serves the k3d sandbox too (its `k3d` profile has the wrong CNI path).
- Forgejo (`platform/forgejo`) runs on SQLite in the sandbox and PostgreSQL (CloudNativePG `forgejo-db`, `platform/cloudnative-pg`, production only) in production; a seed Job copies `seed.repository` once into `ops/homelab` and never overwrites it, and production Argo CD syncs from there. The Forgejo chart keeps Gitea's names for its own keys (`gitea:`, `configure-gitea`); everything else is named `forgejo`.
- The first sync on a fresh cluster takes about 20 minutes: some apps wait for CRDs (ServiceMonitor, ESO, kaniop) and admission webhooks, then ArgoCD retries.

## Before the first production deploy (sub-project 3)

The sandbox runs Kubernetes 1.37 and only ever installs from scratch, so it does not prove these:

- k3s defaults (Flannel, kube-proxy, ServiceLB, network policy), the Gateway API CRDs through k3s's auto-deploy manifests (k3s's packaged ones disabled), kube-vip v1.2.4 serving class `kube-vip.io/kube-vip-class` and its addresses `192.168.1.224` (Gateway) and `192.168.1.225` (wireguard) were only rendered: the sandbox has no kube-vip. The node `metal3` is still a k3s agent (upstream makes every node a server).
- Major chart upgrades in sub-project 2 were only rendered, never run in production: app-template 2/3 → 5 (blog, jellyfin, ollama, paperless, cloudflared, and in step 4 tailscale and wireguard; PVC names kept with `suffix: data`), woodpecker 1 → 3, dendrite 0.13 → 0.14.
- App images jumped several majors (step 3) and only the sandbox apps ran: paperless-ngx 2 → 3 (consume script arguments and advanced DB settings changed), homepage 0.8 → 2 (`HOMEPAGE_ALLOWED_HOSTS` required; v2 adds optional homepage auth), Jellyseerr replaced by its successor Seerr (`ghcr.io/seerr-team/seerr`), jellyfin 10.8 → 10.11, ollama 0.1 → 0.40, redis 7 → 8.
- The public path is untested: Gateway `istio-system/public`, cloudflared `matchSNItoHost` to `public-istio.istio-system`, external-dns 1.23 with `gateway-httproute` and the `external-dns.kubernetes.io/` annotation prefix.
- k3s jumps 1.30 → 1.37; upgrade an existing cluster one minor at a time (Istio 1.31 supports Kubernetes 1.32–1.37).
- Charts with changed workload selectors cannot be upgraded in place: delete the Deployment once or sync with `Replace=true`. This covers every app-template 2/3 → 5 upgrade (excalidraw, actualbudget, blog, homepage, jellyfin, ollama, pairdrop, paperless, speedtest, cloudflared, tailscale, wireguard; selector `component` → `controller`). ollama's Deployment is also renamed `ollama` → `ollama-main` and only starts once the old pod releases the `ollama-data` volume. wireguard's Service gains `loadBalancerClass` (kube-vip), which Kubernetes never changes on an existing Service, not even with `Replace=true`: delete the Service once.
- Run `make configure` to replace upstream's domain (`khuedoan.com`), seed repo, timezone and IP ranges. Until then, prod values still carry upstream's settings; the sandbox overrides them in `values-dev.yaml` (for apps: the `global.*` parameters of the `apps` ApplicationSet in `system/argocd/values-dev.yaml`).
- The ambient mesh on k3s, the CloudNativePG operator's opt-out and rook-ceph's exclusion were only rendered. Pods that start before istio-cni on a rebooted node are not redirected until they restart.
- OpenCloud only ran in the sandbox. `opencloud-config` holds the secrets `opencloud init` generated and the internal LDAP (accounts, shares); losing it loses them (backups in step 10). The desktop and mobile apps need their own public Kanidm clients.
- Woodpecker and Renovate read `forgejo.woodpecker` (`client_id`, `client_secret`) and `forgejo.renovate` (`token`) from `global-secrets`, which nothing creates yet: set them up (step 9) before deploying those apps.
- CloudNativePG and Forgejo on PostgreSQL were only rendered (the sandbox uses SQLite); backups of `forgejo-db` and `forgejo-data` come in step 10.
- Argo CD's switch to `ops/homelab` on Forgejo (`values.yaml`, `bootstrap.yml` detection, repository Secret `argocd-repo-forgejo` with `forgejo_admin`) is only rendered; the sandbox tests prove the seed and that the credential reads the repository.
- Argo CD has `exec.enabled: "true"` (needed for the editor role), so admins and editors can open terminals in pods.
- The Grafana chart now comes from `https://grafana-community.github.io/helm-charts`.
- zot refuses anonymous pulls: `apps/blog` (`registry.khuedoan.com/blog`) needs pull credentials (a read-only htpasswd account plus an `imagePullSecret`) or an anonymous `read` policy for its repository. `registry-admin-secret` (`admin`) no longer works: `admin` is not in zot's htpasswd.
- Forgejo does not start while Kanidm is unreachable: its init container registers the Kanidm login source at every start and needs Kanidm's discovery document. Argo CD syncs from Forgejo, so bring Kanidm back first after an outage.
- zot exits at start when Kanidm is unreachable (OIDC discovery), so a Kanidm outage plus a zot restart also stops `ci` and API-key pulls and pushes.
- The first person to log in to Actual Budget with OpenID becomes its server owner: add yourself to `homelab-budget` and log in first.
- Removing someone from Kanidm or a group does not revoke their zot API keys (zot keeps the groups from their last login); delete their keys in zot when offboarding.
- Argo CD editors may exec only into apps listed in `system/argocd/values.yaml` (`apps/*`); add new apps there.

## Ideas (not planned)

- VPN with kanidm users: Headscale with OIDC to kanidm instead of per-device WireGuard keys and third-party Tailscale accounts. Needs kanidm reachable from the internet.
