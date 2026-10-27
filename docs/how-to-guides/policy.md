# Policy: Kyverno and Pod Security Admission

Two layers keep workloads within the rules (step 15b, `docs/superpowers/specs/2026-10-27-policy-design.md`).

- **Kyverno** (`system/kyverno`) applies the Pod Security Standards at **restricted**, plus two rules of this repository:
  every image is **pinned** (a tag other than `latest`, or a digest), and comes from an **allowed registry**
  (`registries` in `system/kyverno/values.yaml`, plus zot). It runs in **Audit**: violations land in PolicyReports;
  nothing is refused yet (Enforce is step 15b-2).
- **Pod Security Admission** (Kubernetes itself) enforces **baseline** on every managed namespace, and stays in force
  if Kyverno is down. Namespaces whose pods need the node are `privileged`: the mesh (`istio-system`), Ceph
  (`rook-ceph`), `kured`, `loki` (Alloy), `monitoring-system` (node-exporter), the sandbox's `csi-hostpath`, and the VPN
  routers (`tailscale`, `wireguard`). The labels come from Argo CD's ApplicationSets (`system/argocd/values.yaml`).

## The `policy` check

`scripts/ci-check policy` (a basic check, also in CI) renders every chart and app file for both environments and runs
the same policies with the `kyverno` CLI. A pull request whose workload breaks one fails before it merges.

## A new workload

Set its securityContext:

- **The pod:** `seccompProfile: {type: RuntimeDefault}`, and `runAsNonRoot: true` with the image's numeric user
  (`runAsUser`, `runAsGroup`, `fsGroup`) when the image has a non-root user.
- **Each container:** `allowPrivilegeEscalation: false`, and `capabilities: {drop: [ALL]}` when it runs as non-root.

In app files, use `defaultPodOptions.securityContext` and each controller's `defaultContainerOptions`
(`defaultContainerOptionsStrategy: merge`).

## An exception

When the image cannot meet a rule (it runs only as root, it needs the node), write a `PolicyException` beside the
workload, in its own chart (`templates/policy-exception.yaml`) or app file (`rawResources.policy-exception`):

```yaml
apiVersion: policies.kyverno.io/v1
kind: PolicyException
metadata:
  name: pod-security
  annotations:
    homelab/reason: "the image runs as root (no USER)"
spec:
  policyRefs:
    - {name: require-run-as-nonroot, kind: ValidatingPolicy}
  matchConditions:
    - name: app
      expression: "object.metadata.name.startsWith('app')"
```

Name only the policies it breaks, and match only its workload by name. Read the current exceptions with
`kubectl get policyexceptions.policies.kyverno.io -A`. If the workload also breaks baseline (hostPath, host
namespaces, privileged, added capabilities), its namespace must be `privileged` in Argo CD's `templatePatch`.

## Reports

```sh
kubectl get policyreports -A                      # one per namespace
kubectl -n <namespace> get policyreports -o yaml  # each result: pass, fail, skip
```

`TestPolicy` checks that no workload of this repository fails. That is the condition for switching to Enforce.
