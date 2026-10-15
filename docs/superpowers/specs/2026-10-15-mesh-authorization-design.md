# Step 6c: default-deny ingress in the mesh

Status: design approved in chat 2026-10-15, written spec awaiting review. Branch: `feat/mesh-authorization` (PR into `dev`).

## Goal

Inside the cluster, a pod accepts connections only from callers that are explicitly allowed. Meshed pods get a
mesh-wide default-deny `AuthorizationPolicy` (enforced by ztunnel at L4, by SPIFFE identity, namespace and port)
plus one `allow` policy per namespace. Pods outside the mesh (webhooks, VPN routers) get `NetworkPolicy`s that
open only their service ports. A render check fails when a namespace, route backend or metrics port has no
matching allow. `make dev` passes all existing tests plus new authorization tests.

Egress (traffic to the internet) is out of scope: step **6d**.

## Context

- Step 6b: every namespace Argo CD manages is in Istio's ambient mesh with strict mTLS; out of the mesh:
  `istio-system`, `rook-ceph`, `kube-system`, and the pods labelled `istio.io/dataplane-mode: none` (webhooks:
  cert-manager, trust-manager, External Secrets, prometheus-operator, CloudNativePG; VPN routers: wireguard,
  tailscale). `scripts/check-mesh-exclusions` checks webhook targets and that chart NetworkPolicies let HBONE
  (TCP 15008) in.
- Meshed connections reach the destination pod on ztunnel's HBONE port 15008, so a `NetworkPolicy` cannot tell
  meshed callers or ports apart; `AuthorizationPolicy` (ztunnel, L4) can. ztunnel matches the destination
  **pod** port (after the Service), e.g. Forgejo SSH is Service port 22 but pod port 2222 (rootless image).
- Traffic observed by ztunnel on the sandbox (2026-10-15): the Gateway `gateway-istio` to every routed app
  (actualbudget 5006, argocd-server 8080, excalidraw 80, forgejo 3000 and 2222, homepage 3000, kanidm 8443,
  opencloud 9200, pairdrop 3000, paperless 8000, speedtest 3000, zot 5000); within `argocd` (controller and
  applicationset controller to repo-server 8081, server to redis 6379); kaniop to Kanidm 8443; Forgejo to
  itself; Prometheus to argocd-server 8083 and cert-manager 9402. Apps reach Kanidm and each other through the
  Gateway (their hostnames), not through Services.
- Production-only in-cluster calls found in the repo: Argo CD's repo server to `forgejo-http.forgejo:3000`
  (production syncs from Forgejo), Grafana to `loki.loki:3100`. Woodpecker and Renovate use Forgejo's hostname.
- ServiceMonitors/PodMonitors render in `argocd`, `cert-manager`, `monitoring-system`, `loki`, `cloudnative-pg`.
- Spike on the sandbox (2026-10-15): a `NetworkPolicy` opening only trust-manager's webhook port (6443) left its
  kubelet readiness probe (6060) working: k3s's network policy controller lets the node's probes in.

## Decisions

| Topic | Decision |
|---|---|
| Scope | Ingress only; egress in 6d |
| Granularity | Per namespace: same namespace any port; callers from other namespaces by identity and pod port |
| Where | Each chart owns its `allow` policy (`templates/authorization-policy.yaml`; app files: `rawResources`); the default deny lives in `system/istio-system` |
| Outside the mesh | `NetworkPolicy` per opted-out pod: its service port(s) from anywhere, metrics only from `monitoring-system` |
| Guard | `scripts/check-mesh-exclusions` becomes `scripts/check-mesh-policies` and also checks the allows |

## Design

### Default deny

`system/istio-system/templates/deny-all.yaml`: `AuthorizationPolicy` `deny-all` in `istio-system` (the root
namespace) with `spec: {}`: no meshed workload accepts anything unless an `allow` policy matches. Kubelet probes
stay exempt in ambient.

### `allow` per namespace

One `AuthorizationPolicy` `allow` per meshed namespace (`action: ALLOW`), with these rule kinds:

1. Same namespace: `from.source.namespaces: [<namespace>]`, any port.
2. Gateway: principal `cluster.local/ns/istio-system/sa/gateway-istio` (LAN) or
   `cluster.local/ns/istio-system/sa/public-istio` (Cloudflare Tunnel) to the pod ports the routes' backends
   resolve to. App files use the Gateway named by `global.publicGateway` for public routes.
3. Prometheus: principal `cluster.local/ns/monitoring-system/sa/monitoring-system-kube-pro-prometheus` to the
   ports of the namespace's ServiceMonitors and PodMonitors.
4. Named cross-namespace callers: Grafana (`cluster.local/ns/grafana/sa/grafana`) to Prometheus 9090 and Loki
   3100; Argo CD's repo server (`cluster.local/ns/argocd/sa/argocd-repo-server`) to Forgejo 3000; the
   CloudNativePG operator (outside the mesh, no identity) to the `forgejo-db` pods' port 8000, the only rule
   without a source.

| Namespace | Allowed from other namespaces |
|---|---|
| Apps behind `gateway` (actualbudget, homepage, jellyfin, ollama, opencloud, pairdrop, paperless, speedtest) | Gateway to their route ports |
| Apps behind `global.publicGateway` (blog, excalidraw; matrix Element) | that Gateway to their route ports; matrix also `gateway` to dendrite |
| argocd | Gateway 8080; Prometheus to its metrics ports |
| forgejo | Gateway 3000, 2222; Argo CD repo server 3000; CNPG operator `forgejo-db` 8000 |
| kanidm | Gateway 8443 |
| zot | Gateway 5000 |
| grafana | Gateway 3000 |
| monitoring-system | Grafana 9090; Prometheus to its own targets |
| loki (production) | Grafana 3100; Prometheus to its metrics |
| cert-manager, cloudnative-pg | Prometheus to their metrics |
| woodpecker (production) | Gateway to its route port |
| external-secrets, external-dns, cloudflared, kured, renovate, global-secrets, volsync-system | none (same namespace only) |

The ports are read from the rendered pods while implementing, not copied from this table; the render check is
the authority.

### Pods outside the mesh

A `NetworkPolicy` per opted-out pod, in its chart: ingress on its webhook (or VPN) port from anywhere (the API
server calls from node addresses), and its metrics port from namespace `monitoring-system` where a monitor scrapes
it. The VPN routers open their UDP port only.

### Render check

`scripts/check-mesh-exclusions` is renamed `scripts/check-mesh-policies` and, besides its current checks, fails
when, for every chart and app file in production and the sandbox:

- a meshed namespace has no `AuthorizationPolicy` `allow`;
- an `HTTPRoute`/`TCPRoute` backend's pod port is not allowed for the principal of the Gateway the route attaches to;
- a ServiceMonitor/PodMonitor port is not allowed for Prometheus;
- a pod outside the mesh has no `NetworkPolicy`.

Ports resolve from the rendered workloads (named `targetPort`s to container ports).

## Error handling

- A missing allow breaks the caller with connection resets; ztunnel logs `policy rejection` with both identities.
  The render check catches route, monitor and missing-namespace gaps before the merge; calls the check cannot
  see (in-cluster URLs in app config) are listed in this spec and covered by the sandbox tests where they exist.
- Production-only allows (Loki, Woodpecker, Argo CD to Forgejo, CNPG to its instances) are only rendered.

## Testing

- Offline: `scripts/check-mesh-policies` (production and sandbox renders), `go vet` and the offline tests,
  `mkdocs build --strict`.
- `test/mesh_test.go` (sandbox):
  - `TestMeshRejectsPlaintext` changes: the meshed pod in `default` is now refused (`000`); ztunnel logs
    `policy rejection` with `src.identity="spiffe://cluster.local/ns/default/sa/default"`, proving mTLS and the
    default deny. The plaintext pod stays refused.
  - `TestMeshAllowsSameNamespace`: a temporary curl pod in `excalidraw` reaches `excalidraw` (`200`); deleted
    afterwards.
  - `TestMeshPrometheusTargetsUp`: Prometheus reports no target with `up == 0`.
- Every existing sandbox test (smoke, identity, SSO, Gateway, Forgejo, mesh) passes after the merge on a fresh
  `make dev`, proving the allows the sandbox uses.

## Docs

- `docs/reference/architecture/networking.md`: default deny, the allow rule kinds, how a new app adds its policy.
- `CLAUDE.md`: 6c done and **6d** (egress) added to the roadmap; new apps need an `allow` policy, checked by
  `scripts/check-mesh-policies` (replacing `scripts/check-mesh-exclusions` in the basic checks); production: the
  production-only allows were only rendered.
