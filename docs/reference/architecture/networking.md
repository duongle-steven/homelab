# Networking

```mermaid
flowchart TD
  subgraph LAN
    laptop/desktop/phone <--> LoadBalancer
    subgraph k8s[Kubernetes cluster]
      Pod --> Service
      Service --> HTTPRoute
      HTTPRoute --> gateway[Gateway gateway]
      HTTPRoute --> public[Gateway public]
      LoadBalancer <--> gateway
      cloudflared <--> public
    end
  end

  cloudflared -- outbound --> Cloudflare
  Internet -- inbound --> Cloudflare
```

The cluster uses k3s's own networking: Flannel, kube-proxy, the network policy controller and ServiceLB. Istio serves two Gateway API `Gateway`s in namespace `istio-system` (`system/istio-system`):

- `gateway` is for the LAN. In production kube-vip gives its LoadBalancer Service its own address (`192.168.0.2`, announced with ARP and moved to another node when one fails); external-dns points each route's hostname at it. It also redirects HTTP to HTTPS and carries Forgejo SSH (port 22, `TCPRoute`), which the nodes' own SSH would block on their addresses. In the sandbox, ServiceLB serves it on the k3d node.
- `public` is published through the Cloudflare Tunnel: its Service is `ClusterIP`, cloudflared forwards to it in the cluster, and external-dns points its routes' hostnames at the tunnel. Only apps that attach to it are reachable from the internet.

kube-vip also holds the Kubernetes API address (`192.168.0.10`) and serves only LoadBalancer Services of class `kube-vip.io/kube-vip-class`; ServiceLB serves the others.

These addresses are set in `infra/<env>/cluster/config.json` (upstream's format: `vip`, `network`, plus `service_vips`); Argo CD passes them to every chart as `global.addresses.*`, and `scripts/check-env-config` keeps addresses out of charts and app files.

Both terminate TLS with one wildcard certificate. Apps attach an `HTTPRoute` to one of them; there are no `Ingress` objects.

## Service mesh

Every namespace Argo CD manages joins Istio's ambient mesh (`istio.io/dataplane-mode: ambient`, set through Argo CD's `managedNamespaceMetadata`): istio-cni redirects each pod's traffic to the ztunnel on its node, which wraps it in mutual TLS with the workload's SPIFFE identity. A mesh-wide `PeerAuthentication` (`STRICT`) makes meshed pods refuse plaintext. There are no sidecars; apps need no changes.

Outside the mesh:

- `istio-system` (the mesh itself) and `rook-ceph` (some host-network pods, Ceph's own encryption), labelled `none` by the `root` ApplicationSet's `templatePatch`
- `kube-system`, which Argo CD does not manage
- the pods the Kubernetes API server calls as webhooks (cert-manager, trust-manager, External Secrets, the Prometheus operator, CloudNativePG), labelled `none` on the pod; `scripts/check-mesh-policies` fails if a chart adds a webhook that is still meshed
- the VPN routers (WireGuard, Tailscale), labelled `none` on the pod: ambient would hand the clients' forwarded TCP to ztunnel, which refuses it

The CloudNativePG operator, outside the mesh, polls its PostgreSQL instances' status port (8000); a `PeerAuthentication` on `forgejo-db` lets that port accept plaintext.

The Istio Gateways reach meshed pods over HBONE, so inbound traffic is unchanged. Meshed connections arrive on ztunnel's HBONE port (TCP 15008), so a chart that ships NetworkPolicies (Argo CD, Matrix) also gets a policy `allow-hbone` that lets that port in. If ztunnel is down on a node, meshed pods there cannot open connections.

Inside the mesh nothing is reachable unless allowed: `deny-all` in `istio-system` (the Gateways keep `allow-gateways`) and one `AuthorizationPolicy` `allow` per namespace, in the namespace's chart or app file: pods of the same namespace, the Gateway its routes attach to (on the pod ports behind them), Prometheus on the metrics ports, and a few named callers (Grafana to Prometheus and Loki, Argo CD's repo server to Forgejo, CloudNativePG's operator to its instances). Pods outside the mesh get a NetworkPolicy that opens only their webhook, VPN or metrics ports. A new app needs its `allow` policy; `scripts/check-mesh-policies` fails without it. Outbound traffic is restricted too (see Egress).

### Node restarts

When a node restarts, the kubelet recreates its pods while the node's ztunnel may still be waiting for istiod. istio-cni cannot hand those pods to ztunnel within its 5-second limit, containerd tears their sandboxes down, and istio-cni's snapshot keeps their dead network namespaces, which ztunnel then retries forever: the node's meshed pods stay `Pending` and ztunnel stays not Ready. istio-cni works through those pods one at a time, and while each takes more than 5 seconds none ever succeeds. Two settings get the node out: istio-cni skips IPv6 rules (`cni.ambient.ipv6: false`; pods only get IPv4), which halves its work per pod, and the CronJob `istio-system/istio-cni-watchdog` deletes a node's istio-cni pod when that node's ztunnel has run for 3 minutes without becoming Ready, so the new istio-cni builds its snapshot again. The sandbox recovers in 5 to 14 minutes. Look at `kubectl -n istio-system get pods` and the watchdog Jobs' logs. istio-cni writes its own CNI config file (`istioOwnedCNIConfig`), because k3s rewrites `10-flannel.conflist` at every start.

## Egress

Every namespace Argo CD manages (except `istio-system` and `rook-ceph`) gets `system/egress`: egress is denied except to the cluster (pod and Service ranges: DNS, the Gateway, pods outside the mesh), the Kubernetes API (the node network, port 6443) and the LAN Gateway's VIP. Namespaces listed in `system/egress/values.yaml` also reach the internet (any address except the private ranges) on their ports, and `monitoring-system` the nodes' metrics ports. Nothing reaches the rest of the LAN. Traffic between meshed pods and in-cluster DNS go through ztunnel and are governed by the mesh's AuthorizationPolicies, not by these policies. k3s's network policy controller applies policies to a new pod asynchronously, so a pod's first seconds are not yet restricted.
