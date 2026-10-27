# Step 14d: Kiali

Status: designed 2026-10-27. Branch: `feat/kiali` from `dev`. The user asked to try Kiali in the cluster, before step 15,
both to learn it and to see the mesh while steps 15b and 15c change policies. They left the design to Claude; every
choice below not stated by the user is marked *(Claude's call)*.

## Goal

A Kiali that shows the mesh: which workloads talk to which, whether the Istio config is valid, and what the Gateways
serve. It is reachable on the LAN Gateway, for admins only, read-only, in the sandbox too.

## What the user decided (in chat)

- Kiali before security (step 15). The order is recorded in CLAUDE.md's roadmap.
- Read-only (`view_only_mode`), admins only, in the sandbox too.
- The graph shows L4 only inside the mesh: in ambient mode without waypoints, ztunnel reports TCP metrics, not HTTP.
  Waypoints for L7 are not part of this step.

## Design *(Claude's calls unless noted)*

1. **Chart.** `system/kiali`, namespace `kiali`. It wraps `kiali-server` from `https://kiali.org/helm-charts`, version
   2.33.0. That is the latest chart; Kiali's compatibility table tests Istio 1.31 with 2.31 and higher. The
   `kiali-server` chart is used, not the operator: one Deployment needs no CRD or operator. It is added to the `root`
   ApplicationSet in `system/argocd/values-dev.yaml`, so it runs in the sandbox, and to production's list.
2. **Sign-in.** Kiali's `openid` strategy with a confidential `KanidmOAuth2Client` `kiali` in namespace `kiali`.
   - Kiali 2.33 always sends PKCE, which Kanidm requires.
   - Only `homelab-admins` gets a scope (`scopeMap`), so nobody else can sign in.
   - Add `kiali` to `oauth2ClientNamespaces`. The client secret reaches Kiali from kaniop's Secret
     `kiali-kanidm-oauth2-credentials`. Kiali reads key `oidc-secret` from Secret `kiali` by default, so map it through
     the chart's own secret setting, or a small wrapper, whichever the chart supports.
   - `disable_rbac: true`. k3s's API server has no OIDC, so Kiali cannot act as the user. Kiali's service account then
     needs read-only cluster access, the chart's viewer role.
   - The sandbox trusts the dev CA as other Go apps do (`SSL_CERT_DIR` in `values-dev.yaml`).
3. **Read-only.** `deployment.view_only_mode: true`. No write role is rendered.
4. **Route.** HTTPRoute `kiali.<baseDomain>` on `istio-system/gateway`, the LAN Gateway only, never `public`.
5. **Data.**
   - Prometheus is `monitoring-system`'s (`prometheus-operated:9090`).
   - Tracing is off; Tempo is only an idea.
   - The Grafana link uses `grafana.<baseDomain>` for the browser. Kiali needs no Grafana API access; turn off what
     calls it if the chart would.
   - Istio's namespace is `istio-system`, and the root namespace is also `istio-system`.
6. **Istio metrics, new.** Today nothing scrapes Istio, so Kiali's graph would be empty. `system/istio-system` gains
   scrape config for:
   - ztunnel: TCP metrics between workloads (`/stats/prometheus` on 15020);
   - istiod: control plane health and config pushes (15014);
   - the Gateways' Envoy: L7 metrics for requests entering through `gateway` and `public`.

   Each one gets whatever lets Prometheus reach it under `deny-all`. These metrics stay useful without Kiali, for
   Grafana. Keep the series count small where the charts allow, since the sandbox's Prometheus is small.
7. **Mesh and network.**
   - Namespace `kiali` joins the mesh like every managed namespace.
   - Its `AuthorizationPolicy` `allow` admits the Gateway on Kiali's port and Prometheus on its metrics.
   - `monitoring-system`'s `allow` admits Kiali's service account for Prometheus queries.
   - Kiali also calls the API server and istiod (control plane status). Add whatever the renders cannot show to `EXTRA`
     in `scripts/check-mesh-policies`.
   - Egress: none to the internet.
8. **Secrets catalog.** The new Kanidm client's secret gets an entry in `cli/internal/rotate/catalog.yaml`, of kind
   `kaniop-client` like the other confidential clients, with Kiali as reader.
9. **Backup.** Kiali keeps no state and has no PVC.
10. **Test.** A sandbox test `TestKiali`, plus `test/scopes.yaml` entries for `system/kiali/**` and for the Istio
    scrape change. The test checks:
    - Kiali answers through the Gateway and redirects an anonymous visitor to Kanidm;
    - an admin signs in and Kiali's API answers (e.g. its namespaces and graph);
    - a non-admin cannot sign in;
    - Prometheus has ztunnel's TCP series, so the graph has data.

    Use the existing SSO test helpers (Kanidm sign-in like `TestSSO*`).

## Not in this step

- Waypoints (L7 inside the mesh), tracing, Kiali's write features, multi-cluster.
- Kiali through the Cloudflare Tunnel.

## Left to the builder

- The chart's exact keys for the secret mapping, the dev CA, and turning off Grafana calls.
- PodMonitor versus ServiceMonitor, and the scrape intervals and relabelling.
- Resources: requests sized for the sandbox.
