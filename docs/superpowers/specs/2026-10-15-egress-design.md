# Step 6d-2: default-deny egress

Status: design approved in chat 2026-10-15, written spec awaiting review. Branch: `feat/egress` (PR into `dev`).

## Goal

A pod reaches only the cluster, the Kubernetes API, the LAN Gateway and, when its namespace is listed, the
internet on the listed ports. Nothing reaches the rest of the LAN (the homelab network and the home network
behind it). A compromised app cannot scan the LAN or send data out unless its namespace already talks to the
internet. Hostname allowlists (Istio egress waypoint) are a possible later step, not this one.

## Context

- Step 6c: ingress inside the mesh is default-deny (`AuthorizationPolicy`); pods outside the mesh have ingress
  `NetworkPolicy`s. Egress is open.
- Step 6d-1: environment addresses come from `infra/<env>/cluster/config.json` as `global.addresses.*`; the
  ApplicationSets read it through a matrix generator.
- Kubernetes has no cluster-wide `NetworkPolicy`: every namespace needs its own.
- Spike on the sandbox (2026-10-15, k3s network policy controller with Istio ambient, a namespace with a
  `deny-all` egress policy):
  - Meshed pod to meshed pod (HBONE through ztunnel) and DNS for in-cluster names (answered by ztunnel's DNS proxy)
    still work: the egress policy does not see them.
  - Everything else is blocked: DNS for external names, the API server, istiod (outside the mesh), the Gateway
    pods, the internet, the node's other ports.
  - The API server needs the node address on port 6443 (the policy sees the address after kube-proxy's DNAT); the
    Service range on 443 is not enough.
  - The Gateway pods (namespace `istio-system`, label `gateway.networking.k8s.io/gateway-class-name: istio`) need
    their own rule.
  - `0.0.0.0/0` except the private ranges, port 443, reaches `https://github.com` and still blocks port 80 and the
    node's address.
- Node addresses: the API server rule opens the whole node network on port 6443 only (decided 2026-10-15), so no
  node list is needed; `hosts.json` comes with steps 7/11.

## Decisions

| Topic | Decision |
|---|---|
| Mechanism | `NetworkPolicy` (k3s's controller), by address and port |
| Baseline | One chart, `system/egress`, added as an extra source to every Application the `root` and `apps` ApplicationSets generate, rendered into that Application's namespace |
| Exceptions | `istio-system` and `rook-ceph` get no egress policies (the `root` `templatePatch`, like their mesh exclusion); `kube-system` is not managed |
| Internet allowances | One table in `system/egress/values.yaml`, keyed by namespace |
| API server | The node network from `config.json` (`network.gateway`/`network.prefix_length`), port 6443 |
| Sandbox node network | k3d's Docker network pinned to `172.28.0.0/16`, written into `infra/sandbox/cluster/config.json` |

## Design

### Baseline (every namespace)

`system/egress/templates/` renders, in `.Release.Namespace`:

- `deny-all-egress`: `podSelector: {}`, `policyTypes: [Egress]`.
- `allow-cluster`: to the pod range `10.42.0.0/16` and the Service range `10.43.0.0/16`, any port. This covers DNS
  (CoreDNS pods), the Gateway pods, pods outside the mesh and the Service addresses before DNAT. Who may enter
  which pod stays with step 6c's policies. These are k3s's defaults, written as chart constants, like upstream's
  `k3s-server.nix`.
- `allow-kube-api`: to the node network, TCP 6443. The chart computes the network address from
  `global.addresses.router` and `global.addresses.prefixLength` (`192.168.0.1`, 24 → `192.168.0.0/24`): Kubernetes
  warns on, and may reject, a CIDR with host bits set.
- `allow-gateway-vip`: to `global.addresses.gatewayVIP/32`, TCP 80, 443, 22, only when it is set. Traffic to a
  LoadBalancer address from inside the cluster is normally rewritten to the Gateway pod by kube-proxy; this rule
  keeps it working if it is not (production only, not provable in the sandbox).

The chart receives `global.addresses.*`, plus a new `global.addresses.prefixLength` (`network.prefix_length`),
through the same `valuesObject` as the component.

### Internet allowances (one table)

`system/egress/values.yaml`:

```yaml
# Namespaces that reach the internet (0.0.0.0/0 except the private ranges) and on which ports. Everything else
# reaches only the cluster, the Kubernetes API and the Gateway (templates/baseline.yaml).
internet:
  argocd: [{port: 443}]                                   # GitHub, Helm and OCI repositories
  cert-manager: [{port: 443}, {port: 53, protocol: UDP}, {port: 53}]  # ACME, Cloudflare API; DNS01 self-check
  external-dns: [{port: 443}]                             # Cloudflare API
  cloudflared: [{port: 443}, {port: 7844}, {port: 7844, protocol: UDP}]  # Cloudflare edge
  monitoring-system: [{port: 443}]                        # ntfy.sh relay
  forgejo: [{port: 443}]                                  # seed clone, mirrors
  renovate: [{port: 80}, {port: 443}]
  woodpecker: [{port: 80}, {port: 443}]                   # pipelines
  ollama: [{port: 443}]                                   # models
  matrix: [{port: 443}, {port: 8448}]                     # federation
  jellyfin: [{}]                                          # every port: torrents, indexers, Seerr
  tailscale: [{port: 443}, {protocol: UDP}]               # DERP; STUN and direct connections on any UDP port
# Node ports a namespace reaches besides the API server
nodes:
  monitoring-system: [{port: 9100}, {port: 10250}, {port: 10257}, {port: 10259}, {port: 2381}, {port: 10249}]
```

An entry without `port` opens every port (of its protocol; `{}` is every TCP and UDP port). `protocol` defaults to
TCP. The private ranges excluded from "internet": `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `100.64.0.0/10`,
`169.254.0.0/16`.

### Argo CD

- `root`: `template.spec.source` becomes `sources` with two entries: the component (as today) and `system/egress`
  (same repository and revision, the same `valuesObject`). The root generator excludes `system/egress` itself
  (`exclude: true`), in `values.yaml` and `values-seed.yaml` (the sandbox lists its directories explicitly).
  `values-dev.yaml` and `values-seed.yaml` restate `sources` (Helm replaces lists).
- `apps`: a third source, `system/egress`, in `values.yaml`, `values-dev.yaml` and `values-seed.yaml`.
- The `root` `templatePatch` drops the `system/egress` source for `istio-system` and `rook-ceph`.
- `valuesObject` gains `prefixLength: '{{ .network.prefix_length }}'` (`scripts/env-values` resolves it too).

### Sandbox

- `metal/k3d-dev.yaml`: the cluster's Docker network gets `subnet: 172.28.0.0/16`.
- `infra/sandbox/cluster/config.json`: `network.gateway` `172.28.0.1`, `prefix_length` 16 (VIPs stay empty).

## Error handling

- A namespace missing from the table loses the internet: its pods fail on external calls (timeouts). The sandbox
  tests cover the paths the sandbox uses (Argo CD and Forgejo's seed to GitHub); the rest of the table is only
  rendered, like step 6c's production-only allows.
- A missing scrape port shows as `up == 0` (`TestMeshPrometheusTargetsUp`).

## Testing

- `scripts/check-mesh-policies` (offline) also fails when:
  - an Application the ApplicationSets generate (other than `istio-system`, `rook-ceph`) lacks the `system/egress`
    source, in production, the sandbox and the seed;
  - the table names a namespace that no component or app file deploys into.
- `scripts/check-network-config` accepts the private ranges and `0.0.0.0/0` as written in `system/egress` (exact
  literals, not whole ranges).
- `test/egress_test.go` (sandbox):
  - `TestEgressBlocksInternet`: a temporary pod in `excalidraw` gets no answer from `https://github.com`.
  - `TestEgressBlocksNodePorts`: the same pod gets no answer from the k3d node's address on port 10250 but does
    reach the API server.
  - `TestEgressAllowsListedNamespaces`: a temporary pod in `argocd` reaches `https://github.com`.
- `make dev` passes every existing test (they exercise Kanidm through the Gateway, Argo CD and Forgejo to GitHub,
  Prometheus targets).

## Docs

- `docs/reference/architecture/networking.md`: egress section (baseline, table, what is blocked).
- `CLAUDE.md`: roadmap 6d-2 done; a new component or app that needs the internet adds its namespace to
  `system/egress/values.yaml`; production checklist: the table was only rendered for production-only
  namespaces, the Gateway VIP rule is unproven, VolSync movers (step 10) will need 443.
