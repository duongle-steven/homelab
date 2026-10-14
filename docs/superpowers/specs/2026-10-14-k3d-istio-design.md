# Step 6a: k3d sandbox, k3s defaults, Istio Gateway

Status: design approved in chat 2026-10-14, written spec awaiting review. Branch: `feat/k3d-istio` (PR into `dev`).

## Goal

The sandbox runs on k3d and production on k3s with k3s's own networking (Flannel, kube-proxy, ServiceLB,
network policy controller). Cilium leaves the repo. Istio replaces Cilium as the Gateway API implementation,
with the Gateways named and placed like upstream `nixos` (`istio-system/gateway`, `istio-system/public`).
`make dev` builds the k3d sandbox from scratch and passes every existing test (72), under 8 GiB of RAM.

Step 6 is split: **6a** (this spec) and **6b** Istio ambient mesh (`istio-cni`, `ztunnel`, mTLS,
`AuthorizationPolicy`, network policies), brainstormed after 6a.

## Context

- Sandbox today: kind (Kubernetes 1.36.1, held back for kind and Cilium), Cilium as CNI and kube-proxy
  replacement, Cilium Gateway with LB IPAM and L2 announcements (`172.30.255.200` for `gateway/homelab`,
  `.201` for `gateway/homelab-public`), a host-network socat container forwarding `127.0.0.1:80/443` to
  `.200`, CoreDNS rewrite of `*.127-0-0-1.nip.io` to `cilium-gateway-homelab.gateway` (Ansible role
  `coredns_rewrite`), and tests that dial `172.30.255.200`.
- Production `metal/` (never run on this fork): k3s v1.30.4 with Flannel, kube-proxy, network policy and
  ServiceLB disabled; Cilium installed by Ansible; kube-vip v0.6.4 for the API VIP `192.168.1.100`; Cilium
  LB pool `192.168.1.224/27`; Gateway API CRDs applied by `metal/` before Cilium.
- Upstream `nixos`: every node is a k3s server (`clusterInit` on the first), k3s defaults except
  `traefik` and `local-storage` disabled, Gateway API CRDs (standard channel) through k3s's auto-deploy
  manifests, kube-vip v1.2.4 for the API only (`svc_enable: false`). Istio 1.30.5 (`base` + `istiod`, default
  values, no `istio-cni` or `ztunnel`, so no mesh) is only the Gateway API implementation. Gateways
  `istio-system/gateway` (LAN, `LoadBalancer` through ServiceLB) and `istio-system/public` (Tunnel,
  `ClusterIP` through `infrastructure.parametersRef`), wildcard certificate `wildcard-tls`.
- Latest stable on 2026-10-14: k3d 5.9.0, k3s v1.37.1+k3s1, Istio 1.31.1 (supports Kubernetes 1.32–1.37;
  1.30 stops at 1.36), kube-vip v1.2.4.
- ServiceLB publishes a `LoadBalancer` Service on every node's IP with `hostPort`s. Production nodes run
  `sshd` on port 22, so a Gateway listener on 22 cannot go through ServiceLB there.
- Things that name Cilium or the current Gateways: `metal/` (role `cilium`, inventories, `kind-dev.yaml`,
  Makefile), `system/gateway`, `system/argocd` (allowlist, `global.publicGateway`, Argo CD route),
  `system/cloudflared` (`cilium-gateway-homelab-public.gateway`), `system/monitoring-system/values-dev.yaml`
  (kube-proxy scrape disabled), every `apps/*.yaml` route, `apps/matrix`, `platform/kanidm` (kaniop
  `gateway.parentRefs`, `backendCA` comment), `platform/forgejo` (HTTPRoute, TCPRoute), `platform/grafana`,
  `platform/zot`, `platform/woodpecker`, `flake.nix` (`cilium-cli`, `kind`), `scripts/get-dns-config`,
  `test/identity_helpers_test.go` (`sandboxIngress`), `test/gateway_test.go`, docs.

## Decisions

| Topic | Decision |
|---|---|
| Split | 6a here; ambient mesh in 6b |
| Production `metal/` | Changed too: k3s defaults, Cilium role removed (rendered and syntax-checked only) |
| Sandbox nodes | One k3s server (HA is tested on staging in step 11) |
| Gateway names | Like upstream: `istio-system/gateway` (LAN), `istio-system/public` (Tunnel) |
| Host access | k3d port mapping (80, 443, 22) to its load balancer; no socat; spike first, socat to the node IP as fallback |
| Gateway LB in production | kube-vip `svc_enable` for Services of class `kube-vip.io/kube-vip-class`: Gateway `gateway` on `192.168.1.224` (Forgejo SSH keeps port 22, one VIP for HA), wireguard on `192.168.1.225`; ServiceLB stays for everything else |
| Versions | k3d 5.9.0, k3s v1.37.1+k3s1, Istio 1.31.1, kube-vip v1.2.4 |

## Design

### Sandbox cluster (`metal/`)

- `metal/k3d-dev.yaml` replaces `kind-dev.yaml`: cluster `homelab-dev`, one server, image
  `rancher/k3s:v1.37.1-k3s1`, ports `80`, `443` and `22` mapped to the k3d load balancer, k3s args
  `--disable=traefik`, `--cluster-init` (embedded etcd, like production), `--etcd-expose-metrics`,
  `--kube-controller-manager-arg=bind-address=0.0.0.0`, `--kube-scheduler-arg=bind-address=0.0.0.0`.
  Flannel, kube-proxy, ServiceLB, network policy and `local-storage` stay on.
- The Gateway API CRDs (experimental channel, for `TCPRoute`) are mounted into k3s's auto-deploy manifest
  directory, so they exist before Argo CD, as in production.
- `metal/Makefile`: `dev` creates the cluster if missing and writes `kubeconfig.yaml` (keeping the WSL
  mirrored fix only if the spike needs it); `clean` deletes it. The `kind` and `proxy` targets,
  `inventories/dev.yml` and the role `coredns_rewrite` go away.
- Spike before anything else: an empty k3d cluster with the port mapping and an nginx `LoadBalancer`
  Service, reached with curl at `127.0.0.1` from WSL and from the Windows browser. If it fails, the tests
  dial the k3d node IP (from `docker inspect`) and the browser goes through a socat container to it.

### Production (`metal/`, rendered only)

- Role `k3s`: `k3s_version: v1.37.1+k3s1`; `k3s_server_config` keeps `tls-san`, `secrets-encryption`,
  `disable-helm-controller` and `disable: [local-storage, traefik]`, and drops `servicelb`,
  `disable-kube-proxy`, `disable-network-policy` and `flannel-backend: none`; same metrics flags as the
  sandbox; Gateway API CRDs (experimental channel) through the auto-deploy manifests.
- kube-vip v1.2.4: `cp_enable` for `192.168.1.100` as before, plus `svc_enable`, `lb_class_only` and the
  class `kube-vip.io/kube-vip-class`, so it only serves Services that ask for it.
- `cluster.yml` drops the role `cilium` (deleted); inventories drop `cilium_*`; `load_balancer_ip_pool`
  becomes the documentation of the addresses used (`192.168.1.224` Gateway, `.225` wireguard).
- `apps/wireguard.yaml`: `loadBalancerClass: kube-vip.io/kube-vip-class` and the fixed IP `192.168.1.225`
  (production only; wireguard is not in the sandbox).
- `flake.nix`: `k3d` replaces `kind` and `cilium-cli`.

### `system/istio-system` (replaces `system/gateway`)

- Dependencies `base` and `istiod` 1.31.1 (`https://istio-release.storage.googleapis.com/charts`); istiod
  with `PILOT_ENABLE_ALPHA_GATEWAY_API=true` (`TCPRoute`) and requests cpu 100m, memory 128Mi; the
  ApplicationSet ignores the validating webhooks' `failurePolicy` and `caBundle`, as upstream.
- Gateway `gateway` (class `istio`): listeners `http` 80 (only the redirect route), `https` 443
  `*.<domain>` (`wildcard-tls`, all namespaces), `ssh` 22 (`TCPRoute` from namespace `forgejo` only).
  Production: `infrastructure.parametersRef` to a ConfigMap that gives the Service
  `loadBalancerClass: kube-vip.io/kube-vip-class`, and `addresses` `192.168.1.224`. Sandbox
  (`values-dev.yaml`): neither, so ServiceLB publishes `gateway-istio` on the node.
- Gateway `public`: listener `https` only, the external-dns target annotation as today, Service
  `public-istio` of type `ClusterIP` through `infrastructure.parametersRef`.
- Certificate `wildcard` (secret `wildcard-tls`) and HTTPRoute `https-redirect`, moved from the old chart.
- Sandbox only: ConfigMap `coredns-custom` in `kube-system` (key `nip-io.override`) with
  `rewrite name regex (.*)\.127-0-0-1\.nip\.io gateway-istio.istio-system.svc.cluster.local answer auto`,
  which k3s's CoreDNS imports.

### Routes and references

- Every `parentRefs` `gateway/homelab` → `istio-system/gateway`, `gateway/homelab-public` →
  `istio-system/public`: `apps/*.yaml`, `apps/matrix`, Kanidm (kaniop `gateway.parentRefs`), Forgejo
  (HTTPRoute, TCPRoute), Grafana, zot, Woodpecker, Argo CD.
- `global.publicGateway`: `public` in production, `gateway` in the sandbox (`values.yaml`,
  `values-dev.yaml`, `values-seed.yaml`).
- cloudflared forwards to `https://public-istio.istio-system`.
- Kanidm keeps its `BackendTLSPolicy` (Istio accepts a ConfigMap CA).
- `system/argocd/values-dev.yaml`: `system/istio-system` replaces `system/gateway` in the allowlist.
- `system/monitoring-system/values-dev.yaml`: kube-proxy scraping comes back.
- `scripts/get-dns-config` and every other Cilium mention are updated or removed.

## Error handling

- WSL blocks the k3d port mapping: the spike finds it first; fallback as above.
- Gateway `TCPRoute` not accepted (alpha flag missing or CRD channel wrong): `TestGatewayForgejoSSH` fails.
- CoreDNS rewrite not loaded: the identity and SSO tests fail on Kanidm discovery from pods.
- kube-vip service LB misconfigured: only visible in production; recorded in `CLAUDE.md`.

## Testing

- Offline: `scripts/render-chart` (prod and dev) for `system/istio-system`, `system/argocd`,
  `system/cloudflared`, `system/monitoring-system`, `platform/kanidm`, `platform/forgejo`,
  `platform/grafana`, `platform/zot`, `platform/woodpecker`, `apps/matrix` and every changed `apps/*.yaml`;
  a grep that no `cilium`, `homelab-public` or `gateway/homelab` reference is left; `ansible-playbook
  --syntax-check` for `metal/cluster.yml`; `go vet` and the offline test regex; `mkdocs build --strict`.
- Test changes: `sandboxIngress` goes (tests and chromium resolve `*.127-0-0-1.nip.io` to the address the
  spike chose); `test/gateway_test.go` checks `istio-system/gateway` and `gateway-istio`;
  `TestGatewayForgejoSSH` goes through port 22 of the k3d load balancer.
- After the merge: `make dev-clean && make dev` from scratch, 72/72; measured RAM in `sandbox.md`.

## Docs

- `docs/installation/sandbox.md`: k3d, no socat and fixed IPs, prerequisites, RAM.
- `docs/reference/architecture/networking.md`, `docs/how-to-guides/expose-services-to-the-internet.md`,
  README: Istio, the Gateway names, kube-vip for the Gateway.
- `CLAUDE.md`: roadmap step 6 split into 6a (done) and 6b; Sandbox section (k3d, Gateway names,
  `coredns-custom`, no fixed addresses); "Before the first production deploy": drop the Cilium notes, add
  that k3s defaults, kube-vip `svc_enable` and the addresses `192.168.1.224/.225` were only rendered.
