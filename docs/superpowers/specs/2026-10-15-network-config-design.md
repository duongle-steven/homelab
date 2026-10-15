# Step 6d-1: network addresses from an environment config file

Status: design approved in chat 2026-10-15, written spec awaiting review. Branch: `feat/network-config` (PR into `dev`).

## Goal

No chart or app file holds an IP address. Every address that depends on where the homelab runs lives in one
config file per environment, in upstream's `nixos` format (`infra/<env>/cluster/config.json`), which Argo CD reads
now and OpenTofu will read in step 7. Someone else using the repo edits that file; no script rewrites the code.
Production gets this homelab's addresses (network `192.168.0.0/24`). Step 6d-2 (egress) builds on these parameters.

## Context

- Upstream `nixos` keeps per-environment settings in `infra/<env>/`: `metal/hosts.json` (nodes: MAC and IP, or a
  cloud node's IPv6, interface and disk) and `cluster/config.json` (`vip`, `network.prefix_length`,
  `network.gateway`); OpenTofu passes them to NixOS and Argo CD. Its only fixed address is the Kubernetes API VIP:
  the Gateway runs on k3s ServiceLB on the node addresses, and kube-vip has `svc_enable = "false"`.
- This fork also gives two Services their own kube-vip address (step 6a): the LAN Gateway (Forgejo SSH keeps
  port 22, which the nodes' sshd owns on the node addresses) and wireguard. Kept (decided 2026-10-15).
- Addresses today are upstream's (`192.168.1.x`), hard-coded in `system/istio-system/values.yaml` (Gateway),
  `system/monitoring-system/values.yaml` (node addresses for the control-plane scrape), `apps/wireguard.yaml`,
  `apps/tailscale.yaml`, `apps/homepage.yaml` (router link), `metal/inventories/prod.yml`, `scripts/configure`
  (rewrites the Gateway and wireguard addresses) and docs.
- The sandbox does not scrape the control plane (`system/monitoring-system/values-dev.yaml`: "the k3d node has no
  fixed address to scrape").
- This homelab's network (2026-10-15): router Archer C64 `192.168.0.1` behind the ISP router (`192.168.1.0/24`,
  home devices, nothing of the homelab); DHCP pool `.100-.249`. Chosen addresses: Service VIPs `.2-.9` (Gateway
  `.2`, wireguard `.3`), Kubernetes API `.10`, nodes `.11-.19` (now `.11-.13`).

## Decisions

| Topic | Decision |
|---|---|
| Source of truth | `infra/<env>/cluster/config.json`, upstream's format plus `service_vips` |
| Reader now | The `root` and `apps` ApplicationSets (matrix with a git files generator), passing Helm parameters `global.network.*` |
| Reader later | OpenTofu in step 7 reads the same file |
| Service VIPs | Kept: Gateway and wireguard |
| Control-plane scrape | Prometheus discovers the nodes (`ScrapeConfig`, Kubernetes node discovery), no address list |
| Node list | Not in this step: nothing reads it yet (`metal/hosts.json` comes with step 7 or 11); Ansible keeps its inventory |

## Design

### Config file

`infra/production/cluster/config.json`:

```json
{
  "vip": "192.168.0.10",
  "network": {
    "prefix_length": 24,
    "gateway": "192.168.0.1"
  },
  "service_vips": {
    "gateway": "192.168.0.2",
    "wireguard": "192.168.0.3"
  }
}
```

`infra/sandbox/cluster/config.json` holds the same keys with empty strings: k3d has no kube-vip, and charts skip a
value that is empty. (`infra/` is otherwise created by step 7; this step adds only these files.)

### Argo CD passes the parameters

Both ApplicationSets wrap their generator in a `matrix` with a git files generator reading the environment's
`config.json` (production: Forgejo `ops/homelab`; sandbox: the GitHub branch `sandbox`, restated in
`values-dev.yaml` like the other generators). Every generated Application gets Helm parameters:

| Parameter | From |
|---|---|
| `global.network.apiVIP` | `vip` |
| `global.network.router` | `network.gateway` |
| `global.network.gatewayVIP` | `service_vips.gateway` |
| `global.network.wireguardVIP` | `service_vips.wireguard` |

`root` Applications gain a `helm.parameters` list (they have none today); `apps` adds these to its existing
`global.*` parameters.

### Consumers

| Where | Before | After |
|---|---|---|
| `system/istio-system` Gateway Service | `gateway.loadBalancer.ip: 192.168.1.224` | `global.network.gatewayVIP`; the kube-vip class and address apply only when it is set |
| `apps/wireguard.yaml` Service | `192.168.1.225` | `{{ .Values.global.network.wireguardVIP }}` |
| `apps/wireguard.yaml` peer `AllowedIPs` | `172.16.0.1/32, 192.168.1.100/32, 192.168.1.224/27` | `172.16.0.1/32`: the list is an ESO template (no Helm values) and the server ignores its own peer; clients add the VIPs from `config.json` (VPN docs) |
| `apps/tailscale.yaml` `TS_ROUTES` | `192.168.1.224/27` | `gatewayVIP/32` |
| `apps/homepage.yaml` router link | `https://192.168.1.1` | `https://{{ .Values.global.network.router }}` |

App files template these strings the way they template hosts; a value app-template does not template is moved
to a field it does (checked by rendering).

### Control-plane scrape without addresses

`system/monitoring-system` replaces the chart's `endpoints` lists (controller-manager, scheduler, etcd, kube-proxy)
with `ScrapeConfig`s (`monitoring.coreos.com/v1alpha1`) using Kubernetes node discovery: the control-plane
components on nodes labelled `node-role.kubernetes.io/control-plane`, kube-proxy on every node. Job names stay
those of the chart (`kube-controller-manager`, `kube-scheduler`, `kube-etcd`, `kube-proxy`) so its dashboards and
rules keep working; the per-component metric filter stays. HTTPS components authenticate with Prometheus's
service account token. The sandbox scrapes them too (the k3d node already exposes them), so `values-dev.yaml`
drops its opt-outs and the sandbox tests prove the scrape.

### Leftovers

- `metal/inventories/prod.yml`: `control_plane_endpoint` read from `config.json`'s `vip` (Ansible `lookup`), nodes
  `192.168.0.11-13`; `gateway_ip` and `wireguard_ip` are removed (nothing in `metal/` reads them).
  MAC, disk and interface are placeholders marked "fill in before the first deploy" (Ansible is replaced in step
  11).
- `scripts/configure`: drops the Gateway and wireguard questions (domain, seed repo and timezone stay until
  step 7).
- Wireguard's peer keys and upstream's `horus` peer are upstream's personal VPN settings: unchanged here, noted in
  the production checklist.

## Error handling

- A missing or malformed `config.json` makes the ApplicationSet generate nothing; the ApplicationSet's status shows
  the error and the sandbox tests fail at once.
- An empty Service VIP leaves the Service to k3s ServiceLB (the sandbox behaviour).
- A node without the control-plane label is not scraped for the control-plane jobs; `up == 0` catches a node
  that is discovered but unreachable.

## Testing

- `scripts/check-network-config` (new, offline): renders every chart and app file in production and the sandbox
  and fails when a rendered manifest contains an IPv4 literal that does not come from the environment's
  `config.json`, apart from an allowlist of addresses that are not environment settings (loopback, the k3s pod and
  Service ranges, the wireguard tunnel `172.16.0.0/24`, `0.0.0.0`). It also checks that the production render
  carries `config.json`'s values where the table above says. Added to the basic checks.
- `make dev`: every existing test passes; `TestMeshPrometheusTargetsUp` (no target with `up == 0`) now covers the
  control-plane jobs, and a new check asserts that the jobs `kube-controller-manager`, `kube-scheduler`,
  `kube-etcd` and `kube-proxy` have targets.

## Docs

- `docs/reference/architecture/networking.md`, `docs/getting-started/vpn-setup.md`: the new addresses and where
  they are set.
- `docs/installation/`: a new user edits `infra/<env>/cluster/config.json`.
- `CLAUDE.md`: roadmap 6d split into 6d-1 (this) and 6d-2 (egress); rule: addresses only in
  `infra/<env>/cluster/config.json`, checked by `scripts/check-network-config`; the production checklist uses the new
  addresses and notes the placeholders in `metal/inventories/prod.yml` and upstream's wireguard peers.
