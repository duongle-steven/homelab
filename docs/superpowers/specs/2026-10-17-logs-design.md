# Step 9: Logs with Loki 3 and Grafana Alloy, in production and the sandbox

Status: design approved in chat 2026-10-17, written spec awaiting review. Branch: `feat/logs` (PR into `dev`, after
step 8b's PR #35 merges).

## Goal

Every pod's logs and the cluster's Kubernetes events land in Loki and stay queryable in Grafana for 30 days, in
production and in the sandbox, where tests prove the path. The deprecated `loki-stack` chart (Loki 2.x, Promtail) is
replaced by the maintained `loki` chart (single binary) and Grafana Alloy. The roadmap gains this step as 9; Git and
CI, Backups, Nodes and Remote access become 10 to 13.

## Context

- `system/loki` wraps `loki-stack` 2.10.1 (Grafana's repository): Loki 2.9 with Promtail, ServiceMonitor on. It runs
  only in production (the root ApplicationSet takes `system/*`; the sandbox's list omits it and
  `system/monitoring-system/values-dev.yaml` drops Grafana's Loki data source). Its `allow` admits Grafana and
  Prometheus on 3100; `scripts/check-mesh-policies` has Grafana → Loki in `EXTRA`.
- Upstream `master` and `nixos` use the same chart: Promtail as a DaemonSet reading `/var/log/pods` (and
  `/var/lib/docker/containers`) through hostPath; neither collects Kubernetes events.
- Found 2026-10-17: Grafana's repository marks `loki-stack` deprecated (last 2.10.3, Loki 2.9.3); `loki-distributed`
  and `lgtm-distributed` are deprecated too; no maintained all-in-one chart exists. Promtail reached end of life on
  2026-03-02; Grafana Alloy replaces it. The `loki` chart moved to `https://grafana-community.github.io/helm-charts`
  (Grafana's own copy stopped at 7.3.0); simple scalable mode is deprecated before Loki 4.0. `k8s-monitoring` (Alloy
  with ready-made pipelines) overlaps kube-prometheus-stack.
- k3s runs containerd: the kubelet writes every container's log under `/var/log/pods`; `/var/lib/docker/containers`
  is empty there.
- `loki.source.kubernetes_events` supports Alloy's clustering: watching every namespace, only one clustered instance
  collects; positions live in Alloy's storage path.
- Decided 2026-10-17: separate charts (`loki`, plain `alloy`), Alloy as a DaemonSet reading files like upstream's
  Promtail, events kept 30 days, the step numbered 9.
- Versions (2026-10-17): chart `loki` 18.17.0 (Loki 3.7.8), chart `alloy` 1.13.1 (Alloy v1.20.1).

## Decisions

| Topic | Decision |
|---|---|
| Component | `system/loki` (namespace `loki`) wraps both charts, as upstream wraps `loki-stack` |
| Loki | `deploymentMode: SingleBinary`, 1 replica, filesystem storage on a 10Gi PVC, schema `tsdb` v13, `auth_enabled: false`, retention 30 days (compactor) |
| Loki extras off | gateway, canary, chunks and results caches, minio, helm test, self-monitoring dashboards that need the operator |
| Collector | Alloy DaemonSet; `discovery.kubernetes` (pods on its node) → `loki.source.file` on `/var/log/pods` (hostPath, read-only) → `loki.process` (`stage.cri`) → `loki.write` to `http://loki:3100` |
| Labels | `namespace`, `pod`, `container`, `app` (the pod's `app.kubernetes.io/name`, else `app`), `node`; stream from the CRI line |
| Positions | Alloy's storage path on a hostPath (`/var/lib/alloy`), so a restarted pod resumes where it stopped |
| Events | `loki.source.kubernetes_events`, all namespaces, clustering on (one instance collects), labels `namespace`, `job` |
| RBAC | Alloy's chart ClusterRole: pods and nodes (discovery), events (get, list, watch) |
| Mesh | Alloy stays in the ambient mesh; same-namespace calls are allowed; Prometheus scrapes Alloy (12345) and Loki (3100) |
| Grafana | Data source `http://loki.loki:3100` in production and the sandbox |
| Sandbox | `system/loki` joins the sandbox's root list; smaller PVC and retention (`values-dev.yaml`) |
| Roadmap | Step 9 Logs; old 9 to 12 become 10 to 13 everywhere they are named |
| Backup | Not in this step: step 11 decides whether Loki's PVC is backed up (default: not) |

## Design

### `system/loki`

`Chart.yaml` depends on `loki` 18.17.0 (grafana-community) and `alloy` 1.13.1 (grafana). `values.yaml`:

- `loki`: single binary as above; `loki.limits_config.retention_period: 720h`, compactor with retention enabled and
  its delete-request store on the filesystem; `singleBinary.persistence.size: 10Gi`; ServiceMonitor on (the chart's
  `monitoring` block), everything that needs Grafana Agent operator or Loki's own canary off.
- `alloy`: `controller.type: daemonset`; `alloy.clustering.enabled: true`; mounts `/var/log/pods` read-only and
  `/var/lib/alloy` (hostPath, `DirectoryOrCreate`) as storage; runs as root (the log files are root's, read-only
  mount); config in `alloy.configMap.content` (River) with the pipeline above and the events source; ServiceMonitor on.
- `templates/authorization-policy.yaml`: unchanged rules (same namespace, Grafana and Prometheus on 3100) plus
  Prometheus on Alloy's 12345.

`values-dev.yaml`: PVC 2Gi, retention 7 days.

### Wiring

- `system/argocd/values-dev.yaml`: `system/loki` in the root directories.
- `system/monitoring-system/values-dev.yaml`: drop the override that removes Grafana's Loki data source.
- `scripts/check-mesh-policies`: Grafana → Loki stays; the ServiceMonitor checks see the new ports.
- Egress: nothing new (the API server is in the baseline).

### Roadmap renumbering

CLAUDE.md's roadmap inserts "9. Logs: Loki 3 (single binary) and Grafana Alloy (pod logs and Kubernetes events), in
the sandbox too (done)" and renumbers 9–12 to 10–13; every "step 9/10/11/12" in docs, comments, CLAUDE.md and
`docs/superpowers/deferred-minors.md` follows (about 15 places, plus those 8b added).

## Error handling

- Loki down or its PVC full: Alloy buffers and retries (its write-ahead log in the storage path); logs older than the
  buffer are lost; kube-prometheus-stack's pod alerts (crash loops, PVC filling up) reach ntfy.
- A pod deleted before Alloy read its last lines: they are lost (the kubelet removes its log directory).
- The events instance moves to another Alloy pod (node drain): a few events may be delivered twice.
- Losing Loki's PVC loses past logs only. Whether step 11 (backups) declares Loki's PVC is decided there; until then
  logs have no backup.

## Testing

- Offline: renders (prod, dev), `scripts/check-mesh-policies`, `scripts/check-env-config`, `scripts/check-secret-stores`.
- Sandbox (`test/logs_test.go`, through the API server's service proxy as `prometheusQuery` does):
  - a temporary pod prints a marker; a LogQL query `{namespace="<ns>", pod="<pod>"} |= "<marker>"` returns it within a
    few minutes, with the labels above;
  - a Kubernetes event of that namespace (for example the pod's `Scheduled` or `Pulled`) is in Loki;
  - after the Alloy pod on the node restarts, a second marker arrives and the first is not delivered again;
  - Grafana's Loki data source health check succeeds (Grafana API, admin user);
  - Prometheus has Loki's and Alloy's targets up.
- `make dev-clean; make dev`: every test passes; the `make dev` filter gains `Logs`.

## Docs

- `docs/reference/architecture/overview.md`: the logs path.
- A short how-to: querying logs and events in Grafana (Explore → Loki, label examples).
- CLAUDE.md: roadmap (step 9 done, renumbered; step 11 gains "decide whether Loki's PVC is backed up"), Sandbox (Loki and Alloy run there), production checklist (Loki and
  Alloy ran only in the sandbox: rook-ceph PVC, retention sizing, `/var/log/pods` on NixOS k3s nodes).
