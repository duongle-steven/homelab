# Sub-project 2: Gateway API with Cilium Gateway

Status: design approved in chat 2026-10-13, written spec awaiting review. Branch: `feat/gateway-api` (PR into `dev`).

## Goal

Every component is reached through Gateway API `Gateway`s served by Cilium: `homelab` for the LAN and
`homelab-public` for the apps published through the Cloudflare Tunnel, sharing one wildcard
certificate. ingress-nginx (retired, no security fixes since 2026-03) and every `Ingress` object are
removed. The sandbox proves the result, now with five more apps, including gitea and its SSH over
`TCPRoute`.

## Context

- Builds on sub-project 1 (kind + Cilium sandbox, identity, SSO). Specs:
  `2026-10-12-kind-cilium-sandbox-design.md`, `2026-10-12-sandbox-identity-design.md`,
  `2026-10-13-sandbox-sso-design.md`.
- **Cilium** is installed by Ansible (`metal/roles/cilium`, 1.20.2) for both the sandbox and
  production, with LB IPAM and L2 announcements. Gateway API is not enabled. Cilium 1.20 implements
  Gateway API v1.6.1, including `HTTPRoute`, `TCPRoute` and `BackendTLSPolicy`.
- **ingress-nginx** (`system/ingress-nginx`, chart 4.15.1) holds the sandbox LB IP `172.30.255.200`
  (`values-dev.yaml`) and forwards TCP 22 to gitea SSH.
- **Ingress objects** exist in 17 components. Six run in the sandbox (argocd, excalidraw, kanidm, dex,
  grafana, zot); the rest run only in production.
- **nginx-specific settings:** zot `proxy-body-size: "0"`, kanidm `backend-protocol: HTTPS` (Kanidm
  serves TLS itself and reuses the ingress certificate secret).
- **Certificates:** every Ingress carries `cert-manager.io/cluster-issuer: letsencrypt-prod`.
  Production issues through ACME DNS-01 on Cloudflare; the sandbox issues from the local CA under the
  same issuer name.
- **Around the ingress:**
  - the sandbox socat proxy forwards `127.0.0.1:80/443` to `172.30.255.200`
  - the sandbox CoreDNS rewrite (`metal/inventories/dev.yml`, `coredns_rewrite_rule`) points
    `*.127-0-0-1.nip.io` at `ingress-nginx-controller.ingress-nginx`; the role only adds the rule when
    that exact text is missing
  - cloudflared (production) forwards `*.khuedoan.com` to `https://ingress-nginx-controller.ingress-nginx`
    with `noTLSVerify`
  - external-dns (production, chart 1.14.3) uses the default sources (`service`, `ingress`)
  - three apps are public today (excalidraw, blog, matrix): their Ingress annotation
    `external-dns.alpha.kubernetes.io/target: homelab-tunnel.khuedoan.com` makes external-dns point
    their DNS at the tunnel; every other host points at the LAN ingress IP
  - external-dns reads `target` only from a `Gateway`, never from a route (gateway sources docs), and
    since v0.22 uses the annotation prefix `external-dns.kubernetes.io/` (the `alpha` prefix is legacy)
- **Tests:** `TestSmoke` and `integration_test.go` read hosts from `Ingress` objects
  (`k8s.GetIngress`).
- **Charts and route support** (latest versions checked 2026-10-13):

  | Component | Chart now → latest | Native Gateway API support |
  |---|---|---|
  | argocd | argo-cd 10.10.3 (latest) | `server.httproute` |
  | dex | dex 0.26.0 (latest) | `httpRoute` |
  | grafana | grafana 13.5.0 → 13.5.1 | `route` |
  | zot | zot 0.1.128 (latest) | `httproute` |
  | kanidm | kaniop 0.18.0 → 0.19.0 | `Kanidm` CR `gateway`, with `backendTlsPolicy` |
  | excalidraw | app-template 5.3.0 (latest) | `route` |
  | actualbudget, blog, homepage, jellyfin, ollama, pairdrop, paperless, speedtest | app-template 2.2–2.6 → 5.3.0 | `route` |
  | gitea | gitea 10.1.3 → 12.7.0 | `gatewayAPI.core.httpRoute`, `tcpRoute`, `backendTLSPolicy` |
  | woodpecker | woodpecker 1.5.1 → 3.7.5 | none |
  | matrix | elementweb 0.0.6 (latest), dendrite 0.13.5 | none |

- **Sandbox memory:** about 6 GiB used of the 12 GiB WSL VM.

## Decisions

| Topic | Decision |
|---|---|
| Scope | Migrate every component, including the ones that run only in production; delete `system/ingress-nginx` |
| Topology | Two `Gateway`s in namespace `gateway`: `homelab` (LAN, DNS → its LB IP) and `homelab-public` (DNS → Cloudflare Tunnel, cloudflared → its Service); one shared wildcard certificate; apps attach `HTTPRoute`s to one of them |
| Public apps | excalidraw, blog and matrix attach to `homelab-public`; every other app to `homelab`. In the sandbox, excalidraw attaches to `homelab` (no tunnel) |
| external-dns | Chart 1.23.0, source `gateway-httproute`, annotation prefix `external-dns.kubernetes.io/` everywhere (including the exclude filter) |
| Delivery | Gateway API CRDs and the Cilium `gatewayAPI` setting in `metal/` (Ansible, before Cilium); the `Gateway` in a new ArgoCD component `system/gateway`; routes in each app's own values |
| Chart versions | Upgrade every touched chart to its latest stable and use its native route support; write `templates/httproute.yaml` only where a chart has none (woodpecker, matrix) |
| Major upgrades | app-template 2/3 → 5, gitea 10 → 12, woodpecker 1 → 3; each listed in the CLAUDE.md production checklist |
| Sandbox apps | Add `pairdrop`, `speedtest`, `actualbudget`, `homepage` and `gitea` (SQLite, in-memory cache, no Dex login) |
| Kanidm backend TLS | `HTTPRoute` plus `BackendTLSPolicy` from the kaniop `Kanidm` CR; Kanidm gets its own `Certificate` (`tlsSecretName: kanidm-tls`). Production validates with `wellKnownCACertificates: System`, the sandbox with the dev CA ConfigMap (Cilium 1.20.2 accepts only one ConfigMap with key `ca.crt`, no Secret) |
| Dev CA distribution | trust-manager v0.25.0, sandbox only (conditional dependency of `system/cert-manager`), copies `homelab-dev-ca` into a ConfigMap `homelab-dev-ca` (key `ca.crt`) in every namespace. Used by Kanidm's `BackendTLSPolicy` and by the dev CA mounts of dex, grafana, zot and argocd, which today read `ca.crt` from their own ingress certificate secrets |
| gitea SSH | `TCPRoute` on the Gateway listener `ssh` (port 22) |
| Versions | Re-check and pin exactly while planning (Gateway API CRDs, Cilium, every chart) |

## Design

### 1. Gateway infrastructure

- **`metal/` (sandbox and production):**
  - Install the Gateway API CRDs, standard channel, at the version the chosen Cilium release
    supports (v1.6.1 for 1.20), before Cilium. Cilium's operator enables its Gateway controller only
    when the CRDs exist at start; on an existing cluster, restart the operator once after installing
    the CRDs.
  - `metal/roles/cilium` adds `gatewayAPI.enabled: true`.
- **`system/gateway`** (new ArgoCD component, wrapper chart with templates only):
  - `Gateway` `homelab-public`, `gatewayClassName: cilium`, annotation
    `external-dns.kubernetes.io/target: homelab-tunnel.<domain>`, one listener `https` (port 443,
    hostname `*.<domain>`, same wildcard secret, routes from all namespaces). No HTTP or SSH listener:
    Cloudflare and cloudflared only speak HTTPS to it. Its address comes from the LB pool, also in the
    sandbox.
  - `Gateway` `homelab`, `gatewayClassName: cilium`, listeners:
    - `http`, port 80, routes from namespace `gateway` only
    - `https`, port 443, hostname `*.<domain>`, TLS terminate with secret `homelab-wildcard-tls`,
      routes from all namespaces
    - `ssh`, port 22, TCP, routes from namespace `gitea` only
  - `HTTPRoute` `https-redirect` on listener `http`: `RequestRedirect` to `https` with status 301 for
    every host.
  - `Certificate` `homelab-wildcard` for `*.<domain>`, issuer `letsencrypt-prod`, secret
    `homelab-wildcard-tls`. No cert-manager gateway-shim.
  - `values.yaml` holds the domain (`khuedoan.com` until `make configure` in sub-project 3);
    `values-dev.yaml` sets `127-0-0-1.nip.io` and pins the address `172.30.255.200` (LB IPAM, the
    address ingress-nginx holds today).
- **Remove `system/ingress-nginx`** in the same change: the Gateway takes over its IP.
- **Sandbox allowlist** (`system/argocd/values-dev.yaml`): drop `system/ingress-nginx`, add
  `system/gateway`.

### 2. Routes per app

- **Common pattern:** remove the `ingress` block, its cert-manager annotation and its `tls` block;
  add one `HTTPRoute` with `parentRefs` to `gateway/homelab`, `sectionName: https`, and one hostname
  `<app>.<domain>`. Sandbox hostnames stay in `values-dev.yaml` (`<app>.127-0-0-1.nip.io`). Apps no
  longer declare certificates.
- **Native chart support:** argocd (`server.httproute`; `server.insecure: true` stays), dex
  (`httpRoute`), grafana (`route`), zot (`httproute`), gitea (`gatewayAPI.core.httpRoute` and
  `tcpRoute`), the app-template 5 apps (`route`).
- **kanidm:** kaniop 0.19 `Kanidm` CR `gateway` with `parentRefs`, hostname and `backendTlsPolicy`
  (validation hostname `auth.<domain>`; CA: system trust in production, the sandbox CA in the
  sandbox). A `Certificate` for `auth.<domain>` replaces the ingress certificate Kanidm serves today.
- **Dev CA trust (sandbox):** dex, grafana, zot and argocd mount the ConfigMap `homelab-dev-ca`
  instead of their former ingress certificate secrets; paths and `SSL_CERT_DIR` stay the same.
- **Own templates:** woodpecker and matrix (`elementweb`, `dendrite`) add `templates/httproute.yaml`
  to their wrapper charts.
- **zot:** large blob pushes must not be cut by a gateway timeout; set the route's `timeouts` so
  pushes are not limited, and verify with a 200 MB layer.
- **gitea:** `TCPRoute` to the SSH service on listener `ssh`. Sandbox `values-dev.yaml` uses SQLite
  and an in-memory cache, disables `postgresql-ha` and `valkey-cluster`, and configures no Dex login.
- **Public apps (excalidraw, blog, matrix):** `parentRefs` to `gateway/homelab-public`; the route
  keeps only `external-dns.kubernetes.io/cloudflare-proxied: "true"` (the target comes from the
  Gateway). excalidraw's `values-dev.yaml` points `parentRefs` at `gateway/homelab` and clears the
  annotation.
- **ArgoCD CLI:** document `argocd --grpc-web`; plain gRPC through the Gateway is not tested.
- **Sandbox additions:** `apps/pairdrop`, `apps/speedtest`, `apps/actualbudget`, `apps/homepage`,
  `platform/gitea` join the allowlist.

### 3. Around the Gateway

| Component | Change | Tested in the sandbox |
|---|---|---|
| socat proxy | none: the Gateway holds `172.30.255.200` | yes |
| CoreDNS rewrite | target `cilium-gateway-homelab.gateway.svc.cluster.local`; the role replaces an existing `*.127-0-0-1.nip.io` rewrite rule instead of only adding when missing | yes, through the identity and SSO tests |
| cloudflared | service `https://cilium-gateway-homelab-public.gateway` with `matchSNItoHost: true` (the wildcard listener needs a matching SNI); only public apps are reachable through the tunnel | no |
| external-dns | chart 1.23.0, source `gateway-httproute`, `policy: upsert-only` set explicitly (the old chart default; now required), `--annotation-filter=external-dns.kubernetes.io/exclude notin (true)` | no |
| cert-manager | wildcard and Kanidm certificates only; trust-manager and a `Bundle` `homelab-dev-ca` in the sandbox | yes |

The two untested rows are checked against upstream documentation while planning and listed in the
CLAUDE.md production checklist.

### 4. Tests, rollout, docs

- **Changed tests:**
  - `TestSmoke` and `integration_test.go` read hosts from `HTTPRoute`s and wait for the parent
    conditions `Accepted` and `ResolvedRefs`. The route name per chart is found while planning and
    recorded in `smokeApps`.
  - The sandbox smoke list adds pairdrop, speedtest, actualbudget, homepage and gitea.
  - Identity and SSO tests are unchanged; they now cover the CoreDNS rewrite and Kanidm's
    `BackendTLSPolicy`.
- **New sandbox tests** (run by `make dev`):
  - `http://` requests get a 301 to `https://`
  - `Gateway/homelab` is `Programmed` with address `172.30.255.200`; `Gateway/homelab-public` is
    `Programmed`
  - no `Ingress` objects and no `ingress-nginx` namespace remain
  - TCP to `172.30.255.200:22` returns gitea's SSH banner
  - a 200 MB layer pushes to zot through the Gateway
- **Rollout:**
  - One PR from `feat/gateway-api` into `dev`; removing ingress-nginx and adding the Gateway must land
    together.
  - Basic checks on the branch: `scripts/render-chart` (prod and dev) for every changed chart, the
    offline Go tests, `mkdocs build --strict`.
  - After the merge: `make dev` on the running sandbox (upgrade path), then
    `make dev-clean && make dev` (fresh install, about 25 minutes).
  - Measure sandbox memory; report to the user before going on if it exceeds about 8 GiB.
- **Docs:**
  - `docs/installation/sandbox.md`: app list, `--grpc-web`, Gateway instead of ingress-nginx.
  - Other pages under `docs/` that mention ingress-nginx or `Ingress`.
  - `CLAUDE.md` Sandbox section: Gateway instead of ingress-nginx.
  - `CLAUDE.md` production checklist: the major chart upgrades; the public path (`homelab-public`,
    cloudflared `matchSNItoHost`, external-dns 1.23 with `gateway-httproute` and the new annotation
    prefix) is untested; install the Gateway API CRDs before Cilium.

## Acceptance criteria (on branch `dev`)

1. No `system/ingress-nginx`, no `Ingress` in any rendered chart (prod and dev).
2. `make dev` passes on the upgraded sandbox and on a fresh one: Smoke (11 apps), Identity, SSO and
   the new Gateway tests.
3. `https://<app>.127-0-0-1.nip.io` opens from the Windows browser for every sandbox app.
4. Every changed chart renders for prod and dev with `scripts/render-chart`.
5. CLAUDE.md and docs describe the Gateway setup and the new production caveats.

## Risks

- **Major chart upgrades without sandbox coverage:** blog, jellyfin, ollama, paperless, matrix,
  woodpecker only render. Production is installed from scratch in sub-project
  3, so no in-place upgrade happens.
- **Envoy defaults differ from nginx:** timeouts, body size and header limits; the zot push test
  covers the largest known case.
- **Cilium Gateway on an existing cluster** needs an operator restart after the CRDs are installed;
  the fresh-install run covers the normal order.
- **gitea 12 in the sandbox** differs from production in its database (SQLite).
- **Memory:** five more apps; gitea is the largest unknown.

## Out of scope

- `apps/tailscale` and `apps/wireguard`: no ingress, not touched.
- Dex login for gitea, woodpecker and renovate tokens (production checklist, sub-project 3).
- `make configure` and the real domain (sub-project 3).
- gRPC routes, `ListenerSet`s, per-app certificates.
- The localhost socat stall (sub-project 1 finding).
