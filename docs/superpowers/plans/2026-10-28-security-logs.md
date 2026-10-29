# Security logs (step 15c) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** k3s's API audit log and OpenBao's audit device reach Loki, are kept longer than other logs, are searchable in
Grafana, and a few dangerous events raise alerts through the existing Alertmanager.

**Architecture:** One audit policy file is passed to the API server by k3d (sandbox) and by `metal/roles/k3s`
(production, rendered only). Alloy tails the audit file on the node, and relabels OpenBao's audit lines out of its pod
log. Loki keeps `job=~"audit/.*"` streams longer (`retention_stream`), and its ruler evaluates LogQL alert rules from a
ConfigMap and sends them to Alertmanager.

**Tech Stack:** k3s 1.37 audit policy (`audit.k8s.io/v1`), Loki 3.7.8 (chart 18.17.0, ruler, `lokitool`), Alloy,
OpenBao 2.7.1, kube-prometheus-stack's Alertmanager, Grafana's dashboard sidecar, Go tests (terratest).

**Spec:** `docs/superpowers/specs/2026-10-28-security-logs-design.md`

## Global Constraints

- No secret value ever reaches Loki: a Secret's body is never logged (level `Metadata` at most for Secrets, ConfigMaps,
  TokenReviews, SubjectAccessReviews, ServiceAccount tokens), and OpenBao's HMAC of values stays on.
- No level `RequestResponse` anywhere; stage `RequestReceived` omitted (`omitStages`).
- Labels: `job="audit/k3s"` and `job="audit/openbao"`.
- Retention: audit 90 days (`2160h`) in production, other logs 30 days as today; the sandbox keeps 7 days for everything.
  If the measured volume says 90 days will not fit 10Gi, shorten to 60 days, then 45 or 30. Never raise the PVC.
- A person: not `system:serviceaccount:…`, `system:node:…`, `system:kube-…` or `system:apiserver`; `system:admin` is a
  person.
- Expected actors left out: Argo CD, ESO, the rotation CronJob, Velero, kaniop, Kyverno (their ServiceAccounts).
- OpenBao's no-audit window is 2h; k3s's is 15m.
- The two person alerts (exec/attach/port-forward and Secret read) are grouped by user in Alertmanager, so one tooling
  run sends one notification.
- Pinned versions: Loki's chart and image stay as they are; `lokitool` comes from nixpkgs' `grafana-loki`, which must
  match Loki's version (3.7.8); say so beside it if it does not.
- Never run `make dev` from the worktree; tests run from the main checkout after the branch is checked out there.

## Review Focus

1. **A secret value in an audit line.** A Secret `create`/`update` by Argo CD or ESO would put the value in a `Request`
   level body if a later rule catches it before the Secret rule. Expected: the Secret rule comes first. Pinned by
   `TestAuditPolicyRender` (rule order) and `TestSecurityLogsNoSecretValues` (Task 5).
2. **A k3s restart or audit file rotation.** Alloy must resume the rotated file without loss or repeat, and the
   no-audit alert must not fire. Expected: `audit.log` rotates at 100 MB, Alloy follows by name. Pinned by
   `TestNodeRestart` running after this branch with no `AuditLogsMissing` firing (Task 4's check in its last step).
3. **OpenBao's server log lines labelled as audit, or audit lines left as `job="pods"`.** Expected: only JSON lines
   with a `type` of `request` or `response` get `audit/openbao`. Pinned by `TestSecurityLogsOpenBao` (Task 2), which
   also checks the marker read is not under `job="pods"`.
4. **Expected actors raising person alerts.** Argo CD reading Secrets or Velero exec'ing (it does not, but kaniop and
   ESO read Secrets constantly) must not fire. Expected: the "person" filter drops every `system:serviceaccount:` user.
   Pinned by `TestSecurityLogsQuietWhenIdle` (Task 4): after the suite's start, no `AuditSecretReadByPerson` alert
   names a ServiceAccount.
5. **The ruler silently loading nothing.** A LogQL typo makes Loki drop a group. Expected: every group loads and each
   rule's health is `ok`. Pinned offline by `lokitool rules lint` in `TestSecurityLogsRules` and in the sandbox by
   `TestSecurityLogsRulesLoaded` (Task 4).

---

## File Structure

- Create `metal/roles/k3s/files/audit-policy.yaml`: the one audit policy (sandbox and production).
- Modify `infra/_modules/k3d/main.tf`, `infra/_modules/k3d/k3d.yaml`: mount the policy, pass the audit flags.
- Modify `metal/roles/k3s/defaults/main.yml`, `metal/roles/k3s/tasks/*.yml`: copy the policy, the same flags.
- Modify `system/loki/values.yaml`, `values-dev.yaml`: Alloy's audit source and OpenBao relabel, `retention_stream`,
  the ruler.
- Create `system/loki/files/audit-rules.yaml`, `system/loki/templates/audit-rules.yaml`: the rules and their ConfigMap.
- Modify `system/monitoring-system/values.yaml`: the route grouping the person alerts by user.
- Modify `system/monitoring-system/templates/authorization-policy.yaml`: Loki may call Alertmanager on 9093.
- Create `system/monitoring-system/templates/security-dashboard.yaml`, `system/monitoring-system/files/security-dashboard.json`.
- Modify `platform/openbao/values.yaml`: the audit device.
- Modify `flake.nix`: `grafana-loki` (for `lokitool`) in the default dev shell.
- Create `test/security_logs_test.go`; modify `scripts/ci-check` (offline tests), `test/scopes.yaml`.
- Create `docs/how-to-guides/security-logs.md`; modify `mkdocs.yml`, `CLAUDE.md`, `docs/superpowers/deferred-minors.md`.

---

### Task 1: k3s's audit log into Loki

**Files:**
- Create: `metal/roles/k3s/files/audit-policy.yaml`
- Modify: `infra/_modules/k3d/main.tf`, `infra/_modules/k3d/k3d.yaml`, `metal/roles/k3s/defaults/main.yml`, the k3s
  role's task that writes `config.yaml` (copy the policy beside it)
- Modify: `system/loki/values.yaml` (Alloy)
- Test: `test/security_logs_test.go`

**Interfaces:**
- Produces: on every server node, `/var/log/kubernetes/audit/audit.log` (JSON lines, `audit.k8s.io/v1` `Event`); Loki
  streams `{job="audit/k3s", node="<node>"}` whose line is the raw event JSON. Later tasks' LogQL uses `| json` fields
  `user_username`, `verb`, `objectRef_resource`, `objectRef_subresource`, `objectRef_namespace`, `objectRef_name`,
  `responseStatus_code`, `sourceIPs`.
- Produces: `auditPolicyPath = "../metal/roles/k3s/files/audit-policy.yaml"` and helper
  `auditEntries(t, query string, since time.Duration) []lokiEntry` (wraps `lokiQuery` from `logs_test.go`, retrying
  until non-empty for 5 minutes) in `test/security_logs_test.go`.

- [ ] **Step 1: Write the failing offline test `TestAuditPolicyRender`**

It parses the policy file (`sigs.k8s.io/yaml` into a local struct of `rules[].level`, `resources[].group/resources`,
`verbs`, `users`, `nonResourceURLs`, plus `omitStages`) and asserts:
- `omitStages` contains `RequestReceived`; no rule has level `RequestResponse`.
- The first rule naming `secrets`, `configmaps`, `tokenreviews`, `subjectaccessreviews`, `serviceaccounts/token`,
  `pods/exec`, `pods/attach` or `pods/portforward` has level `Metadata`, and no earlier rule matches those resources
  with another level than `None`.
- Some rule at level `Request` names `rbac.authorization.k8s.io` (roles, clusterroles, rolebindings,
  clusterrolebindings), `policyexceptions` (group `policies.kyverno.io`), kyverno policies, `admissionregistration.k8s.io`,
  `namespaces`, `apiextensions.k8s.io` `customresourcedefinitions`, with verbs `create, update, patch, delete`.
- The last rule is `level: Metadata` with no selectors.
- `infra/_modules/k3d/k3d.yaml` passes `--kube-apiserver-arg=audit-policy-file=/etc/rancher/k3s/audit-policy.yaml`,
  `audit-log-path=/var/log/kubernetes/audit/audit.log`, `audit-log-maxsize=100`, `audit-log-maxbackup=3`, and
  `metal/roles/k3s/defaults/main.yml`'s `kube-apiserver-arg` holds the same four values.

- [ ] **Step 2: Run it: FAIL (no file)**

Run: `cd test && go test -count=1 -run TestAuditPolicyRender ./...`

- [ ] **Step 3: Write the policy and the wiring**

- Policy order: `None` rules first (the spec's read-only noise: `get/list/watch` by `system:kube-*`, `system:node:*`
  groups `system:nodes`; leases; endpoints, endpointslices; events; nonResourceURLs `/healthz*`, `/readyz*`, `/livez*`,
  `/metrics`, `/version`), then the `Metadata` rule for sensitive resources, then the `Request` rule, then the catch-all
  `Metadata`. A `None` rule must never name the sensitive resources.
- k3d: mount the file with `--volume "<abspath>:/etc/rancher/k3s/audit-policy.yaml@server:*"` in `main.tf` beside the
  Gateway API manifest, and add `filesha1(<policy>)` to `triggers_replace` (a policy change rebuilds the sandbox
  cluster, as a `k3d.yaml` change does). The four flags go in `k3d.yaml`'s `extraArgs`.
- metal: copy the file to `/etc/rancher/k3s/audit-policy.yaml` before k3s starts (servers only); the four flags in
  `k3s_server_config.kube-apiserver-arg`.
- Alloy: a hostPath volume `/var/log/kubernetes/audit` (`DirectoryOrCreate`, mounted read-only), a `local.file_match`
  on `audit.log` with labels `job="audit/k3s"` and `node` from `K8S_NODE_NAME`, `loki.source.file` into
  `loki.write.loki` (no `stage.cri`: the file is plain JSON). Agents have no audit file; the match finds nothing there.

- [ ] **Step 4: Run it: PASS**

Run: `cd test && go test -count=1 -run TestAuditPolicyRender ./...`; add `TestAuditPolicyRender` to
`offline_tests` in `scripts/ci-check`.

- [ ] **Step 5: Write the sandbox test `TestSecurityLogsK3sExec`**

`logPod(t, "audit")` (from `logs_test.go`), then `printTo` a marker. Assert within 5 minutes that
`{job="audit/k3s"} | json | objectRef_subresource="exec" | objectRef_name="audit"` returns a line whose
`user_username` is the kubeconfig's user (`system:admin` in k3d) and whose `objectRef_namespace` is `logs-test`.

- [ ] **Step 6: Commit**, then `scripts/ci-check all`; the sandbox run is Task 5's.

---

### Task 2: OpenBao's audit device

**Files:**
- Modify: `platform/openbao/values.yaml`, `system/loki/values.yaml` (Alloy's `loki.process "pods"`)
- Test: `test/security_logs_test.go`

**Interfaces:**
- Produces: Loki streams `{job="audit/openbao", namespace="openbao", pod=…}`, line = OpenBao's audit JSON
  (`type`, `auth.display_name`, `auth.policies`, `request.path`, `request.operation`, `request.remote_address`,
  `error`). Pod log lines of OpenBao that are not audit stay `job="pods"`.
- Consumes: `auditEntries` (Task 1).

- [ ] **Step 1: Write the sandbox test `TestSecurityLogsOpenBao`**

With the admin role (the helper the secrets tests use to write OpenBao), write `secret/smoke/audit-marker` with a
random value, read it back, then assert within 5 minutes:
- `{job="audit/openbao"} |= "secret/data/smoke/audit-marker"` has a `response` line for the read.
- No line in `{namespace="openbao"}` over the last 10 minutes contains the random value (fetch and `strings.Contains`
  in Go; never put the value in a query or an argument).
- `{job="pods", namespace="openbao"} |= "secret/data/smoke/audit-marker"` is empty.
Delete the marker in `t.Cleanup`.

- [ ] **Step 2: Declare the device**

In the server config: `audit "file" "stdout" { options = { file_path = "/dev/stdout" } }` (OpenBao 2.x declarative
audit devices). Check on 2.7.1 by rendering and, on the sandbox, `bao audit list` with the admin role. If 2.7.1 refuses
config-declared devices, add an `initialize "audit"` stanza with request `sys/audit/stdout` (type `file`,
`options.file_path=/dev/stdout`) instead, and note that a running OpenBao needs it once by hand (the guide says how).

- [ ] **Step 3: Relabel in Alloy**

In `loki.process "pods"` after `stage.cri`: a `stage.match` with selector
`{namespace="openbao", container="openbao"} |~ "^\\{.*\"type\":\"(request|response)\""` whose `stage.static_labels`
sets `job = "audit/openbao"`.

- [ ] **Step 4: Commit.** The sandbox run is Task 5's.

---

### Task 3: retention

**Files:**
- Modify: `system/loki/values.yaml`, `system/loki/values-dev.yaml`
- Test: `test/security_logs_test.go`

- [ ] **Step 1: Write the offline test `TestSecurityLogsRetention`**

Render `system/loki` (`scripts/render-chart system/loki`) and `system/loki dev`; from the Loki config in the rendered
ConfigMap assert `limits_config.retention_period` is `720h` (production) / `168h` (dev), and `retention_stream` holds
one entry with selector `{job=~"audit/.*"}` and period `2160h` (production) / `168h` (dev). Add it to `offline_tests`.

- [ ] **Step 2: Run: FAIL. Step 3: set the values** (dev restates the whole `retention_stream` list: Helm replaces
lists). **Step 4: Run: PASS. Step 5: Commit.**

The volume measurement and the final period are Task 5's last steps.

---

### Task 4: alert rules, the ruler and Alertmanager

**Files:**
- Create: `system/loki/files/audit-rules.yaml`, `system/loki/templates/audit-rules.yaml`
- Modify: `system/loki/values.yaml` (ruler), `system/monitoring-system/values.yaml`,
  `system/monitoring-system/templates/authorization-policy.yaml`, `flake.nix`
- Test: `test/security_logs_test.go`

**Interfaces:**
- Produces: ConfigMap `loki/loki-audit-rules`, key `audit-rules.yaml` (Prometheus-style rule groups with LogQL),
  mounted at `/etc/loki/rules/fake/` (tenant `fake`, `auth_enabled: false`); every alert carries labels `severity`
  and `source="audit"`, the person alerts also `user`, and an annotation `summary` naming the user, the object and a
  LogQL query to paste in Explore.
- Alert names (exact): `AuditExecByPerson` (warning), `AuditSecretReadByPerson` (warning), `AuditClusterAdminBinding`
  (critical), `AuditAnonymousAllowed` (critical), `AuditManyDenied` (warning), `AuditPolicyExceptionChanged` (warning),
  `OpenBaoRootToken` (critical), `OpenBaoManyFailedLogins` (warning), `OpenBaoConfigChanged` (critical),
  `AuditLogsMissing` (critical, two rules: `job="audit/k3s"` over 15m, `job="audit/openbao"` over 2h).

- [ ] **Step 1: Write the offline test `TestSecurityLogsRules`**

Render `system/loki`; extract the ConfigMap's `audit-rules.yaml` to a temp file; run `lokitool rules lint <file>` and
`lokitool rules check <file>` (both exit 0); parse it and assert the set of alert names equals the list above, each
with its severity, `source="audit"`, and a non-empty `summary`; the person rules' expressions contain the person
filter (a regex excluding `system:serviceaccount:.*|system:node:.*|system:kube-.*|system:apiserver`) and
`AuditClusterAdminBinding`/`AuditPolicyExceptionChanged` exclude Argo CD's ServiceAccounts. Also render
`system/monitoring-system` and assert a route matching `source="audit"` and `alertname=~"AuditExecByPerson|AuditSecretReadByPerson"`
with `group_by: [alertname, user]`, and that `allow` admits `cluster.local/ns/loki/sa/loki` on port 9093. Add to
`offline_tests`.

- [ ] **Step 2: Run: FAIL.**

- [ ] **Step 3: Write the rules, the ruler and the wiring**

- Rules: `count_over_time` / `sum by (user_username, …)` over the window that fits each row (5m for "many";
  `[5m] > 0` for single events, `for: 0m`); `absent_over_time` for `AuditLogsMissing`. Expected actors are excluded by
  ServiceAccount in every rule (namespaces `argocd`, `external-secrets`, `secrets-rotation`, `velero`, `kanidm`
  (kaniop: check its namespace), `kyverno`). OpenBao's "known flows": requests whose `auth.policies` are only the
  `rotator`, `writer`, `infra` or a store's read policy are excluded; `admin` is not. Thresholds for the "many" rules
  start at 20 in 5 minutes and are tuned in Task 5.
- Ruler in Loki's values: `loki.rulerConfig` with `storage: {type: local, local: {directory: /etc/loki/rules}}`,
  `rule_path: /var/loki/rules-temp`, `alertmanager_url:
  http://monitoring-system-kube-pro-alertmanager.monitoring-system:9093`, `enable_api: true`; the ConfigMap mounted
  through `singleBinary.extraVolumes`/`extraVolumeMounts`. (Check the chart: if the monolithic target needs `ruler`
  enabled explicitly, do that.)
- Alertmanager: a route before the default, matching `source="audit"` for the two person alerts, `group_by:
  [alertname, user]`, receiver `ntfy`; `allow` gets the Loki principal on 9093. Run `scripts/check-mesh-policies`; add
  an `EXTRA` entry only if it asks.
- `flake.nix`: `grafana-loki` in the default shell's packages (comment: for `lokitool`, matching Loki 3.7.8).

- [ ] **Step 4: Run: PASS. Step 5: Write the sandbox tests**

- `TestSecurityLogsRulesLoaded`: Loki's `/prometheus/api/v1/rules` (through the service proxy, like `lokiQuery`)
  lists every alert name above with `health: ok`.
- `TestSecurityLogsExecAlert`: after an exec into `logs-test/audit` (Task 1's pod), Alertmanager's `/api/v2/alerts`
  (from inside its pod, like `postAlert`) holds `AuditExecByPerson` with `user="system:admin"` within 5 minutes.
- `TestSecurityLogsQuietWhenIdle`: no active `Audit*` alert has a `user` starting `system:serviceaccount:`, and
  `AuditLogsMissing` is not firing.

- [ ] **Step 6: Commit.**

---

### Task 5: dashboard, docs, scopes, the sandbox proof and the volume

**Files:**
- Create: `system/monitoring-system/templates/security-dashboard.yaml` (ConfigMap with label `grafana_dashboard: "1"`),
  `system/monitoring-system/files/security-dashboard.json`, `docs/how-to-guides/security-logs.md`
- Modify: `mkdocs.yml`, `test/scopes.yaml`, `CLAUDE.md`, `docs/superpowers/deferred-minors.md`
- Test: `test/security_logs_test.go`

- [ ] **Step 1: `TestSecurityLogsNoSecretValues` (sandbox)**: read three generated values from their apps' Secrets
  (Grafana's admin password, ntfy's alerts token, one Forgejo token) and assert no line of `{job=~"audit/.*"}` over the
  last 2 hours contains any (fetch in pages of 5000, `strings.Contains` in Go; never print the values).

- [ ] **Step 2: Dashboard** "Security", data source uid `loki`, panels: exec/attach/port-forward by user, Secret reads
  by persons, RBAC and PolicyException changes, 401/403 by source IP, OpenBao logins and reads by role and path. Its
  queries are the guide's. **Scopes:** a group `[metal/roles/k3s/files/audit-policy.yaml]` → `Logs`; check
  `infra/_modules/k3d/**`, `system/loki/**`, `platform/openbao/**` and `system/monitoring-system/**` already select
  `Logs` (`TestSecurityLogs*` match it).

- [ ] **Step 3: Guide** `docs/how-to-guides/security-logs.md`: where the logs are, the LogQL queries, each alert and
  what to do, the tooling noise (accepted, grouped by user), that Loki is in-cluster (cluster-admin can delete it), how
  to add the OpenBao device by hand if Task 2 fell back to `initialize`. `CLAUDE.md`: 15c done (one line in the
  roadmap), the Sandbox section's Loki line (audit streams, ruler), the "before production" items (the policy on the
  real nodes, the volume against 10Gi, the period chosen). Deferred minors for anything the review leaves.

- [ ] **Step 4: Prove it in the sandbox** (main checkout, branch checked out there, `pgrep -x terragrunt; pgrep -x make`
  empty): `make dev`. The policy changes the k3d config, so the cluster is rebuilt; expect the whole selection. Then
  `make dev tests='NodeRestart'` and check `AuditLogsMissing` never fired (Alertmanager's alerts).

- [ ] **Step 5: Measure and choose the period**: after the suite, `sum(bytes_over_time({job=~"audit/.*"}[1h]))` and
  Loki's chunk bytes on disk for those streams; scale the idle hour's rate (not the suite's) to a day, take production
  as three times the sandbox's API traffic, and compute 90 days plus 30 days of the other logs (their measured rate)
  against 10Gi with 20% headroom. Keep 90 days if it fits; else 60, else 45, else 30. Write the numbers and the period
  in the PR body, `values.yaml` beside `retention_stream`, and `CLAUDE.md`'s production line. Tune the "many"
  thresholds from the same idle hour.

- [ ] **Step 6: `scripts/ci-check all`, commit, final review (opus), PR into `dev`.**
