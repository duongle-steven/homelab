# Logs

Every pod's log and the cluster's Kubernetes events are in Loki (`system/loki`) for 30 days (7 in the sandbox).
Grafana → Explore → data source Loki.

| Query | Shows |
|---|---|
| `{namespace="forgejo"}` | every container of a namespace |
| `{namespace="forgejo", container="forgejo"} \|= "error"` | lines containing "error" |
| `{app="grafana"}` | one app (its `app.kubernetes.io/name`, else `app` label) |
| `{job="kubernetes-events", namespace="paperless"}` | a namespace's events (scheduling, pulls, probe failures, OOM kills) |

Alloy (a DaemonSet in namespace `loki`) reads `/var/log/pods` on each node and remembers where it stopped in
`/var/lib/alloy` on the node; one Alloy instance of the cluster gathers the events. Logs have no backup (step 11
decides).
