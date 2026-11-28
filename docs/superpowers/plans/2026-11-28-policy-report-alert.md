# G1: Policy Report Alert Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Alert once per (namespace, top owner, policy, rule) when Kyverno reports a new `fail` or `error`. Apply every
`PolicyException` before the workloads it excuses, so deploys raise no false alert.

**Architecture:** kube-state-metrics' `customResourceState` exports one series per PolicyReport result. Recording rules
map each report's scope to its top owner, using `kube_pod_owner`, `kube_replicaset_owner` and `kube_job_owner`.
`PolicyReportFail` alerts on that result. A Loki ruler rule on Kyverno's `PolicyViolation` events catches pods too
short-lived to leave a series. `scripts/check-policy` also refuses any `PolicyException` whose sync wave is not below
every workload's in the same render.

**Tech Stack:** Helm wrapper charts, kube-prometheus-stack (kube-state-metrics 2.20), Kyverno 1.19, Loki 3.7 ruler,
Argo CD sync waves, Go offline tests (testify), Ginkgo e2e, promtool, lokitool.

**Spec:** `docs/superpowers/specs/2026-11-27-policy-report-alert-design.md`

## Global Constraints

- Results alerted: `fail` and `error`, never `warn` or `pass`.
- `PolicyReportFail`: no `for`, `keep_firing_for: 15m`, `severity: warning`. It has no `source` label, so it is routed
  by namespace and muted at night like every other warning.
- Identity labels: `namespace`, `owner_kind`, `owner_name`, `policy`, `rule`, `result`.
- The owner is the Deployment, StatefulSet, DaemonSet or CronJob at the top of the pod's owners. A pod owned by
  anything else maps to that owner. A pod with no owner maps to itself.
- For `owner_kind="Workflow"`, `owner_name` is empty.
- The identity never uses a pod's or ReplicaSet's name.
- Loki rule `PolicyViolationEvent`: `severity: warning`, `source: kyverno`.
- Sync wave rule: each rendered `PolicyException` has `argocd.argoproj.io/sync-wave` strictly below the lowest wave
  among the Deployments, StatefulSets, DaemonSets, Jobs, CronJobs, Pods and Rollouts in the same render. A missing
  wave counts as `0`. With no workload in the render, the exception's wave must be below `0`.
- kube-state-metrics stays without Secrets.
- Every component's PromQL and LogQL in its own files follows the repository's comment style: one comment above each
  rule saying why.
- Versions: the newest stable `prometheus` (promtool) in nixpkgs. Pin nothing older without a reason beside it.
- Stage named paths only (never `git add -A`).

## Review Focus

1. A Deployment rollout gives the failing pod a new name, and that must not fire a second notification. Covered by
   the promtool case `rollout keeps one alert` (Task 3).
2. A CronJob's next Job and pod must keep the same identity (`owner_kind=CronJob`). Covered by the promtool case
   `cronjob runs map to the cronjob` (Task 3).
3. A bare pod with no owner, and a pod owned by a CloudNativePG `Cluster`, must each alert with their own identity.
   Covered by the promtool case `bare and operator pods` (Task 3).
4. A chart whose workloads already sit at a negative wave must still need its exception's wave below them. Covered by
   the `TestCheckPolicy` case `exception wave below a negative workload wave` (Task 2).
5. A failing result that disappears must keep its alert firing for 15 minutes, then resolve. Covered by the promtool
   case `keep firing after the pod is gone` (Task 3).

---

### Task 1: Facts the design leaves to the sandbox

Three facts decide details below. Read them on a running sandbox before writing those parts, and record them in the
spec.

**Files:**
- Modify: `docs/superpowers/specs/2026-11-27-policy-report-alert-design.md` (a section `## Found on the sandbox`)

- [ ] **Step 1: Bring up the sandbox with Kyverno, monitoring and Loki**

Run: `just dev --tests 'policy || logs'` from `/home/nixos/homelab` (never a worktree).
Expected: the specs pass, and the run leaves the cluster up.

- [ ] **Step 2: Record whether Kyverno's webhook for its own kinds fails closed**

Run: `kubectl get validatingwebhookconfigurations -l webhook.kyverno.io/managed-by=kyverno -o json | jq -r '.items[] | .metadata.name as $n | .webhooks[] | "\($n) \(.name) \(.failurePolicy) \(.rules | map(.resources[]) | join(","))"'`
Expected: a line for each webhook. Write down the `failurePolicy` of the one matching `policyexceptions`, or "no
webhook" if none matches.

- [ ] **Step 3: Record a PolicyViolation event's fields as Loki stores them**

Create a pod that breaks restricted:
`kubectl create ns g1-probe && kubectl -n g1-probe run bare --image=docker.io/library/busybox:1.37.0 --restart=Never -- true`.
Then query Loki for `{service_name="kubernetes-events"} |= "PolicyViolation"` over the last 10 minutes, the way
`lokiQuery` in `test/e2e/logs_test.go` does.
Expected: at least one line. Write down the JSON paths of the reason, the involved object's kind, name and namespace,
the reporting component, and where the policy's name appears (a field or only the message). Then
`kubectl delete ns g1-probe`.

- [ ] **Step 4: Record which kinds Kyverno reports for a Deployment**

Create a Deployment that breaks restricted in a fresh namespace. Run
`kubectl get policyreports.wgpolicyk8s.io -n <ns> -o jsonpath='{range .items[*]}{.scope.kind}{" "}{.results[*].rule}{"\n"}{end}'`.
Expected: the scope kinds that carry results (Deployment, ReplicaSet, Pod). Write them down, and whether
`results[].rule` is set for a ValidatingPolicy. Delete the namespace.

- [ ] **Step 5: Commit**

```bash
git add docs/superpowers/specs/2026-11-27-policy-report-alert-design.md
git commit -m "docs(g1): what the sandbox showed for webhooks, events and report scopes"
```

If Step 2 shows that a webhook fails closed, nothing changes: the spec already accepts this. If Step 3 shows no
`PolicyViolation` events, stop and tell the user before Task 4.

---

### Task 2: Exceptions before their workloads

**Files:**
- Modify: `scripts/check-policy` (a wave check over each target's render)
- Modify: `test/offline/policy_test.go` (`TestCheckPolicy` cases)
- Modify: every `PolicyException` under `apps/`, `platform/`, `system/`. List them with
  `grep -rln "kind: PolicyException" apps platform system --include=*.yaml | grep -v system/crds`, which gives 21 files
  and 24 exceptions.

**Interfaces:**
- Produces: `scripts/check-policy` fails with a line `wave: <target>: PolicyException <ns>/<name> at <w>, workloads from <min>`
  for each offender. With `WORKLOADS=<file>`, it judges that file as one render.

- [ ] **Step 1: Write the failing tests**

Add these cases to `TestCheckPolicy`. They reuse its `check` helper, which writes a file and runs with `WORKLOADS`.
- `exception wave below workloads`: secure Deployment plus an exception with wave `-1`. Expect no error.
- `exception without wave`: the same exception with no annotation. Expect an error, and output containing
  `wave: ` and `PolicyException fixture/`.
- `exception wave below a negative workload wave`: a Deployment annotated wave `-2` and an exception at `-1`. Expect
  an error.
- `exception alone`: an exception at `-1` with no workload. Expect no error. The same exception without a wave
  expects an error.

- [ ] **Step 2: Run them to see them fail**

Run: `direnv exec . go test ./test/offline -run TestCheckPolicy -v`
Expected: FAIL on `exception without wave` and on `exception wave below a negative workload wave`.

- [ ] **Step 3: Implement the wave check in `scripts/check-policy`**

For each render (each target, or `WORKLOADS`), use yq to read the exceptions' waves and the workloads' waves, with
Rollout included and a missing wave read as 0. Compare them in POSIX sh, and print the line above. Exit non-zero
after the policy run if any line was printed. Production renders are enough, since waves do not differ by
environment.

- [ ] **Step 4: Run the tests to see them pass**

Run: `direnv exec . go test ./test/offline -run TestCheckPolicy -v`
Expected: PASS.

- [ ] **Step 5: See the repository fail, then annotate every exception**

Run: `direnv exec . scripts/check-policy`
Expected: one `wave:` line per exception (24).

Then add the annotation `argocd.argoproj.io/sync-wave: "-1"`. Use a lower wave where that render's workloads sit
below `0` (check `cert-manager`, `monitoring-system` and `kargo`'s renders). In `apps/*.yaml`, it goes under the
`rawResources` entry's `annotations`. Above each, add one comment line: `# Before its workloads: admitted first, they would keep a false fail until the next background scan`.

- [ ] **Step 6: Run the check and the renders**

Run: `direnv exec . scripts/check-policy && direnv exec . scripts/ci-check render`
Expected: `check-policy: every workload passes`, and the render check passes.

- [ ] **Step 7: Commit**

```bash
git add scripts/check-policy test/offline/policy_test.go <the 21 files>
git commit -m "feat(g1): PolicyExceptions apply before their workloads, checked by check-policy"
```

---

### Task 3: Report metrics and PolicyReportFail

**Files:**
- Modify: `flake.nix` (add `prometheus` for promtool, with a comment naming `test/offline/policy_report_rule_test.go`)
- Modify: `system/monitoring-system/values.yaml` (`kube-state-metrics.customResourceState`, `rbac.extraRules`)
- Create: `system/kyverno/templates/prometheus-rule.yaml`
- Create: `test/offline/policy_report_rule_test.go`
- Create: `test/offline/fixtures/policy-report-rule-tests.yaml` (promtool's test file; it loads the rendered rule)

**Interfaces:**
- Produces: the metric `kyverno_policyreport_result` with labels `namespace`, `kind`, `name`, `policy`, `rule` and
  `result`, plus ksm's `customresource_*`. Also `kyverno_clusterpolicyreport_result` with `kind`, `name`, `policy`,
  `rule`, `result`.
- Produces: the recording rule `kyverno:policy_result:owner` with labels `namespace`, `owner_kind`, `owner_name`,
  `policy`, `rule`, `result`, value 1.
- Produces: the alert `PolicyReportFail`, built from `kyverno:policy_result:owner{result=~"fail|error"}` and the
  cluster metric. Cluster reports map `owner_kind`/`owner_name` from their own `kind`/`name`.

- [ ] **Step 1: Write the failing test**

Write `TestPolicyReportRule` in `test/offline/policy_report_rule_test.go`.
1. It renders `system/kyverno` with `render.Yq` and selects the `PrometheusRule` `kyverno-policy-reports`. It asserts:
   - alert `PolicyReportFail` with `keep_firing_for` `15m`, no `for`, and severity `warning`;
   - the description containing `PolicyException` and `docs/how-to-guides/policy.md`.
2. It writes the rule's `spec` to a temp file and runs `promtool test rules` on the fixture, which names that file
   through `rule_files`. Copy the file into the temp dir, or template its path. Expect exit 0.
3. It renders `system/monitoring-system` and asserts that ksm's RBAC lists `policyreports` and
   `clusterpolicyreports` (`wgpolicyk8s.io`) with only `list` and `watch`.

Fixture cases, each giving input series and expected alerts at an evaluation time:
- `deployment and its pods are one alert`: `kyverno_policyreport_result` for `kind=Deployment name=web`, `kind=ReplicaSet name=web-5d`
  and `kind=Pod name=web-5d-abc`, all with `policy=disallow-privilege-escalation`, `rule=autogen-check`,
  `result=fail`. Add `kube_pod_owner{pod="web-5d-abc",owner_kind="ReplicaSet",owner_name="web-5d"}` and
  `kube_replicaset_owner{replicaset="web-5d",owner_kind="Deployment",owner_name="web"}`. Expected: one
  `PolicyReportFail` with `owner_kind=Deployment owner_name=web`. If Task 1 recorded that the rules differ by scope
  kind, use the kinds and rules it recorded.
- `rollout keeps one alert`: the Pod series ends at 5m and `web-5d-xyz` (from the new ReplicaSet `web-7f`, owned by
  `web`) starts. Expected at 6m: still exactly one alert with the same labels.
- `cronjob runs map to the cronjob`: Pod `backup-28911-q` → Job `backup-28911` → CronJob `backup`. Expected:
  `owner_kind=CronJob owner_name=backup`.
- `bare and operator pods`: Pod `bare` with `kube_pod_owner{owner_kind="<none>"}` gives `owner_kind=Pod owner_name=bare`.
  Pod `forgejo-db-1` with owner `Cluster/forgejo-db` gives `owner_kind=Cluster owner_name=forgejo-db`.
- `workflow pods share an identity`: pods `p-1` and `p-2` owned by Workflows `pipeline-aaa` and `pipeline-bbb` give
  one alert with `owner_kind=Workflow owner_name=""`.
- `keep firing after the pod is gone`: a series present 0–2m. Expected: firing at 10m, none at 20m.
- `pass and warn do not alert`: `result=pass` and `result=warn` give no alert.

- [ ] **Step 2: Run it to see it fail**

Run: `direnv exec . go test ./test/offline -run TestPolicyReportRule -v`
Expected: FAIL. The rule does not exist (and promtool is missing until `flake.nix` gains it).

- [ ] **Step 3: Add promtool, the metrics and the rule**

- `flake.nix`: add `prometheus` to the dev shell.
- `values.yaml`: add two `customResourceState` resources beside Kargo's. Use `metricNamePrefix`
  `kyverno_policyreport` / `kyverno_clusterpolicyreport`, and one metric `result` of `each.type: Info` on
  `path: [results]`. Labels relative to the element are `policy: [policy]`, `rule: [rule]`, `result: [result]`. The
  report's labels are `namespace: [metadata, namespace]`, `kind: [scope, kind]`, `name: [scope, name]`. Add `list`
  and `watch` to `rbac.extraRules`.
- The rule: recording rules in steps (pod → its owner, ReplicaSet → Deployment, Job → CronJob, top kinds →
  themselves, Workflow name blanked) into `kyverno:policy_result:owner`, using `label_replace` and
  `group_left(owner_kind, owner_name)` joins on `namespace` plus the pod, ReplicaSet or Job name. Then
  `PolicyReportFail` is `max by (namespace, owner_kind, owner_name, policy, rule, result) (...) > 0`.
  - Summary: `{{ $labels.owner_kind }} {{ $labels.namespace }}/{{ $labels.owner_name }} breaks {{ $labels.policy }}`.
  - Description: fix the workload, or add a `PolicyException` beside it with `homelab/reason` and a sync wave before
    it (`docs/how-to-guides/policy.md`); `kubectl get policyreports -n {{ $labels.namespace }}`.
  - Escape the `{{` for Helm, as `node-patches-rule.yaml` does.

- [ ] **Step 4: Run it to see it pass**

Run: `direnv exec . go test ./test/offline -run 'TestPolicyReportRule|TestMonitoring' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add flake.nix flake.lock system/monitoring-system/values.yaml system/kyverno/templates/prometheus-rule.yaml test/offline/policy_report_rule_test.go test/offline/fixtures/policy-report-rule-tests.yaml
git commit -m "feat(g1): PolicyReportFail from kube-state-metrics, keyed by the top owner"
```

(`flake.lock` only if it changed.)

---

### Task 4: PolicyViolationEvent from Loki

**Files:**
- Create: `system/loki/files/policy-rules.yaml`
- Modify: `system/loki/templates/audit-rules.yaml` (mount `policy-rules.yaml` like `falco-rules.yaml`)
- Modify: `test/offline/security_logs_test.go` (lint the new file)

**Interfaces:**
- Consumes: the event fields Task 1 recorded.
- Produces: the alert `PolicyViolationEvent`, labels `namespace`, `kind`, `policy` (when the event carries it),
  `severity=warning`, `source=kyverno`.

- [ ] **Step 1: Write the failing test**

Write `TestPolicyViolationRule`. It renders `loki-audit-rules`, takes `data["policy-rules.yaml"]`, and runs
`lokitool rules lint` and `rules check` as `TestSecurityLogsRules` does. It asserts:
- the one alert `PolicyViolationEvent`, with labels `severity: warning` and `source: kyverno`;
- an expression containing `{service_name="kubernetes-events"}` and `PolicyViolation`, and no pod name in its
  `by (...)`.

- [ ] **Step 2: Run it to see it fail**

Run: `direnv exec . go test ./test/offline -run TestPolicyViolationRule -v`
Expected: FAIL, because there is no `policy-rules.yaml`.

- [ ] **Step 3: Write the rule**

`sum by (namespace, kind, policy) (count_over_time({service_name="kubernetes-events"} |= "PolicyViolation" | json ... | reason="PolicyViolation" [5m])) > 0`,
with the json paths Task 1 recorded. The `kind` is the involved object's. The description says the same as
`PolicyReportFail`'s and gives the Explore query. Add a comment above saying why: pods gone before a scrape leave no
series.

- [ ] **Step 4: Run it to see it pass**

Run: `direnv exec . go test ./test/offline -run 'TestPolicyViolationRule|TestSecurityLogsRules' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add system/loki/files/policy-rules.yaml system/loki/templates/audit-rules.yaml test/offline/security_logs_test.go
git commit -m "feat(g1): PolicyViolationEvent from Kyverno's events in Loki"
```

---

### Task 5: The sandbox proves it

**Files:**
- Create: `test/e2e/policy_alerts_test.go`
- Modify: `test/e2e/policy_test.go` (drop the cutoff in "reports")
- Modify: `test/scopes.yaml` (the new spec's label and its groups)

**Interfaces:**
- Consumes: `prometheusQuery` (`monitoring_test.go`), `activeAlerts` (`security_logs_test.go`), `kube.CreateNamespace`,
  `kube.DeleteNamespace`, `kube.Kubectl`.

- [ ] **Step 1: Write the spec `PolicyAlerts`**

Labels: `sandbox` and `policy-alerts`. In a namespace `policy-alert-<unix>`:
- a Deployment `web` (busybox `sleep 3600`, no securityContext). Within 5 minutes, `kyverno:policy_result:owner{namespace=<ns>,owner_kind="Deployment",owner_name="web"}`
  exists, and exactly one active `PolicyReportFail` has those labels.
- `kubectl delete pod -l app=web`. When the new pod is reported, still exactly one alert with the same labels.
- a bare pod `brief` (`--restart=Never -- true`). Within 5 minutes, an active `PolicyViolationEvent` with
  `namespace=<ns>`.
- delete the namespace. Within 5 minutes, no `kyverno_policyreport_result{namespace=<ns>}`.

`SpecTimeout(20*time.Minute)`.

- [ ] **Step 2: Scope it**

In `test/scopes.yaml`, the label `policy-alerts` deploys Kyverno, monitoring and Loki. Add it to a group whose
`needs` bring those. Keep the existing groups' deploys unchanged where possible, and add a comment saying why. Add
`policy-alerts` to `full:`.
Run: `direnv exec . just cli check` and `direnv exec . go test ./test/offline -run 'TestE2EKeepsSelection|TestScopes' -v`.
Expected: PASS.

- [ ] **Step 3: Drop TestPolicy's cutoff**

In "reports", judge every pod outside `outside(...)`, not only those after the newest exception. Remove the exception
listing, `newest` and `cutoff`, and update the comment above it: exceptions now apply before their workloads (sync
waves).

- [ ] **Step 4: Run the basic checks**

Run: `direnv exec . scripts/ci-check all`
Expected: every check passes.

- [ ] **Step 5: Commit, then run the scoped sandbox**

```bash
git add test/e2e/policy_alerts_test.go test/e2e/policy_test.go test/scopes.yaml
git commit -m "test(g1): PolicyAlerts on the sandbox; TestPolicy without its cutoff"
```
Run: `just dev`
Expected: the selected specs pass, `PolicyAlerts` and `Policy` among them.

- [ ] **Step 6: The race is gone on a fresh cluster**

Run: `just dev-fresh`
Expected: the whole suite passes, and `Policy`'s "reports" finds no failing repository pod.

If it does find failing pods: check whether they were admitted before Kyverno was up. If so, restore the cutoff for
that case only, with a comment, write the finding into the spec, and tell the user before going on.

---

### Task 6: Docs and records

**Files:**
- Modify: `docs/how-to-guides/policy.md`
- Modify: `docs/superpowers/specs/2026-11-14-threat-model-design.md` (G1 and Order)
- Modify: `CLAUDE.md` (Roadmap order, the policy rule under Rules)
- Modify: `docs/superpowers/deferred-minors.md`

- [ ] **Step 1: Write them**

- `policy.md`, a section "When it alerts":
  - what `PolicyReportFail` and `PolicyViolationEvent` mean, and how to read the report;
  - fix the workload, or add the exception with `homelab/reason` and a wave before its workloads (check-policy
    refuses it otherwise);
  - silence a known failure by its labels (silences expire);
  - a Workflow's pods share one identity.
- Threat model:
  - G1 gets a paragraph: the alert is done (2026-11-28), naming this spec. The window's other conditions stay.
  - Order item 1: "G1's alert on new `fail` results (done 2026-11-28)".
- CLAUDE.md:
  - In the Roadmap's order, before LAN DNS: "→ G1's alert (done: `docs/superpowers/specs/2026-11-27-policy-report-alert-design.md`: `PolicyReportFail` from kube-state-metrics keyed by the top owner, `PolicyViolationEvent` from Loki, every `PolicyException` a sync wave before its workloads)".
  - The Rules line on securityContext: "a `PolicyException` with its reason and a sync wave before its workloads".
- `deferred-minors.md`:
  - add whatever this step defers, for example a dashboard panel, and the testbed's `NodePatches` still owed from
    #119 if no testbed run happened in this step;
  - remove any line this step fixes.

- [ ] **Step 2: Check**

Run: `direnv exec . sh -c 'mkdocs build --strict && scripts/ci-check all'`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add docs/how-to-guides/policy.md docs/superpowers/specs/2026-11-14-threat-model-design.md CLAUDE.md docs/superpowers/deferred-minors.md
git commit -m "docs(g1): the policy alerts, the waves rule, the threat model and the Roadmap"
```

This step changes no path the testbed gates: the exceptions in `system/istio-system` and `system/rook-ceph` are in
`infra/testbed/release-paths.yaml`'s `not_run`. So no testbed run is needed, and `NodePatches` stays owed.
