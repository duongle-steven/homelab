# Policy: Kyverno and Pod Security Admission

Two layers keep workloads within the rules (step 15b, `docs/superpowers/specs/2026-10-27-policy-design.md`).

- **Kyverno** (`system/kyverno`) applies the Pod Security Standards at **restricted**, plus two rules of this repository:
  every image is **pinned** (a tag other than `latest`, or a digest), and comes from an **allowed registry**
  (`registries` in `system/kyverno/values.yaml`, plus zot). It runs in **Audit**: violations land in PolicyReports;
  nothing is refused yet (Enforce is step 15b-2).
- **Pod Security Admission** (Kubernetes itself) enforces **baseline** on every managed namespace, and stays in force
  if Kyverno is down. Namespaces whose pods need the node are `privileged`: the mesh (`istio-system`), Ceph
  (`rook-ceph`), `opentelemetry` (its agent), `monitoring-system` (node-exporter), the sandbox's `csi-hostpath`, and the VPN
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
    # Before its workloads (scripts/ci-check policy checks it)
    argocd.argoproj.io/sync-wave: "-1"
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
kubectl get policyreports -A                      # one per resource (its scope)
kubectl -n <namespace> get policyreports -o yaml  # each result: fail, error or warn (passes are not kept)
```

`TestPolicy` checks that no workload of this repository fails. That is the condition for switching to Enforce.

Every `PolicyException` carries `argocd.argoproj.io/sync-wave` below its workloads' (`"-1"` where they sit at the
default `0`), and `scripts/ci-check policy` refuses one that does not. Argo CD applies the kinds it does not know last
within a wave, so without the wave a pod was admitted before the exception that excuses it and kept a `fail` until the
next background scan. Kyverno does not judge again what it has already reported when an exception is created: a new
exception for a running workload clears its `fail` only at the next background scan (every hour in production, every
6 hours in the sandbox) or when the pod is recreated. k3s's own pods in `kube-system` (CoreDNS, metrics-server,
kube-vip, the sandbox's ServiceLB) have exceptions in `system/kyverno` (`kubeSystemExceptions`), applied after
Kyverno's own workloads, since Kyverno's exception webhook fails closed and Kyverno must be able to sync itself: on a
fresh cluster they report until that first scan, so `TestPolicy` leaves `kube-system` out.

## When it alerts

Two alerts reach ntfy and Telegram (warnings: muted at night), so the Audit window does not depend on someone reading
the reports (the threat model's G1, `docs/superpowers/specs/2026-11-27-policy-report-alert-design.md`):

- **`PolicyReportFail`** (Prometheus, `system/kyverno/templates/prometheus-rule.yaml`): a report holds a `fail` or
  `error`. kube-state-metrics exports each result, and the rule names the workload at the top of the pod's owners
  (the Deployment, Argo Rollout, StatefulSet, DaemonSet or CronJob; an operator's resource such as CloudNativePG's
  `Cluster`; the pod itself when kube-state-metrics shows it has none), so a rollout's new pod or a CronJob's next run
  is the same alert. A report whose resource is already gone names nothing new. Pods of Argo Workflows share one per namespace, policy
  and rule (owner kind `Workflow`, no name: a new Workflow every run). It keeps firing 15 minutes after the report is
  gone, so a short-lived pod still notifies.
- **`PolicyViolationEvent`** (Loki's ruler, `system/loki/files/policy-rules.yaml`): Kyverno's admission event for a
  violation, per namespace, kind and policy. It catches a pod deleted before any scrape saw its report (a passing CI
  step). While `PolicyReportFail` fires (silenced or not) for the same namespace and policy, Alertmanager holds it back
  (`inhibit_rules`), so a known failure's next pods do not notify again; two workloads of one namespace breaking the
  same policy share it.

What to do:

1. Read the report: `kubectl -n <namespace> get policyreports -o yaml` (the alert's description has the command).
2. Fix the workload's securityContext (above), or add a `PolicyException` beside it with its `homelab/reason` and its
   wave. The alert resolves 15 minutes after the report is clean (`keep_firing_for`): the report of a fixed
   workload is clean once its new pods are, a new exception's only after the next background scan.
3. Until then, silence it in Alertmanager by its labels (`alertname`, `namespace`, `owner_kind`, `owner_name`,
   `policy`): the labels stay the same across pods and runs, and a silence expires by itself.

A failure nobody expected (a pod no chart renders, in a namespace that should hold none) is a security event, not a
fix to make: find who created it first (`docs/how-to-guides/security-logs.md`).
