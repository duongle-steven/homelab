# Step 6b: Istio ambient mesh with strict mTLS

Status: design approved in chat 2026-10-15, written spec awaiting review. Branch: `feat/istio-ambient` (PR into `dev`).

## Goal

Every namespace Argo CD manages joins Istio's ambient mesh, so traffic between pods is mutually authenticated
and encrypted (mTLS, SPIFFE identities) without sidecars or app changes. The mesh refuses plaintext from
outside it (`PeerAuthentication` STRICT). Exceptions are explicit: the namespaces `istio-system` and
`rook-ceph`, `kube-system` (not managed by Argo CD), and the pods the API server calls as webhooks.
`make dev` builds the sandbox from scratch and passes the existing 73 tests plus new mesh tests, under 8 GiB.

Step 6 continues: **6c** adds default-deny `AuthorizationPolicy` and `NetworkPolicy` with explicit allows.

## Context

- Step 6a: k3s networking (Flannel, kube-proxy, ServiceLB, network policy controller), Istio 1.31.1 `base` +
  `istiod` (`system/istio-system`, repo `https://blob.istio.io/istio-release/charts`) serving the Gateways
  `istio-system/gateway` and `istio-system/public`; the sandbox is k3d (one k3s v1.37.1 server).
- Istio 1.31.1 ships `profile-ambient.yaml` and `profile-platform-k3d.yaml` / `profile-platform-k3s.yaml`
  for `base`, `istiod`, `cni` and `ztunnel` (the k3s CNI paths: `cniConfDir`
  `/var/lib/rancher/k3s/agent/etc/cni/net.d`, `cniBinDir` `/var/lib/rancher/k3s/data/cni` on k3s, `/bin` on k3d).
- Upstream `nixos` runs Istio without ambient (no `cni`, no `ztunnel`); there is no upstream reference.
- Pods join ambient through the label `istio.io/dataplane-mode=ambient` on their namespace (or pod); the pod
  label `istio.io/dataplane-mode=none` opts a pod out. istio-cni marks redirected pods with the annotation
  `ambient.istio.io/redirection: enabled`. Kubelet probes are exempt from capture.
- Argo CD creates every namespace (`CreateNamespace=true`) and applies the shared `managedNamespaceMetadata`
  to it, including namespaces that already exist (the VolSync annotation is on `argocd`, `forgejo` and
  `istio-system` in the sandbox). The ApplicationSet `root` uses fasttemplate (`{{path.basename}}`); `apps`
  uses go templates.
- Webhook targets in the sandbox: `cert-manager/cert-manager-webhook`, `cert-manager/trust-manager`,
  `external-secrets/external-secrets-webhook`, `istio-system/istiod`,
  `monitoring-system/monitoring-system-kube-pro-operator`; API service `kube-system/metrics-server`.
  Production adds the CloudNativePG operator webhook and rook-ceph.
- Sandbox RAM after 6a: node 7.23 GiB.

## Decisions

| Topic | Decision |
|---|---|
| Split | 6b mesh + strict mTLS; 6c authorization (`AuthorizationPolicy`, `NetworkPolicy`) |
| Scope | Every namespace Argo CD manages; out: `istio-system`, `rook-ceph`, `kube-system` |
| Enrollment | Label `istio.io/dataplane-mode: ambient` in the shared `managedNamespaceMetadata`; `root` moves to go templates and a `templatePatch` sets `none` for `istio-system` and `rook-ceph` |
| Webhook pods | Pod label `istio.io/dataplane-mode: none` through each chart's pod labels: cert-manager webhook, trust-manager, external-secrets webhook, prometheus-operator, CloudNativePG operator |
| mTLS | Mesh-wide `PeerAuthentication` `default` in `istio-system`, `STRICT` |
| Platform | `global.platform: k3s` (production), `k3d` (sandbox) |

## Design

### `system/istio-system`

- Dependencies `cni` and `ztunnel` 1.31.1 next to `base` and `istiod`.
- Every subchart gets `profile: ambient` and `global.platform: k3s` in `values.yaml`; `values-dev.yaml`
  sets `global.platform: k3d` for each.
- Small requests for `ztunnel` and `cni`, sized after measuring.
- `templates/peer-authentication.yaml`: `PeerAuthentication` `default` (namespace `istio-system`, the root
  namespace), `spec.mtls.mode: STRICT`.

### Enrollment (`system/argocd`)

- ApplicationSet `root`: `goTemplate: true` with `missingkey=error`; `{{path.basename}}` and `{{path}}` become
  `{{ .path.basename }}` and `{{ .path.path }}`, in `values.yaml`, `values-dev.yaml` and `values-seed.yaml`.
- Shared `managedNamespaceMetadata.labels`: `istio.io/dataplane-mode: ambient` (the `apps` ApplicationSet
  shares the sync policy).
- `root` `templatePatch`: for `istio-system` and `rook-ceph`, `managedNamespaceMetadata.labels`
  `istio.io/dataplane-mode: none` (the annotations stay).

### Webhook pods

- `istio.io/dataplane-mode: none` on the pods of: cert-manager's webhook, trust-manager, external-secrets'
  webhook, kube-prometheus-stack's prometheus-operator, CloudNativePG's operator; each through its chart's
  pod label value.
- A render check lists every `ValidatingWebhookConfiguration`, `MutatingWebhookConfiguration`, `APIService`
  and CRD conversion webhook in every chart (production and sandbox) and fails unless its Service selects
  pods labelled `none` or lives in a namespace outside the mesh.

### Paths that do not change

- `kube-system` (CoreDNS, metrics-server, ServiceLB) stays outside the mesh.
- The Istio Gateways (in `istio-system`) speak HBONE to meshed pods, so inbound traffic keeps its path.
- Prometheus is in the mesh: meshed targets over mTLS, targets outside the mesh (k3s, node-exporter on the
  host network) in plaintext as before.

## Error handling

- A missed webhook pod: the API server's calls are reset and Argo CD syncs fail; the render check catches it
  before the merge.
- ztunnel down on a node: meshed pods on that node cannot open connections; documented.
- Pods started before istio-cni on a node: istio-cni enrols running pods when their namespace is labelled and
  re-applies its rules at startup (corrected after the final review; this line first said a restart was needed).
- rook-ceph stays outside the mesh (Ceph encrypts itself; host-network pods).

## Testing

- Offline: `scripts/render-chart` (prod and dev) for `system/istio-system`, `system/argocd`, and every chart
  that gains a pod label; the webhook render check; `go vet` and the offline tests; `mkdocs build --strict`.
- `test/mesh_test.go` (sandbox only; `make dev` filter gains `Mesh`):
  - `TestMeshEnrolled`: every namespace Argo CD manages has the label `ambient` except `istio-system`
    (`none`); every pod there carries `ambient.istio.io/redirection: enabled`, except the opted-out webhook
    pods, which carry the label `none`.
  - `TestMeshRejectsPlaintext`: two temporary pods in namespace `default` (outside the mesh). The plain one
    cannot reach `http://excalidraw.excalidraw`; the one labelled `istio.io/dataplane-mode=ambient` can, and
    ztunnel logs the connection with the identity `spiffe://cluster.local/ns/default/sa/default`. Both are
    deleted afterwards.
- After the merge: `make dev-clean && make dev`, all tests pass; RAM measured.

## Docs

- `docs/reference/architecture/networking.md`: ambient mesh, strict mTLS, the exceptions.
- `docs/installation/sandbox.md`: RAM with ztunnel and istio-cni.
- `CLAUDE.md`: 6b done and 6c added to the roadmap; namespaces created by Argo CD join the mesh, new webhook
  pods need the `none` label, `istio-system` and `rook-ceph` stay out; production: ambient on k3s, the
  CloudNativePG operator opt-out and rook-ceph's exclusion were only rendered.
