# Logs

Every pod's log, the audit logs and the cluster's Kubernetes events are in Loki (`system/loki`) for 30 days (7 in the
sandbox). Grafana → Explore → data source Loki.

Labels follow OpenTelemetry's conventions (the Collector sends over OTLP):

| Query | Shows |
|---|---|
| `{k8s_namespace_name="forgejo"}` | every container of a namespace |
| `{k8s_namespace_name="forgejo", k8s_container_name="forgejo"} \|= "error"` | lines containing "error" |
| `{service_name="grafana"}` | one app (its `app.kubernetes.io/name`) |
| `{service_name="kubernetes-events"} \|= "paperless"` | the events naming a namespace (scheduling, pulls, probe failures, OOM kills) |
| `{k8s_namespace_name="istio-system"} \|= "<trace id>"` | the Gateways' access log lines of one request ([Traces](traces.md)) |

Other attributes (the node, the pod's uid, the workload) are structured metadata: filter them after the selector, e.g.
`{k8s_namespace_name="loki"} | k8s_node_name="homelab-1"`.

The OpenTelemetry agent (a DaemonSet in namespace `opentelemetry`) reads `/var/log/pods` and the API server's audit log
on each node, remembers where it stopped and queues what Loki has not taken yet in `/var/lib/otelcol` on the node, so a
Loki outage plus a restart of the agent loses no line. The gateway Collector gathers the cluster's events once. Logs
have no backup (step 11 decides).
