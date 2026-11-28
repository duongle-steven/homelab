# G1: an alert on new failures in Kyverno's reports, and exceptions applied before their workloads

The threat model's G1 (`docs/superpowers/specs/2026-11-14-threat-model-design.md`), its part before 12b-3: production
starts in Kyverno's Audit and switches to Enforce (15b-2) after about two weeks of its own PolicyReports. During that
window, a new `fail` result must reach a person by itself instead of waiting for someone to read the reports.

## Why

Production runs workloads the sandbox never does (cloudflared, the VPN routers, jellyfin, Ceph's daemons, kube-vip,
Renovate's initContainer), so their reports exist only there. Audit blocks nothing, so an unread report is a silent gap
(10f's build pod broke a restricted rule and nothing noticed until a reviewer read it).

The alert stays useful after Enforce. With `failurePolicy: Ignore`, a pod admitted while Kyverno is down is checked
only by the next background scan. A new or changed policy (a Renovate bump included) judges running workloads only in
the reports.

## Goals

- A notification (ntfy, Telegram) when a resource gets a `fail` or `error` result it did not have before.
- No false alert when a workload ships together with its `PolicyException`.
- A known failure stays visible until it is fixed or excepted.

## Non-goals

- Enforce (15b-2) and restricting who may create a `PolicyException`: both come after production's window.
- A dashboard or UI for the reports: `kubectl get policyreports -A` (`docs/how-to-guides/policy.md`) and the alert's
  labels are enough for now.
- `warn` results: our policies run in Audit and report `fail`, not `warn`.

## Anti-goals

- An alert that fires again and again for the same failure (it would be muted, then ignored).
- A second list of accepted failures outside the `PolicyException`s.

## Approaches

1. **kube-state-metrics reads the reports** *(chosen, user, 2026-11-27)*: its `customResourceState` (already used for
   Kargo's Promotions) exports one series per result of every PolicyReport and ClusterPolicyReport, and a
   `PrometheusRule` alerts per (resource, policy, rule). Alertmanager already treats each label set as its own alert,
   so "new" needs no stored state: a label set that was not firing is new, and one already firing (or silenced) is not
   sent again. No new component.
2. **Policy Reporter** (`kyverno/policy-reporter`, Kyverno's own subproject): the same gauges from a purpose-built
   exporter, plus a UI and its own notifications. It adds a component (image, mesh, egress, policy, the sandbox's
   memory), and its UI and notifications repeat Grafana and Alertmanager.
3. **Kyverno's `kyverno_policy_results_total`**: a counter of evaluations, which every background scan increments
   again, so it cannot tell a new failure from an old one. Dropped.

`pass` results are not needed for any of them: a new failure is a (resource, policy, rule) failing that was not failing
before. Kyverno keeps only `fail`, `error` and `warn` in its reports (`allowedResults`), which is enough.

## Decisions

- **A known failure** *(user, 2026-11-27)*: the alert keeps firing until the workload is fixed or a `PolicyException`
  with its reason excuses it. Until then a person may silence it in Alertmanager (silences expire). No accepted list.
- **No `for`, and `keep_firing_for: 15m`** *(user, 2026-11-27)*: one scrape that sees a failure fires the alert and keeps
  it long enough for Alertmanager to send it. A pod's report goes with the pod (the golden path's passing steps are
  deleted at once, `podGC: OnPodSuccess`), so a `for` would miss the short-lived pods Audit leaves open (G1: an
  operator's, a CI step's). The waves below remove the reason a `for` was first proposed (false failures on deploys).
- **Kyverno's `PolicyViolation` events alert too** *(user, 2026-11-27)*: a pod that lives less than about a minute
  (the scrape interval plus the reports controller's aggregation) may leave no series at all. Kyverno emits a
  `PolicyViolation` event at admission (its default `omitEvents` keeps them), and the OpenTelemetry gateway already sends
  Kubernetes events to Loki, so a Loki ruler rule catches what the metrics miss.
- **One failure, one identity** *(user, 2026-11-27)*: an alert is keyed by namespace, policy, rule and the workload at
  the top of the pod's owners, never by a pod's or ReplicaSet's name, so a rollout, a node restart or a CronJob's next
  run is not a new failure and a silence keeps holding. The top owner is the Deployment, StatefulSet, DaemonSet or
  CronJob; for a pod owned by anything else (an operator's resource, such as CloudNativePG's `Cluster`), that owner;
  for a pod with no owner, the pod. Pods of Argo Workflows are keyed by owner kind `Workflow` without its name (a new
  Workflow every run): a known failure in the golden path stays one label set across runs, and two steps breaking the
  same rule in the same namespace share it (accepted). Its real fix is the golden path's pods rendered for the policy
  check (10f's deferred minor).
- **Every `PolicyException` is applied before the workloads it excuses** *(user, 2026-11-27)*, which makes the short
  `for` quiet on deploys. Today Argo CD orders resources within a sync wave by kind, and kinds it does not know (a
  `PolicyException`) come last, after Deployments. So whenever a workload ships with its exception, its pods are
  admitted before the exception exists. They keep a false `fail` until the next background scan (an hour in
  production, six in the sandbox). `TestPolicy` works around it with a cutoff (it judges only pods created after the
  newest exception change). Every rendered `PolicyException` gets a sync wave lower than every workload in the same
  render, an offline test keeps it so, and `TestPolicy` drops its cutoff.
- **A Kyverno outage may hold a sync** *(user, 2026-11-27)*: if Kyverno's webhook for its own kinds refuses a
  `PolicyException` while Kyverno is down, Argo CD stops at the exception's wave and applies none of that Application's
  workloads until Kyverno is back. Running pods are not affected (their controllers recreate them, not Argo CD), and
  `system/kyverno` has no exception, so Kyverno can always sync itself. Accepted; the first task checks on the sandbox
  whether that webhook fails closed and writes the answer here.

## Design *(Claude's calls)*

- **Metrics** (`system/monitoring-system/values.yaml`, `kube-state-metrics.customResourceState`): PolicyReport
  (namespaced) and ClusterPolicyReport, `wgpolicyk8s.io/v1alpha2`. Per report the labels `namespace`, the scope's
  `kind` and `name`. One `Info` series per element of `results` (kube-state-metrics makes one series per array element),
  with `policy`, `rule` and `result`. `rbac.extraRules` adds `list` and `watch` on both kinds. kube-state-metrics stays
  as narrow as 15d left it (no Secrets).
- **Alert** (`system/kyverno/templates/prometheus-rule.yaml`, next to the policies, as Kargo keeps its rule in
  `platform/kargo`): `PolicyReportFail` when a result is `fail` or `error`, no `for`, `keep_firing_for: 15m`,
  `severity: warning`. The expression maps each report's scope to its identity (above) by joining kube-state-metrics'
  `kube_pod_owner`, `kube_replicaset_owner` and `kube_job_owner`, then aggregates by namespace, owner kind, owner name
  (empty for `Workflow`), policy, rule and result. Reports on the top workloads themselves (Kyverno's autogen rules
  judge Deployments, StatefulSets, DaemonSets and CronJobs too) map to themselves, so a violation reported on a
  Deployment and on its pods is one alert. Its description names the fix: correct the workload, or add a
  `PolicyException` beside it with `homelab/reason` (`docs/how-to-guides/policy.md`). Routing stays as it is: grouped by
  namespace (so one violation reported on its Deployment, ReplicaSet and Pod is one notification) and muted at night
  like every warning.
- **Events alert** (`system/loki/files/`, beside the audit rules, Loki's ruler): `PolicyViolationEvent` on Kyverno's
  `PolicyViolation` events (reporting component `kyverno-admission`), keyed like the metrics alert as far as the event
  allows (namespace, policy, the involved object's kind; a Pod's name reduced to its owner where the event names it,
  otherwise left out), `severity: warning`, `source: kyverno`. Grouped by namespace like the metrics alert. The builder
  checks an event's exact fields in the sandbox before writing the query.
- **Sync waves**: an annotation `argocd.argoproj.io/sync-wave` on each of the 24 `PolicyException`s (21 files under
  `apps/`, `platform/`, `system/`). The value is below the lowest wave of that render's workloads: `-1` where workloads
  sit at the default `0`, lower in charts that already order waves (cert-manager, monitoring-system, kargo).
- **Offline test** (`test/offline`): for every chart and app file, render it (sandbox and production values) and fail
  when a `PolicyException` has no wave lower than every Pod-bearing workload (Deployment, StatefulSet, DaemonSet, Job,
  CronJob, Pod, Rollout) in the same render. A render with exceptions but no workload (exceptions for pods an operator
  makes) only needs a wave below `0`, for uniformity.
- **Sandbox spec** (`test/e2e`, family `policy`, needs monitoring and logs): in a fresh namespace, a Deployment that
  breaks restricted: its result series appear in Prometheus and `PolicyReportFail` fires once for the Deployment (not
  per pod); deleting its pod (a new pod name) keeps one alert with the same labels; a bare pod that exits at once fires
  `PolicyViolationEvent` from Loki's ruler; deleting the namespace makes the series go. Its entry in `test/scopes.yaml` in the same change.
- **Proof that the race is gone**: `TestPolicy`'s "reports" drops the cutoff and expects no repository pod failing, on
  a `just dev-fresh` (every group). If a fresh cluster still shows false failures (Kyverno starting while the apps sync,
  before its caches hold the exceptions), the cutoff stays for that case only, the finding is written here, and the
  user hears it before anything else changes.

## Docs and records

- `docs/how-to-guides/policy.md`: the alert, what to do when it fires, the waves rule for new exceptions.
- The threat model's G1 and Order: the alert done.
- CLAUDE.md's Roadmap and the policy rule (a new `PolicyException` carries its wave).
- `docs/superpowers/deferred-minors.md`: what this step defers.

## Left to the builder

- The exact kube-state-metrics metric names (a prefix like `kyverno_policyreport`), within the repository's style.
- Which waves each chart's exceptions take, under the rule above.
- How the offline test walks the renders (reusing `lab test charts`' list or the existing render helpers).

## Found on the sandbox (2026-11-28)

- **Kyverno's webhooks for its own kinds fail closed**: `kyverno-exception-validating-webhook-cfg` and
  `kyverno-cel-exception-validating-webhook-cfg` (`policyexceptions`) are `failurePolicy: Fail`, as is the policy webhook;
  the resource webhooks for workloads are `Ignore`. So a `PolicyException` at wave `-1` holds its Application while
  Kyverno is down, as accepted above. The exceptions for k3s's own workloads (below) live in `system/kyverno` and go
  *after* its workloads, so Kyverno can always sync itself.
- **Events**: Kyverno emits `PolicyViolation` at admission (`reportingComponent: kyverno-admission`) and at each
  background scan (`kyverno-scan`), on the involved Pod, ReplicaSet and Deployment, and a copy on the `ValidatingPolicy`
  itself (in namespace `default`). Loki stores the OpenTelemetry gateway's watch record: the fields are under
  `object.` (`object.reason`, `object.reportingComponent`, `object.involvedObject.kind|name|namespace`), and the
  policy's name only in the message (`policy <name>/ fail: ...`). `PolicyViolationEvent` reads admission events only:
  a scan repeats every known failure each interval, which the reports already carry.
- **Report scopes**: a Deployment that breaks a policy gets results on its Deployment, its ReplicaSet and its Pod, each
  under the same policies; `results[].rule` is empty for a `ValidatingPolicy`.
- **k3s's own workloads fail**: in `kube-system`, CoreDNS (`disallow-capabilities-strict`, `require-run-as-nonroot`,
  `restrict-seccomp-strict`), metrics-server (`restrict-seccomp-strict`) and, in the sandbox, ServiceLB's `svclb-*`
  (seven policies; ServiceLB is off on the nodes). k3s rewrites its packaged manifests at every start, so they cannot
  be fixed here; production adds kube-vip (`infra/_modules/metal`, never judged by Kyverno: the testbed runs none).
  Without exceptions they would keep `PolicyReportFail` firing forever. Each gets a `PolicyException` in
  `kube-system` with its reason *(Claude's call, 2026-11-28: the decision that a known failure is fixed or excepted,
  never listed elsewhere)*; kube-vip's list is read from its manifest, so production's first reports confirm it.
- **A first-sync race, not this step's**: on this fresh cluster cert-manager's webhook was not answering when
  `system/opentelemetry` synced its operator's `Issuer` and `Certificate`; the operator waited for the Secret, its
  collectors stayed Progressing in the same wave, and Argo CD's operation never ended (no retry), so every `logs` spec
  failed until the operation was terminated by hand. Recorded in `docs/superpowers/deferred-minors.md`.
