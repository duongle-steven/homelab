# See the mesh with Kiali

Kiali (`system/kiali`) draws the mesh from Prometheus and checks Istio's config. Open `https://kiali.<domain>/kiali`
on the LAN (in the sandbox `https://kiali.127-0-0-1.nip.io/kiali`) and sign in with Kanidm.

- **Who:** only `homelab-admins`. Kanidm refuses everyone else (the client `kiali` gives no other group a scope).
- **Read-only:** Kiali shows config and traffic but changes nothing (`view_only_mode`; its service account can only
  read). Change the mesh in git.
- **Not public:** its route is on the LAN Gateway only, never the Cloudflare Tunnel.

## What the graph shows

- **Inside the mesh, L4 only:** the ambient mesh has no sidecars and no waypoints, so ztunnel reports TCP connections
  and bytes between workloads, not HTTP requests, codes or latency.
- **At the Gateways, L7:** requests entering through `gateway` and `public` are HTTP to the Gateways' Envoy, so the
  edges from `gateway-istio` carry request rates, codes and latency.
- **Istio config:** validation of `AuthorizationPolicy`, Gateway API routes and the rest, per namespace.

The data comes from Prometheus scraping ztunnel, istiod and the Gateways (`system/istio-system`'s PodMonitors), which
Grafana can also query.

## Not here

Traces (no Tempo), Kiali's write features, and L7 inside the mesh (it needs waypoints).
