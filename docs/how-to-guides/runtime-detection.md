# Runtime detection

Falco (`system/falco`) watches every node's system calls through its modern eBPF probe. It reports what happens inside
running containers that should not: a shell nobody expected, a read of `/etc/shadow`, a binary that was not in the image,
a reverse shell, a kernel module loaded, `ptrace` on another process, a container escape. It only detects; nothing is
blocked (step 15g-2 will respond to a short list of rules, after production's first weeks).

Its events are in Loki, beside the other logs ([Logs](logs.md)), and on the dashboard **Security** (row "Runtime
(Falco)"). Error and above raise alerts, which reach ntfy and Telegram like every other alert ([Alerts](alerts.md)).

| Priority | Where it goes |
|---|---|
| `emergency`, `alert`, `critical` | alert `FalcoCriticalEvent` (severity critical) |
| `error` | alert `FalcoErrorEvent` (severity warning) |
| `warning` | the dashboard only |
| `notice` and below | not logged |

## Reading an event

Grafana → Explore → data source Loki:

```logql
{namespace="falco"} | json rule="rule", priority="priority", ns=`output_fields["k8s.ns.name"]` | __error__=""
```

Each line is one event: `rule`, `priority`, `output` (one sentence with the process, its command line, the user, the
container and the pod) and `output_fields` (the same as fields). An event without `k8s.ns.name` came from a process
outside Kubernetes. On the nodes that is the node itself. In the sandbox, which shares WSL's kernel, it can also be the
workstation or another Docker container. Falco cannot see Windows.

## When an alert fires

- **`FalcoCriticalEvent` or `FalcoErrorEvent`.**
  1. Open the event (the alert's description has the query).
  2. Find out who or what ran the process: the pod, its image, the command line, the parent process. For an exec, the
     API server's audit log names the person ([Security logs](security-logs.md)).
  3. If it was an attack, isolate the pod (scale it to 0, or a NetworkPolicy denying all), keep its logs, then follow
     [Secrets rotation](secrets-rotation.md) for what the pod could read.
  4. If it is expected, accept it (below). Never accept an event you cannot explain.
- **`FalcoNotRunning`**: a node has no ready Falco, so nothing watches it. `kubectl -n falco get pods -o wide` shows
  which; its events (`kubectl -n falco describe pod`) say why.
- **`FalcoStalled`**: Prometheus cannot scrape a Falco, or its syscall counter has not moved for 10 minutes. The probe
  may have detached. Read its log and restart the pod.
- **`FalcoEventsDropped`**: Falco's ring buffer overflowed on a busy node, so rules may have missed events. Raise its
  CPU limit or its buffer (`falco.driver.modernEbpf.bufSizePreset`).

## Accepting an event

An accepted event narrows its rule with a Falco exception; it never disables the rule. Add it to `accepted` in
`system/falco/values.yaml`, with its reason:

```yaml
accepted:
  # ...the entries already there
  - rule: Read sensitive file untrusted
    name: backup-reads-shadow
    fields: [k8s.ns.name, proc.name]
    comps: ["=", "="]
    values: [["velero", "kopia"]]
    reason: "Velero's data mover reads every file of the volumes it backs up"
```

`fields` and `comps` are Falco's field names and comparisons (`=`, `in`, `startswith`…), and `values` holds one row per
match (a value under `in` is a list). A process seen only on the workstation goes in `acceptedWorkstation` in
`values-dev.yaml`, never in `values.yaml` (Helm would replace `accepted` itself). Every entry needs
`rule`, `name` and `reason` (`TestFalcoAccepted`).

## What it costs

In the sandbox, Falco's pod was measured during a full suite on a fresh sandbox (`just dev-fresh`). Its requests in
`values-local.yaml` are near the median and its limits about twice the peak (production keeps the chart's: 100m and
512Mi requested, 1 core and 1Gi at most).

| | Median | Peak | Request | Limit |
|---|---|---|---|---|
| Memory | 145Mi | 165Mi | 160Mi | 384Mi |
| CPU | 0.036 core | 0.095 core | 40m | 200m |

No event was dropped during that run, so `base_syscalls` stays at Falco's default. If `FalcoEventsDropped` fires on a
busy node, raise the CPU limit first.

## What is proven where

- **Sandbox** (`Falco`): a read of `/etc/shadow` and a dropped binary in a test pod reach Loki with their rules, and
  `FalcoCriticalEvent`'s expression selects them.
- **Testbed** (`TestbedFalco`): Falco runs on the three NixOS nodes with the modern eBPF probe and reports a dropped
  binary.
- **Production**: its own workloads (cloudflared, the VPN routers, Matrix, jellyfin, Ceph's daemons, kube-vip) were
  never watched. Review Falco's events in the first days after the first deploy, and accept each with its reason or fix
  its cause.
