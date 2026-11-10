# Runtime Detection (15g) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Falco runs in every environment and only detects. Its events reach Loki and alert through Alertmanager, and a
stalled Falco alerts too.

**Architecture:**
- **Chart:** `system/falco` is a wrapper chart of `falcosecurity/falco`:
  - the `modern_ebpf` driver, JSON events to stdout from `warning` up, no falcoctl;
  - accepted events rendered from `values.yaml`'s `accepted` list into a rules file of its own.
- **Event alerts:** Alloy already ships the pods' logs to Loki, and `system/loki/files/falco-rules.yaml` alerts on them.
- **Health alerts:** a `PrometheusRule` in `system/falco` alerts on Falco's health from its metrics.
- **Proof:** Ginkgo specs on the sandbox and the testbed.

**Tech Stack:** Helm wrapper charts, Argo CD ApplicationSets, Loki ruler (LogQL), Prometheus rules, Go (testify,
Ginkgo v2).

**Spec:** `docs/superpowers/specs/2026-11-10-runtime-detection-design.md`

## Global Constraints

- **Versions:** the latest stable `falcosecurity/falco` chart and Falco image, pinned exactly. When this was written:
  chart 9.2.2, Falco 0.45.2. Check upstream when building (CLAUDE.md, Rules).
- **Detection only:** no Falco Talon, no falcosidekick, no k8s-metacollector, no Tetragon.
- **No runtime downloads:** `falcoctl.artifact.install.enabled: false` and `falcoctl.artifact.follow.enabled: false`.
  Rules come from the image's stable ruleset plus files vendored in `system/falco/files/`.
- **Driver and output:** `driver.kind: modern_ebpf`, `falco.json_output: true`, `falco.priority: warning`.
- **Alert severities:** `critical`, `alert` and `emergency` events alert with severity `critical`. `error` events alert
  with severity `warning`. `warning` events show only on the dashboard. Alerts are grouped by rule and namespace.
- **The sandbox keeps every event and alert, as production does.** Only the clean-baseline check counts events that
  have a Kubernetes namespace. A noisy workstation process is accepted in `values-dev.yaml`, never in `values.yaml`.
- **Every accepted event has a non-empty `reason`.**
- **Namespace `falco`:** `privileged` under Pod Security Admission and in the mesh, with an `allow` for Prometheus on the
  metrics port. No new egress.
- **The testbed:**
  - no `PolicyException`, `ServiceMonitor` or `PrometheusRule` there (their CRDs are missing);
  - Falco is listed as a layer in `values-testbed.yaml`.
  - If it breaks the testbed's bar, Falco goes to `not_run`, its proof becomes one recorded manual run, and the VM
    sizes stay.
- **Commits** end with the session's `Co-Authored-By` and `Claude-Session` lines.

## Review Focus

1. **Falco's own startup lines are not JSON.** Every LogQL expression drops lines that fail `| json` (`__error__=""`),
   so a restart never fires or breaks an alert. Pinned in Task 2's `TestFalcoLokiRules` (the expressions carry
   `__error__=""`).
2. **An accepted entry must narrow a rule without disabling it.** The template renders a Falco `exceptions:` append on
   the named rule, never `enabled: false`. Pinned in Task 1's `TestFalcoAccepted`.
3. **Events from the workstation have no `k8s.ns.name`.** The alert expressions group by `namespace` and still fire with
   an empty namespace. The baseline query filters `namespace!=""`. Pinned in Task 2's test (the expression has no
   namespace filter).
4. **A node whose Falco pod is missing** is caught by `FalcoNotRunning` (desired versus ready on the DaemonSet), not
   only by a failed scrape. Pinned in Task 2's `TestFalcoPrometheusRules`.
5. **The testbed's render has none of the kinds whose CRDs it lacks.** Pinned in Task 1's `TestFalcoRender` (testbed
   case).

---

### Task 1: `system/falco`, the chart

**Files:**
- Create:
  - `system/falco/Chart.yaml`: dependency `falco` from `https://falcosecurity.github.io/charts`, pinned;
  - `system/falco/values.yaml`;
  - `system/falco/values-local.yaml`;
  - `system/falco/values-testbed.yaml`;
  - `system/falco/templates/accepted-rules.yaml`;
  - `system/falco/templates/policy-exception.yaml`;
  - `system/falco/templates/authorization-policy.yaml`.
- Create: `system/falco/files/` holding the vendored incubating rules for 15g-2's list (reverse shell, crypto miner,
  container escape) where the image's stable ruleset lacks them. Each file has a header with the falcosecurity/rules
  version it came from.
- Modify:
  - `system/argocd/values.yaml`: add `falco` to the `templatePatch` list next to `kured` and `loki` (privileged, in
    ambient);
  - `test/e2e/policy_test.go:18`: add `falco` to `privilegedNamespaces`;
  - `system/argocd/values-dev.yaml`: add `system/falco` to the root directories after `system/trivy-operator`;
  - `system/argocd/values-testbed.yaml`: add `system/falco` under a comment `# Layer: runtime detection`.
- Test: `test/offline/falco_test.go`

**Interfaces:**
- **Produces:**
  - `renderFalco(t *testing.T, env, expr string) string`: renders `system/falco` for `""`, `dev` or `testbed` with
    `scripts/render-chart` and runs a yq expression. Same shape as `renderTrivyOperator`.
  - The values key `accepted`: a list of `{rule, name, fields, comps, values, reason}`.
  - The ConfigMap `falco-accepted-rules`, mounted into Falco's `rules.d`.
  - The Service and ServiceMonitor named by the chart, scraped on Falco's metrics port.

- [ ] **Step 1: Write the failing test `TestFalcoRender`.** It loops over envs `""`, `dev` and `testbed` and asserts, in
  the rendered `falco` ConfigMap or the DaemonSet args:
  - `engine.kind` is `modern_ebpf`;
  - `json_output` is `true`;
  - `priority` is `warning`;
  - `rules_files` contains `/etc/falco/falco_rules.yaml` and `/etc/falco/rules.d`;
  - the containerd socket list includes `/run/k3s/containerd/containerd.sock`;
  - no container is named `falcoctl-artifact-install` or `falcoctl-artifact-follow`;
  - no resource has kind `Deployment` (no sidekick or metacollector);
  - the Falco image tag equals the pinned app version;
  - the DaemonSet's pod template has a ConfigMap volume `falco-accepted-rules`;
  - that ConfigMap holds every file in `system/falco/files/` (the vendored rules), as well as the accepted entries;
  - for `""` and `dev`: one `PolicyException`, with a non-empty `homelab/reason` annotation and the
    `SkipDryRunOnMissingResource=true` sync option, plus one `ServiceMonitor`;
  - for `testbed`: no `PolicyException`, `ServiceMonitor` or `PrometheusRule`.
- [ ] **Step 2: Run it.** `go test -C test ./offline/ -run TestFalcoRender -count=1` → FAIL (`system/falco` does not exist).
- [ ] **Step 3: Write the chart.**
  - **values.yaml:**
    - `falco.driver.kind: modern_ebpf`;
    - `falco.falcoctl.artifact.install.enabled: false` and `follow.enabled: false`;
    - `falco.falco.json_output: true`, `priority: warning`;
    - Falco's metrics on (`metrics.enabled`, Prometheus endpoint);
    - `falco.serviceMonitor.create: true`;
    - the accepted-rules volume through `falco.mounts`;
    - `accepted: []` and `policyExceptions: true`.
  - **accepted-rules.yaml:** holds `files/*.yaml` as they are (`.Files.Glob`), then the accepted entries. Falco
    loads `rules.d` in lexicographic order, so the accepted entries' key sorts last (`zz-accepted.yaml`).
  - **values-local.yaml:** requests `cpu: 100m`, `memory: 128Mi`; limits `cpu: "1"`, `memory: 512Mi`. These are
    starting values: Task 4 measures and replaces them.
  - **values-testbed.yaml:** `policyExceptions: false`, `falco.serviceMonitor.create: false`, `prometheusRules: false`.
  - **policy-exception.yaml:** one exception for the DaemonSet's pods, listing the policies they break (privileged,
    host PID, host paths). Same form as `system/loki/templates/policy-exception.yaml`.
  - **authorization-policy.yaml:** `allow` with the same namespace, plus Prometheus on the metrics port. Same form as
    `system/trivy-operator/templates/authorization-policy.yaml`.
  - **Kyverno:** add `docker.io/falcosecurity` only if `allowed-registries` refuses it (it allows `docker.io`, so no
    change is expected).
- [ ] **Step 4: Run it.** `go test -C test ./offline/ -run TestFalcoRender -count=1` → PASS.
- [ ] **Step 5: Write the failing test `TestFalcoAccepted`.**
  - Every entry of `accepted` in `values.yaml`, `values-local.yaml` and `values-dev.yaml` has a non-empty `rule`,
    `name` and `reason`.
  - For a fixture values file with one entry, the rendered `falco-accepted-rules` holds:
    `- rule: <rule>` / `exceptions: [{name, fields, comps, values}]` / `override: {exceptions: append}`.
  - The rendered file holds no `enabled: false`.
- [ ] **Step 6: Run it.** `go test -C test ./offline/ -run TestFalcoAccepted -count=1` → FAIL.
- [ ] **Step 7: Write `templates/accepted-rules.yaml`.** One rules document per entry, appending the exception. The
  `reason` goes in as a YAML comment above the entry.
- [ ] **Step 8: Run the checks.**
  - `go test -C test ./offline/ -run 'TestFalco|TestPolicy' -count=1` → PASS.
  - `scripts/ci-check policy`, `scripts/check-mesh-policies` and `scripts/check-backup-groups` → no failures.
- [ ] **Step 9: Commit.** `feat(falco): system/falco detects at runtime, detection only, no runtime downloads`.

### Task 2: Alerts and the dashboard

**Files:**
- Create:
  - `system/loki/files/falco-rules.yaml`;
  - `system/falco/templates/prometheus-rule.yaml`, gated by `.Values.prometheusRules`, default `true`.
- Modify:
  - `system/loki/templates/audit-rules.yaml`: add the key `falco-rules.yaml` from `files/falco-rules.yaml`;
  - `system/monitoring-system/files/security-dashboard.json`: add the panel "Falco events by priority, rule and
    namespace" (Loki, from `warning` up).
- Test: `test/offline/falco_test.go`

**Interfaces:**
- **Consumes:** `renderFalco` (Task 1). `render.Loki` and `lokitool` as in `TestSecurityLogsRules`.
- **Produces, Loki alerts** (`source: falco`):
  - `FalcoCriticalEvent`, severity `critical`;
  - `FalcoErrorEvent`, severity `warning`.
- **Produces, Prometheus alerts:**
  - `FalcoNotRunning`, severity `critical`;
  - `FalcoStalled`, severity `critical`;
  - `FalcoEventsDropped`, severity `warning`.

- [ ] **Step 1: Write the failing test `TestFalcoLokiRules`.**
  - The rendered `loki-audit-rules` key `falco-rules.yaml` passes `lokitool rules lint` and `lokitool rules check`.
  - It has exactly the two Loki alerts above, with their severities and `source: falco`.
  - Each expression selects `{job="pods", namespace="falco"}`, runs `| json`, has `__error__=""` and groups
    `by (rule, namespace)`.
  - `FalcoCriticalEvent` matches `priority=~"Critical|Alert|Emergency"`; `FalcoErrorEvent` matches `priority="Error"`.
  - Neither expression filters on namespace (Review Focus 3).
  - Each has a `summary` and a `description` naming `docs/how-to-guides/runtime-detection.md`.
- [ ] **Step 2: Run it.** `go test -C test ./offline/ -run TestFalcoLokiRules -count=1` → FAIL.
- [ ] **Step 3: Write the Loki rules.** Map `output_fields["k8s.ns.name"]` to the label `namespace` with `| json`
  extraction. Window `[5m]`.
- [ ] **Step 4: Write the failing test `TestFalcoPrometheusRules`.** In the `PrometheusRule` rendered for env `""`:
  - `FalcoNotRunning` compares `kube_daemonset_status_number_ready` with `kube_daemonset_status_desired_number_scheduled`
    for `daemonset="falco"`, with `for: 10m`;
  - `FalcoStalled` fires on `up == 0` for the Falco job, or when its syscall-event counter's `increase` is `0` over 10
    minutes;
  - `FalcoEventsDropped` fires on an increase of Falco's drop counter.

  Take the counters' names from Falco's `/metrics`, and note them in a comment. Candidates in 0.45:
  `falcosecurity_scap_n_evts_total`, `falcosecurity_scap_n_drops_total`.
- [ ] **Step 5: Run it.** `go test -C test ./offline/ -run TestFalcoPrometheusRules -count=1` → FAIL.
- [ ] **Step 6: Write `prometheus-rule.yaml`.** Same form as `system/trivy-operator/templates/prometheus-rule.yaml`.
- [ ] **Step 7: Add the dashboard panel.** A table or time series from
  `sum by (priority, rule, namespace) (count_over_time({job="pods", namespace="falco"} | json | __error__="" [$__interval]))`.
- [ ] **Step 8: Run it.**
  - `go test -C test ./offline/ -run 'TestFalco|TestSecurityLogs|TestMonitoring' -count=1` → PASS.
  - `scripts/render-chart system/monitoring-system` succeeds.
- [ ] **Step 9: Commit.** `feat(falco): events alert through Loki, a stalled Falco through Prometheus, the Security dashboard shows them`.

### Task 3: The specs and their selection

**Files:**
- Create: `test/e2e/falco_test.go`
- Modify:
  - `test/scopes.yaml`:
    - add label `falco` to `labels:`;
    - add `falco` to `full`;
    - add the group `paths: [system/falco/**, system/loki/files/falco-rules.yaml, test/e2e/falco_test.go]`,
      `tests: falco`;
  - `cli/internal/scope/repository_test.go:24`: add `falco` to the expected `full` filter;
  - `infra/testbed/release-paths.yaml`: under `not_run`, add `system/falco/templates/policy-exception.yaml` and
    `system/falco/templates/prometheus-rule.yaml`.

**Interfaces:**
- **Consumes:** `lokiQuery` (`test/e2e/logs_test.go`), `kube.Kubectl`, `kube.CreateNamespace`, `kube.DeleteNamespace`.
- **Consumes:** the alert names from Task 2. Read the deployed expressions from the ConfigMap `loki-audit-rules`.

- [ ] **Step 1: Write `Describe("Falco", Label("sandbox", "falco"))`**, It "reports a read of /etc/shadow and a dropped
  binary", `SpecTimeout(15*time.Minute)`.
  - Create namespace `falco-test-<unix>`, with a `DeferCleanup` deleting it.
  - `kubectl run probe --image=docker.io/library/busybox:1.37.0 --restart=Never -- sleep 3600`, then wait for Ready.
  - `kubectl exec probe -- cat /etc/shadow`.
  - `kubectl exec probe -- sh -c 'cp /bin/busybox /tmp/dropped && /tmp/dropped true'`.
  - `Eventually` (10 minutes): `lokiQuery` of
    `{job="pods", namespace="falco"} | json | output_fields_k8s_ns_name="<ns>"` returns lines whose `rule` includes
    one sensitive-file read (priority `Warning`) and `Drop and execute new binary in container` (priority
    `Critical`).
  - Run `FalcoCriticalEvent`'s deployed expression as a Loki instant query (`/loki/api/v1/query` through the same
    service proxy). Its result has a series with `namespace="<ns>"`.
- [ ] **Step 2: Write `Describe("TestbedFalco", Label("testbed", "falco"))`**, It "watches every node",
  `SpecTimeout(15*time.Minute)`:
  - the DaemonSet `falco` in `falco` has `numberReady == desiredNumberScheduled == 3`;
  - every Falco pod's log has the line that the modern eBPF probe is loaded;
  - the busybox drop-and-execute above, in a test namespace, appears in some Falco pod's log within 5 minutes, as JSON
    with that rule.
- [ ] **Step 3: Run the selection tests and see them fail.**
  `go test -C test ./offline/ -run 'TestE2E' -count=1` and `go test -C cli ./internal/scope/ -count=1`. They must fail
  on the new label or the missing `full` entry before the `test/scopes.yaml` edit.
- [ ] **Step 4: Edit `test/scopes.yaml`, `repository_test.go` and `release-paths.yaml`.**
- [ ] **Step 5: Run them again.**
  - The two test commands → PASS.
  - `just cli check` → PASS.
  - `go vet -C test ./e2e/` → clean.
- [ ] **Step 6: Commit.** `test(falco): Falco and TestbedFalco prove events, alerts and the driver on every node`.

### Task 4: The guide, the roadmap, and the proof on the clusters

**Files:**
- Create: `docs/how-to-guides/runtime-detection.md`. It covers:
  - what Falco watches;
  - reading an event in Grafana (Explore, Loki: `{namespace="falco"} | json`);
  - each alert and what to do about it;
  - accepting an event in `accepted` with its reason, and where (`values.yaml` versus `values-dev.yaml`);
  - the measured resources;
  - 15g-2.
- Modify:
  - `mkdocs.yml`: add the guide after `how-to-guides/security-logs.md`;
  - `CLAUDE.md`, roadmap: mark 15g done and name what it proved;
  - `CLAUDE.md`, Sandbox: one line on Falco;
  - `CLAUDE.md`, "Before the first production deploy": review Falco's events in the first days (accept with a reason or
    fix); the driver ran only on WSL's kernel and the testbed's VMs; record the testbed fallback here if it was taken;
  - `system/falco/values-local.yaml`: the measured limits;
  - `system/falco/values*.yaml`: `accepted` entries the baseline needs.

- [ ] **Step 1: Write the guide and the CLAUDE.md lines.** Check them with `mkdocs build --strict`.
- [ ] **Step 2: Run `scripts/ci-check all`** in the dev shell → rc=0.
- [ ] **Step 3: Commit, then run `just dev`** from `/home/nixos/homelab`. Expected: the `falco` group's specs pass, and
  so does `Falco`.
- [ ] **Step 4: Run `just dev-fresh`**, with the testbed stopped. Expected: the whole suite passes.
  - During the run, record Falco's peak memory and CPU:
    `max_over_time(container_memory_working_set_bytes{namespace="falco",container="falco"}[3h])` and the CPU rate.
  - Set `values-local.yaml`'s limits to about twice the peak, and requests to about the median.
  - If Falco's CPU peak is above half a core, set `falco.base_syscalls` to the rules' set (`base_syscalls.repair:
    true`) and measure again.
- [ ] **Step 5: Check the clean baseline.** Over the run's window, the Loki query
  `sum by (rule, namespace) (count_over_time({job="pods", namespace="falco"} | json | __error__="" | priority=~"Error|Critical|Alert|Emergency" | output_fields_k8s_ns_name!="" [3h]))`
  holds nothing but the `falco-test-*` namespaces.
  - Accept each other entry in `accepted` with its reason, or fix its cause.
  - Re-run the affected groups until the query is clean.
- [ ] **Step 6: Run the testbed.** Stop the sandbox (`k3d cluster stop homelab-dev`), then run `just testbed` and
  `just test e2e --env testbed --labels falco`.
  - Expected: `TestbedFalco` passes and the testbed's bar holds.
  - If the bar breaks: Falco goes to `not_run`, `TestbedFalco` runs alone on a fresh testbed and is recorded, and
    CLAUDE.md says so.
  - Finish with `just testbed-stop`.
- [ ] **Step 7: Commit the measured values, the accepted entries and the docs.**
  `docs(falco): the guide, the measured limits, the roadmap`. Record the runs for the PR.
