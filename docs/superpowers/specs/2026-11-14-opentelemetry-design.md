# OpenTelemetry: the Collector replaces Alloy, traces into Tempo

Status: designed in chat 2026-11-14; the builder check's four questions are settled below (decisions 10 to 13). Decisions marked *(user)* were made in chat; *(Claude's call)* marks the
builder's own, open to review. Builds on step 9 (Loki and Alloy), 15c (security logs), 15g (Falco) and the threat model
(`docs/superpowers/specs/2026-11-14-threat-model-design.md`).

## Intent

The homelab moves its telemetry pipeline onto the vendor-neutral OpenTelemetry Collector, the standard the community converges on and the one
the user is learning (OTCA), and starts collecting traces. Grafana Alloy (itself a distribution of the Collector) goes: one agent per node, not
two. Loki 3 ingests OTLP natively and Tempo stores the traces, so nothing in between is Grafana-specific.

*(user)* This is learning by doing: traces are collected the way the community usually does it, not to answer a question the homelab already
has. The sandbox is short of resources, so the Collector replaces Alloy at once, with no period of running both: what breaks is fixed.

## Goals

1. The OpenTelemetry Operator runs the Collector; Alloy is removed from `system/loki`.
2. Every log Alloy collects today reaches Loki through the Collector over OTLP: pod logs, OpenBao's audit lines, the API server's audit log,
   Kubernetes events. Falco's events keep arriving as Falco's pod logs.
3. Traces reach Tempo from the Istio Gateways (Envoy, through Istio's Telemetry API) and from the platform tools that export OTLP themselves,
   and Grafana links a log line to its trace and back.
4. Every alert, dashboard panel, retention rule and test that reads logs works on the new labels.

## Non-goals

- **Metrics through the Collector:** Prometheus and its scrapes stay as they are; the Collectors' own metrics are scraped like any component's.
- **Auto-instrumentation:** the Operator can inject SDKs into pods (`Instrumentation`), but no app needs it yet; Backstage, the next step, is the
  first.
- **Tempo at scale:** no object storage, no replication, no metrics-generator (service graphs, span metrics); worth it with apps of our own.
- **Traces inside the mesh:** ambient's ztunnel makes no spans (L4 only); spans between services need waypoints, which stay an idea.

## Anti-goals

- An OTLP endpoint reachable from outside the cluster, or by namespaces that have no reason to send (the threat model's rule: nothing new is
  published).
- Audit lines lost: a line read while Loki is unreachable still arrives after a restart of the Collector, as with Alloy's write-ahead log
  (`LogsSurviveLokiOutage`). Their 60 days hold unless measured otherwise, and then `CLAUDE.md`'s rule applies (shorten to 45, then 30
  days; never grow the volume).
- A Collector configuration only one person can read: every pipeline commented as the Alloy configuration was.

## Decisions

1. *(user)* **Approach A: the Operator manages two Collectors, the agent and the gateway.**
   - **The agent:** an `OpenTelemetryCollector` in mode `daemonset`. It reads `/var/log/pods` and `/var/log/kubernetes/audit/audit.log` on its
     node (`filelog`, with the `container` operator for containerd's format), keeps its read positions on the node (`file_storage` on a
     hostPath, so a restart resumes where it stopped, as Alloy's positions did), adds the pod's metadata (`k8sattributes`), and exports the
     logs to Loki's OTLP endpoint itself (decision 10).
   - **The gateway:** an `OpenTelemetryCollector` in mode `deployment`, one replica. It receives OTLP traces (from the Gateways' Envoy and the
     tools) and exports them to Tempo, and watches Kubernetes events (`k8sobjects`, which a single replica watches once) and exports them to
     Loki.
   - Rejected: the `opentelemetry-collector` chart without the Operator (two ways to configure a Collector once Backstage needs the Operator
     anyway, and it skips what OTCA covers); one DaemonSet doing everything (events need leader election across the DaemonSet, apps would send to
     their node's address, and no single place samples or processes traces).
2. *(user)* **No parallel run.** The Collector replaces Alloy in one change; the sandbox shows what breaks.
3. *(user)* **OpenTelemetry's semantic conventions for labels.** Loki indexes the resource attributes it indexes by default (`service_name`,
   `k8s_namespace_name`, `k8s_pod_name`, `k8s_container_name`, the workload's name, …) and keeps the rest as structured metadata. Queries,
   alerts, dashboards and tests are rewritten to them; nothing keeps the old names `namespace`, `pod`, `container`, `app`, `job`.
4. *(user)* **Traces as usually done:** the Istio Gateways through Istio's Telemetry API with an `opentelemetry` extension provider pointing at
   the gateway Collector, and the platform tools that export OTLP (Argo CD, Grafana; Loki, Tempo, Kanidm and others where their charts or
   configuration allow it, each found by the builder).
5. *(Claude's call)* **The audit streams are named by `service.name`:** `k3s-audit` for the API server's file and `openbao-audit` for
   OpenBao's audit lines, which the agent tells from OpenBao's other output as Alloy did (JSON with `"type":"request"` or `"response"`).
   Loki's 60-day retention selects them by `service_name`. Kubernetes events get `service.name` `kubernetes-events`.
6. *(Claude's call)* **The `otelcol-k8s` distribution**, the Collector project's own build for Kubernetes: it has every component above and less
   than `contrib`, so a smaller image to scan and fewer components to trust. Version 0.165.0 (the latest), the Operator chart 0.129.0
   (Operator 0.162.0); the builder checks the Operator accepts that Collector version and pins the newest pair that works.
7. *(Claude's call)* **Tempo** from grafana-community's chart 3.2.1 (Tempo 3.1.1), single binary, a PVC (`homelab/backup: none`, like Loki:
   telemetry is not worth restoring), 7 days of traces in production and 1 day in the sandbox; Grafana gets a Tempo data source (uid `tempo`),
   traces linked to Loki's logs by `trace_id`, and Loki's lines linked to their trace.
8. *(Claude's call)* **Where things live:** `system/opentelemetry` (the Operator, both Collectors and their `allow` policies), `system/tempo`,
   and `system/loki` without Alloy.
9. *(Claude's call)* **Sampling:** every Gateway request in the sandbox; in production a fraction small enough for Tempo's volume (the builder
   proposes it, e.g. 10%, through the Telemetry API's `randomSamplingPercentage`).

10. *(Claude's call, from the builder check)* **The agent sends logs to Loki directly, with a persistent queue.** Its OTLP exporter's
    `sending_queue` is stored in `file_storage` on its hostPath, beside its read positions, so lines read during a Loki outage survive the
    agent's restart, as Alloy's write-ahead log did; `LogsSurviveLokiOutage` keeps proving it. Logs never pass through the gateway, so the
    gateway needs no volume; Kubernetes events, which only it watches, are queued in memory (an event lost in an outage plus a restart is
    accepted, as they are not audit).
11. *(Claude's call, from the builder check)* **The Gateways write access logs** (Istio's Telemetry API, JSON to stdout, the request's
    `trace_id` among the fields), read by the agent like any pod's log: they are the log lines that link to the Gateways' traces in Grafana
    (a derived field on `trace_id`). The homelab's traffic is small; the measurement in decision 12 covers them.
12. *(Claude's call, from the builder check)* **Loki's volume is measured, not assumed.** OTLP adds structured metadata to every line; the
    agent drops the attributes no query uses (file paths, ids nothing reads), and the plan measures the bytes per audit line against step 15c's
    budget (7.3GiB of 10Gi for 60 days on one node). If it no longer fits, `CLAUDE.md`'s rule applies: shorten the audit period, never grow the
    volume.
13. *(Claude's call, from the builder check)* **The Gateways' traces go over the mesh.** The Gateways (namespace `istio-system`, outside the
    ambient data plane) already reach meshed apps with their own identity, which `allow-gateways` matches; their OTLP export to the gateway
    Collector is an ordinary upstream cluster and should do the same, so the Collector's `allow` admits them by identity. If the sandbox shows
    it cannot, the fallback is a port exception like `forgejo-db-status` (permissive on OTLP's port, a NetworkPolicy admitting only the
    Gateways' pods), recorded in the threat model's entry points.

## Constraints

- Every rule in `CLAUDE.md`: latest stable versions pinned; `homelab/backup` on every PVC; a securityContext or a `PolicyException` with its
  reason (the agent reads host paths as root, as Alloy did: it inherits Alloy's exception and namespace `privileged`); a `test/scopes.yaml`
  entry for each new component; the mesh's `allow` per namespace.
- **The mesh and webhooks:** the Operator's admission webhooks are called by the API server, so its pods carry `istio.io/dataplane-mode: none`
  and its NetworkPolicies let 15008 in; a webhook that mutates pods must not block pod creation cluster-wide while the Operator is down
  (`failurePolicy: Ignore`, as ESO's and Kyverno's).
- **OTLP stays inside** (threat model): no route to the gateway Collector; its `AuthorizationPolicy` admits only the agents, the Gateways'
  namespace and the tools that send; no egress to the internet.
- **Resources:** the sandbox is short already. Alloy goes (about 125Mi); the agent, the gateway, the Operator and Tempo must fit with small
  requests in `values-dev.yaml`, measured under the suite.
- **The first sync:** the Collectors are custom resources whose CRDs come with the Operator, so their Application waits for them like the other
  operators' (`SkipDryRunOnMissingResource` only where a cycle needs it, as `CLAUDE.md` says).

## What changes outside the new components

- `system/loki`: Alloy's subchart and its PolicyException removed; Loki's OTLP ingestion configured (`otlp_config`: which resource attributes
  become labels; the default set unless a query needs another); the retention selector on `service_name`.
- The Loki ruler's rules (`system/loki/files/*.yaml`: 11 audit, 2 Falco, 1 public scan) and the dashboard "Security": stream selectors to the
  new labels. The bodies stay the original lines, so the `| json` stages are unchanged.
- Istio (`system/istio-system`): the extension provider in `meshConfig` and a `Telemetry` resource for the Gateways.
- Argo CD's and Grafana's tracing settings; Grafana's data sources.
- `system/argocd`'s `templatePatch` (namespace `privileged` moves from Alloy's namespace to the agent's), `.cluster-scan-accepted.yaml`
  (Alloy's accepted findings become the agent's), `test/scopes.yaml`, `system/egress` if anything sends out (nothing should).
- Docs: `docs/how-to-guides/logs.md` and `security-logs.md` (the queries people type), the runtime-detection guide, `CLAUDE.md`'s lines on Loki
  and Alloy, a new guide for traces.
- Comments naming Alloy (OpenBao's audit device, k3d's audit mount, Falco's values).

## Testing

- **Offline:** renders of both Collectors (pipelines, receivers, exporters), the Operator's webhook settings, Tempo's PVC label, the retention
  selector, every ruler rule parsed with its new selector (the existing offline tests, rewritten), no Alloy left anywhere.
- **Sandbox:**
  - the existing specs rewritten to the new labels: `Logs*` (pod logs, events, the audit streams, `LogsResumeAfterRestart` now proving the
    agent's `file_storage`), `SecurityLogs*`, `Falco`, the monitoring specs that read logs;
  - a new spec: a request through the LAN Gateway leaves a trace in Tempo, and its access log line carries the same `trace_id`;
  - `LogsSurviveLokiOutage` kept: lines read during an outage arrive after the agent restarts (decision 10);
  - a new spec: the gateway Collector refuses OTLP from a namespace that has no reason to send.
- `test/scopes.yaml`: the `logs` family selects the specs above for changes to `system/opentelemetry`, `system/tempo` and `system/loki`.

## Production, unproven

Added to `CLAUDE.md`'s list for the first deploy: the agent on the NixOS nodes' log paths, three agents' volume into one gateway, Tempo's
volume at the chosen sampling, the Gateways' trace headers through Cloudflare.

## Left to the builder

- Each tool's tracing configuration beyond Argo CD and Grafana, and which tools have none.
- The Collectors' resource requests, measured, and the attributes the agent drops (decision 12).
- Whether Loki needs extra resource attributes indexed for a query the rewrite finds (keep the default set otherwise).
- The production sampling rate.

## Measured (the sandbox, 2026-11-15)

- **Memory:** the agent used 365Mi (Alloy 220Mi), the gateway 56Mi, the Operator 71Mi, Tempo 189Mi: about 460Mi more
  than Alloy. The node reached 92% under the suite and k3s restarted once; `GOMEMLIMIT` at about 80% of each limit and
  smaller sandbox limits (the agent 256Mi, the gateway 128Mi, Tempo 256Mi) followed.
- **Loki's volume:** an audit line averaged 1418 bytes (`k3s-audit`) and 1773 bytes (`openbao-audit`); OTLP added 213
  and 448 bytes of labels and structured metadata (15% and 25% before compression). The receiver's scope and the file's
  name (about 100 bytes a line) are dropped by the agent; what remains repeats line after line and compresses well, so
  the audit streams' 60 days stay within step 15c's budget (7.3GiB of 10Gi) and the period is unchanged.
