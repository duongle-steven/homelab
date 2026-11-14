# OpenTelemetry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The OpenTelemetry Operator's Collectors replace Alloy for every log stream into Loki (OTLP, OTel labels), and traces from the Istio Gateways and the platform tools reach Tempo, linked to their logs in Grafana.

**Architecture:** `system/opentelemetry` holds the Operator and two `OpenTelemetryCollector`s: the agent (DaemonSet: files on the node → Loki directly, persistent queue) and the gateway (Deployment: OTLP traces → Tempo, Kubernetes events → Loki). `system/tempo` stores traces; `system/loki` loses Alloy and gains OTLP ingestion. Istio's Telemetry API makes the Gateways send spans and JSON access logs carrying `trace_id`.

**Tech Stack:** opentelemetry-operator chart 0.129.0 (Operator 0.162.0), `otel/opentelemetry-collector-k8s` 0.165.0, grafana-community `tempo` chart 3.2.1 (Tempo 3.1.1), Loki chart 18.17.0, Istio 1.32 Telemetry API, Go tests (testify offline, Ginkgo e2e).

**Spec:** `docs/superpowers/specs/2026-11-14-opentelemetry-design.md`

## Global Constraints

- *(user)* No parallel run: Alloy is removed in this branch; the sandbox shows what breaks.
- *(user)* Loki labels follow OpenTelemetry's conventions; nothing keeps `namespace`, `pod`, `container`, `app`, `job`. The mapping every rewrite uses:

  | Before (Alloy) | After (OTLP into Loki) |
  | --- | --- |
  | `namespace` | `k8s_namespace_name` |
  | `pod` | `k8s_pod_name` |
  | `container` | `k8s_container_name` |
  | `app` | `service_name` |
  | `node` | `k8s_node_name` (structured metadata unless a query needs it indexed) |
  | `job="pods"` | dropped: any pod stream is selected by its `k8s_*` labels |
  | `job="audit/k3s"` | `service_name="k3s-audit"` |
  | `job="audit/openbao"` | `service_name="openbao-audit"` |
  | Kubernetes events' `job` | `service_name="kubernetes-events"` |

- Versions pinned exactly as in Tech Stack; if the Operator refuses Collector 0.165.0, pin the newest pair that works and say why next to the pin.
- Every PVC carries `homelab/backup: none` (Tempo, like Loki).
- OTLP is never routed: no `HTTPRoute` to a Collector or Tempo; the gateway Collector's `AuthorizationPolicy` names its senders.
- The Operator's pod webhook: `failurePolicy: Ignore`; its pods carry `istio.io/dataplane-mode: none`.
- Audit streams keep 60 days unless Task 10's measurement says otherwise, then `CLAUDE.md`'s rule (45, then 30 days; never a bigger volume).
- The sandbox's new components get small requests in `values-dev.yaml`; the node stays under 85% of its memory during the suite (79% today).

## Review Focus

1. A Collector restart while Loki is down: lines read meanwhile arrive later (`LogsSurviveLokiOutage`, Task 8; the agent's queue in `file_storage`, Task 2).
2. The Operator down or not yet synced: pods anywhere in the cluster are still created (offline assertion on the webhook's `failurePolicy`, Task 1).
3. OTLP sent by a namespace with no reason to send: refused (`OTLPRefusedFromOthers`, Task 8).
4. The API server rotating `audit.log`: the agent keeps following it by name, no line read twice (offline assertion on the receiver's `include` and `start_at`, Task 2).
5. OpenBao lines that are not audit JSON (its startup, its errors): stay ordinary pod logs, not `openbao-audit` (offline assertion on the routing condition, Task 2; `SecurityLogsOpenBao`, Task 8).

---

### Task 1: The Operator (`system/opentelemetry`)

**Files:**
- Create: `system/opentelemetry/Chart.yaml`, `values.yaml`, `values-dev.yaml`, `templates/authorization-policy.yaml`, `templates/allow-hbone.yaml` (if the chart renders NetworkPolicies)
- Modify: `system/argocd/values.yaml` and `values-dev.yaml` (the `root` list, as `system/falco` is listed), `test/scopes.yaml`
- Test: `test/offline/opentelemetry_test.go`

**Interfaces:**
- Produces: namespace `opentelemetry` (the Application's), the CRDs `opentelemetrycollectors.opentelemetry.io` and `instrumentations.opentelemetry.io`, the Operator's default Collector image `otel/opentelemetry-collector-k8s:0.165.0`.

- [ ] **Step 1: Write the failing test** `TestOpenTelemetryOperatorRender` (both envs, `renderVelero(t, "system/opentelemetry", env, …)`): the manager's image tag is `0.162.0`; its `--collector-image` (or the chart's `manager.collectorImage`) is `otel/opentelemetry-collector-k8s:0.165.0`; every `MutatingWebhookConfiguration` webhook has `failurePolicy: Ignore`; the manager pod has label `istio.io/dataplane-mode: none` and seccomp `RuntimeDefault`; the CRDs above render.
- [ ] **Step 2: Run** `cd test && direnv exec .. go test ./offline -run TestOpenTelemetryOperatorRender`; Expected: FAIL (no chart).
- [ ] **Step 3: Implement** the wrapper chart on `opentelemetry-operator` 0.129.0 from `https://open-telemetry.github.io/opentelemetry-helm-charts`, cert-manager for its webhook certificate (the cluster has it), the `allow` policy (the API server reaches it outside the mesh; Prometheus scrapes its metrics), a `test/scopes.yaml` entry `paths: [system/opentelemetry/**]` → `tests: logs || traces` (add label `traces` to `labels:`).
- [ ] **Step 4: Run** the test and `scripts/ci-check render`, `scripts/ci-check mesh`, `scripts/ci-check policy`; Expected: PASS.
- [ ] **Step 5: Commit** `feat(telemetry): the OpenTelemetry Operator`.

### Task 2: The agent Collector, and Loki without Alloy

**Files:**
- Create: `system/opentelemetry/templates/agent.yaml` (the `OpenTelemetryCollector`, mode `daemonset`, its ServiceAccount's ClusterRole for `k8sattributes`), `templates/policy-exception.yaml` (Alloy's reasons, moved)
- Modify: `system/loki/Chart.yaml` and `values.yaml` (drop the `alloy` dependency and values; `loki.limits_config.otlp_config`; `retention_stream` selector `{service_name=~"k3s-audit|openbao-audit"}`), `system/loki/templates/policy-exception.yaml` (Alloy's removed), `system/loki/templates/authorization-policy.yaml` (the agent's and the gateway's namespace may push), `system/argocd/values.yaml` (`privileged` moves from `loki` to `opentelemetry`), `.cluster-scan-accepted.yaml` (Alloy's entries become the agent's)
- Test: `test/offline/opentelemetry_test.go`, `test/offline/security_logs_test.go` (`TestSecurityLogsRetention`)

**Interfaces:**
- Consumes: Task 1's CRD and namespace.
- Produces: the agent's resource attributes per the mapping table; Loki's OTLP endpoint `http://loki.loki:3100/otlp`.

- [ ] **Step 1: Write the failing tests** `TestCollectorAgentRender`: mode `daemonset`; receivers `filelog/pods` (include `/var/log/pods/*/*/*.log`, operator `container`) and `filelog/audit` (include `/var/log/kubernetes/audit/audit.log`), both with `start_at: beginning` and `storage: file_storage` (positions persist, so a restart or the API server's rotation neither rereads nor skips a line: Review Focus 4); extension `file_storage` on a hostPath `/var/lib/otelcol`; processors include `k8sattributes` and a `transform` that sets `service.name` `k3s-audit` on the audit file and `openbao-audit` only when `k8s.namespace.name == "openbao"` and the body matches `^\{.*"type":"(request|response)"` (Review Focus 5); the `otlphttp` exporter's endpoint is `http://loki.loki:3100/otlp` with `sending_queue.storage: file_storage` (Review Focus 1); the pod mounts `/var/log/pods` and `/var/log/kubernetes/audit` read-only. `TestLokiWithoutAlloy`: no rendered object's name contains `alloy`, `otlp_config` is set. Update `TestSecurityLogsRetention` to expect the `service_name` selector at `1440h`.
- [ ] **Step 2: Run** them; Expected: FAIL.
- [ ] **Step 3: Implement.** The attributes the agent drops (Spec decision 12) are left to Task 10's measurement: start by deleting `log.file.path`, `log.iostream` stays.
- [ ] **Step 4: Run** the tests and `scripts/ci-check render policy mesh backup-groups`; Expected: PASS.
- [ ] **Step 5: Commit** `feat(telemetry): the agent Collector replaces Alloy`.

### Task 3: The gateway Collector

**Files:**
- Create: `system/opentelemetry/templates/gateway.yaml` (mode `deployment`, 1 replica; its ClusterRole: `events` list/watch), add its rules to `templates/authorization-policy.yaml`
- Test: `test/offline/opentelemetry_test.go`

**Interfaces:**
- Produces: Service `gateway-collector.opentelemetry` (the Operator's naming for a Collector named `gateway`), ports 4317 (gRPC) and 4318 (HTTP); `service.name` `kubernetes-events` on events.

- [ ] **Step 1: Write the failing test** `TestCollectorGatewayRender`: mode `deployment`, replicas 1; receivers `otlp` (grpc, http) and `k8sobjects` (`events`, mode `watch`); pipelines: traces `otlp` → `otlp/tempo` (`tempo.tempo:4317`), logs `k8sobjects` → `otlphttp/loki`; no `HTTPRoute` in the chart; the `AuthorizationPolicy` for the gateway's pods admits only principals of `istio-system`'s Gateways and the tools' namespaces (`argocd`, `grafana`) on 4317/4318 (Review Focus 3).
- [ ] **Step 2: Run**; Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the test, `scripts/ci-check render mesh`; Expected: PASS.
- [ ] **Step 5: Commit** `feat(telemetry): the gateway Collector, traces and events`.

### Task 4: Tempo and Grafana's data sources

**Files:**
- Create: `system/tempo/Chart.yaml`, `values.yaml`, `values-dev.yaml`, `templates/authorization-policy.yaml`
- Modify: `system/argocd/values.yaml` and `values-dev.yaml` (`root` list), `system/monitoring-system/values.yaml` (`additionalDataSources`: Tempo; Loki's `derivedFields` on `trace_id`), `test/scopes.yaml` (`system/tempo/**` → `traces`)
- Test: `test/offline/tempo_test.go`

**Interfaces:**
- Produces: Service `tempo.tempo` (OTLP gRPC 4317, HTTP API 3200), Grafana data source uid `tempo`.

- [ ] **Step 1: Write the failing test** `TestTempoRender`: single binary; retention `168h` (production) and `24h` (dev); PVC label `homelab/backup: none`; OTLP gRPC receiver on; `allow` admits only the gateway Collector (4317) and Grafana (3200). `TestGrafanaTraceDataSources`: data source `tempo` with `tracesToLogsV2` on uid `loki`; Loki's data source has a derived field `trace_id` to uid `tempo`.
- [ ] **Step 2: Run**; Expected: FAIL.
- [ ] **Step 3: Implement** on grafana-community's `tempo` 3.2.1.
- [ ] **Step 4: Run** the tests, `scripts/ci-check render backup-groups mesh policy`; Expected: PASS.
- [ ] **Step 5: Commit** `feat(telemetry): Tempo, linked to Loki in Grafana`.

### Task 5: The Gateways' spans and access logs

**Files:**
- Modify: `system/istio-system/values.yaml` (istiod `meshConfig.extensionProviders`: `otel-tracing` → `gateway-collector.opentelemetry.svc.cluster.local:4317`; an `envoyFileAccessLog` provider `otel-json` with JSON labels including `trace_id`), `values-dev.yaml` (sampling)
- Create: `system/istio-system/templates/telemetry.yaml` (`Telemetry` in `istio-system` selecting the Gateways' pods: tracing provider `otel-tracing` with `randomSamplingPercentage` 100 in dev and 10 in production; access logging `otel-json`)
- Test: `test/offline/istio_telemetry_test.go`

- [ ] **Step 1: Write the failing test** `TestIstioTracingRender` (both envs): the two providers exist with the values above; the `Telemetry` selects the Gateways (`gateway.networking.k8s.io/gateway-name` exists) and samples 100 (dev) / 10 (production); the access log format has key `trace_id`.
- [ ] **Step 2: Run**; Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the test and `scripts/ci-check render`; Expected: PASS.
- [ ] **Step 5: Commit** `feat(telemetry): the Gateways' traces and access logs`.

### Task 6: Tools that export OTLP

**Files:**
- Modify: `system/argocd/values.yaml` (`configs.params` OTLP address `gateway-collector.opentelemetry:4317`, insecure inside the mesh), `platform/grafana/values.yaml` (`grafana.ini` `tracing.opentelemetry.otlp`), others the builder finds (Loki, Tempo, Kanidm): each with one assertion
- Test: `test/offline/opentelemetry_test.go` (`TestToolsExportTraces`)

- [ ] **Step 1: Write the failing test** `TestToolsExportTraces`: each tool's rendered setting points at `gateway-collector.opentelemetry` port 4317 (gRPC) or 4318 (HTTP).
- [ ] **Step 2: Run**; Expected: FAIL.
- [ ] **Step 3: Implement**; a tool with no OTLP setting is listed in the traces guide (Task 9), not forced.
- [ ] **Step 4: Run**; Expected: PASS.
- [ ] **Step 5: Commit** `feat(telemetry): the platform tools send their traces`.

### Task 7: Rules and dashboard on the new labels

**Files:**
- Modify: `system/loki/files/audit-rules.yaml`, `falco-rules.yaml`, `public-scan-rules.yaml`, `system/monitoring-system/files/security-dashboard.json`
- Test: `test/offline/security_logs_test.go` (`TestSecurityLogsRules`), `test/offline/falco_test.go` (`TestFalcoLokiRules`), `test/offline/public_scan_test.go`, a new `TestNoOldLogLabels`

- [ ] **Step 1: Write the failing test** `TestNoOldLogLabels`: no stream selector in those four files names `job=`, `namespace=` as a stream label, `pod=`, `container=` or `app=` (the `label_format namespace=` in Falco's rules is an output label and stays); update the three existing tests' expected selectors per the mapping table.
- [ ] **Step 2: Run**; Expected: FAIL.
- [ ] **Step 3: Implement**: only stream selectors change; every `| json …` stage stays (the body is the original line).
- [ ] **Step 4: Run** the four tests and `scripts/ci-check render`; Expected: PASS.
- [ ] **Step 5: Commit** `feat(telemetry): the audit, Falco and scan alerts on OpenTelemetry's labels`.

### Task 8: The e2e specs

**Files:**
- Modify: `test/e2e/logs_test.go` (every spec; `LogsTargetsUp` checks the agents' and the gateway's `otelcol_receiver_accepted_log_records`/exporter metrics instead of Alloy's targets; `LogsResumeAfterRestart` restarts the agent), `test/e2e/security_logs_test.go`, `test/e2e/falco_test.go`, `test/e2e/monitoring_test.go` (its log queries), `test/scopes.yaml`
- Create: `test/e2e/traces_test.go`

- [ ] **Step 1: Write the specs.** Rewritten queries per the mapping table. New, labels `sandbox`, `traces`:
  - `TracesFromGateway`: a request to `https://grafana.127-0-0-1.nip.io/` with a fresh `traceparent`; within 2 minutes Tempo's `/api/traces/<id>` returns spans with `service.name` of the Gateway, and Loki has an access log line with `trace_id="<id>"`.
  - `OTLPRefusedFromOthers`: a pod in a namespace not in the gateway Collector's `allow` (e.g. `logs-test`) sends OTLP to 4317/4318 and is refused (connection reset or 403); the same request from an allowed identity is not needed.
  - `LogsSurviveLokiOutage` keeps its shape, restarting the agent instead of Alloy.
- [ ] **Step 2: Compile** `cd test && direnv exec .. go vet ./e2e`; Expected: no errors. (They run in Task 10.)
- [ ] **Step 3: Commit** `test(telemetry): the log specs on OpenTelemetry's labels, and the traces specs`.

### Task 9: Docs, comments, leftovers

**Files:**
- Modify: `docs/how-to-guides/logs.md`, `security-logs.md`, `runtime-detection.md` (queries per the mapping table), `CLAUDE.md` (the Loki/Alloy lines in Sandbox and the production list; step 9's and the roadmap's lines name the Collector), comments in `platform/openbao/values.yaml`, `infra/_modules/k3d/k3d.yaml`, `system/falco/values.yaml`, `docs/reference/architecture/overview.md`, `docs/superpowers/specs/2026-11-14-threat-model-design.md` (E2's OTLP entry), `mkdocs.yml`
- Create: `docs/how-to-guides/traces.md` (find a trace from a log line, from Explore, what sends spans, sampling)
- Test: `test/offline/opentelemetry_test.go` (`TestNoAlloy`)

- [ ] **Step 1: Write the failing test** `TestNoAlloy`: `git grep -il alloy` outside `docs/superpowers/` (history) and `CLAUDE.md`'s step-9 history phrase returns nothing.
- [ ] **Step 2: Run**; Expected: FAIL (the comments above).
- [ ] **Step 3: Implement** the docs and comments.
- [ ] **Step 4: Run** `TestNoAlloy` and `scripts/ci-check all`; Expected: PASS.
- [ ] **Step 5: Commit** `docs(telemetry): logs and traces through the OpenTelemetry Collector`.

### Task 10: The sandbox

- [ ] **Step 1:** `just dev --tests 'logs || security-logs || falco || traces || monitoring || public'` from the main checkout. Expected: every spec passes; fix what breaks (spec decision 2), each fix with its own failing test where an offline one can show it.
- [ ] **Step 2: Measure** the node's memory during the run (`kubectl top node`, under 85%) and set the new components' requests and limits in `values-dev.yaml` from `kubectl top pod`.
- [ ] **Step 3: Measure** the bytes per audit line in Loki (`logcli` or Loki's `/loki/api/v1/index/volume` for `service_name="k3s-audit"` over the run, against the lines counted) and compare with step 15c's budget (7.3GiB of 10Gi for 60 days); drop the attributes no query uses in the agent until it fits, otherwise shorten the period per `CLAUDE.md`. Record the numbers in the spec.
- [ ] **Step 4:** If `TracesFromGateway` shows the Gateways cannot reach the gateway Collector through the mesh, apply spec decision 13's fallback (a port exception admitting only the Gateways' pods) and add it to the threat model.
- [ ] **Step 5:** `just dev` (the branch's whole scope) as the last run before the PR, then the final whole-branch review.
- [ ] **Step 6: Commit** each fix; the PR into `dev` labelled `sandbox` (a fresh install of new operators and CRDs).
