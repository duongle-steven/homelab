# Runtime detection (step 15g)

## Intent

Steps 15a to 15f make an attack hard to start: detection in CI, policy, security logs, scans, the supply chain and the public
surface. Nothing yet sees what happens inside a running container once something gets in: a shell nobody opened, a binary
that was not in the image, a read of `/etc/shadow`, a call to the API server with a stolen token. Step 15g adds that last
layer, following common security practice (the OWASP Kubernetes Top 10 checked in chat on 2026-10-25 lists runtime
detection), before the first production deploy.

## Decisions

- **Falco, not Tetragon.** Falco is a detection product (CNCF graduated) with a maintained ruleset mapped to MITRE ATT&CK.
  Tetragon's strengths are enforcement and observability, and its detections are TracingPolicies we would write ourselves.
  This fork also dropped Cilium in step 6a.
- **Detection only, no blocking.** A wrong kill stops a real workload, and one person watches this homelab. Woodpecker's
  Buildah steps, for one, run binaries created after the container started. Blocking is step **15g-2**: Falco Talon
  responds, either deleting the pod (its controller recreates it from the image) or isolating it with a NetworkPolicy
  (kept for forensics). It covers a short list of rules with almost no false positives:
  - a reverse shell;
  - the execution of a binary that is not in the image (drift), outside Woodpecker's steps;
  - a crypto miner;
  - a container escape (a kernel module load, `ptrace` of another process, a write to `/proc/sys`, a host mount).

  15g-2 waits for production's numbers: some weeks of detection only. This mirrors 15b-1 (Audit) then 15b-2 (Enforce).
- **Alerts follow 15c's path.**
  - Falco writes JSON events to stdout, and Alloy already ships pod logs to Loki (`job="pods"`, `namespace="falco"`).
  - Loki's ruler (`system/loki/files/falco-rules.yaml`, beside the audit and public-scan rules) alerts through
    Alertmanager to ntfy and Telegram.
  - No falcosidekick: it would be one more component for a path that already exists.
- **Priorities.**
  - Falco logs from `warning` up.
  - `error` and above alert: `critical`, `alert` and `emergency` as severity `critical`, `error` as `warning`.
  - `warning` shows only on the dashboard **Security**.
- **No download at runtime.** falcoctl's `install` and `follow` are off, so no egress is opened for Falco and every rule
  is pinned like every other version.
  - Rules come from the Falco image's stable ruleset, plus the rules 15g-2's list will need where they are not in the
    stable set. Those are vendored with their ruleset version, so 15g already reports what 15g-2 would act on.
- **Accepted events** are Falco exceptions or overrides in `system/falco/values.yaml`, each with its reason, like the
  Trivy Operator's accepted findings. Expected sources include Woodpecker's steps, istio-cni, Velero's data mover, the
  rotation and backup Jobs, and kubectl exec by the tests.
- **A silent detector is a failure.**
  - Falco's metrics are scraped (a ServiceMonitor).
  - `FalcoNotRunning` alerts when its DaemonSet does not run on every node (kube-state-metrics).
  - `FalcoEventsDropped` alerts on dropped syscall events (its drop counters).
  - `FalcoStalled` alerts when a node's Falco stops counting syscall events (its event counter does not rise for some
    minutes) or Prometheus cannot scrape it. Falco has no heartbeat log line, so this uses its metrics, not Loki (my
    call: a quiet node logs nothing at `warning`, so a log-based alert could not tell quiet from dead).
- **Runs everywhere, the sandbox included by default.** Proving it with the whole platform shows whether it slows the
  suite.
  - Its requests and limits are small in `values-local.yaml`, as Kyverno's and the Trivy Operator's are.
  - They are measured during `just dev-fresh`, and the guide records the measurement.
  - If the sandbox runs out of memory, the fallbacks, in order, are: lower Falco's limits, scrape less in the sandbox,
    make Falco optional like paperless. Retention would not help: Prometheus's and Loki's memory follows active series and
    ingestion, not days kept.
- **The testbed runs it too**, as a layer of its own on top of layers 1 and 2 (what production has under it), measured
  against the testbed's bar. Production's nodes run NixOS with `linuxPackages_latest` and the sandbox runs WSL's
  kernel, so only the testbed proves the modern eBPF driver on NixOS's kernel. With no Loki there, its spec reads the
  Falco pods' logs.
  - The testbed has no Kyverno or monitoring, so its values switch off the `PolicyException` and the `ServiceMonitor`:
    Argo CD refuses a whole sync that holds a `PolicyException` whose CRD is missing.
  - If the layer breaks the bar, VM sizes stay as they are. Falco goes to the testbed's `not_run`, the driver's proof on
    NixOS is one recorded run by hand (`TestbedFalco` alone on a fresh testbed), and "Before the first production
    deploy" in CLAUDE.md says so (my call: the bar protects this workstation, and the driver's load is what needs NixOS).
- **The sandbox's node shares WSL's kernel** with the workstation and every Docker container, and the probe sees all of
  them, never Windows (WSL 2 is a VM of its own). The sandbox keeps every event and alert as production does: what the
  workstation's own processes trigger stays in the sandbox's Loki. Falco's metadata knows only the k3d node's
  containerd, so those events carry no Kubernetes namespace.
  - Only the clean-baseline check counts events with a Kubernetes namespace, so the workstation's activity during a run
    cannot fail it.
  - A workstation process that turns out noisy is accepted in `values-dev.yaml` with its reason, never in
    `values.yaml`.
- **Production alerts from the first deploy.** Its workloads that the sandbox never runs (cloudflared, wireguard,
  tailscale, Matrix, jellyfin, Ceph's daemons, kube-vip, Renovate's initContainer) may fire rules the sandbox never saw.
  - Alerts are grouped by rule and namespace, so one noisy rule is one notification.
  - "Before the first production deploy" adds a review of Falco's events in the first days, with each one accepted
    (with its reason) or fixed.
  - No silence and no dashboard-only period: a detector nobody hears does not detect (my call).

## Shape

- `system/falco`: a wrapper chart of `falcosecurity/falco`, at the latest stable chart and app version, pinned (chart
  9.2.2 and Falco 0.45.2 when this was written; check upstream when building). It runs in namespace `falco`:
  - the `modern_ebpf` driver: CO-RE, so no kernel module or headers. WSL's 6.18 kernel has BTF, and the NixOS nodes'
    kernels do too;
  - container metadata from containerd's socket. The chart's container collector already lists k3s's
    `/run/k3s/containerd/containerd.sock`; no k8s-metacollector;
  - `json_output`, with `priority: warning`;
  - `base_syscalls` set if the measured CPU calls for it.
- **Kubernetes plumbing:**
  - The namespace is `privileged` in Argo CD's `templatePatch` and outside the mesh (`istio.io/dataplane-mode: none`).
  - A `PolicyException` with `homelab/reason` (privileged, hostPID, host mounts).
  - A NetworkPolicy opening only the metrics port to Prometheus, with HBONE where the checks need it
    (`scripts/check-mesh-policies`).
  - Egress: none beyond the default.
- `system/loki/files/falco-rules.yaml`: the alert rules above, from Falco's JSON fields (`priority`, `rule`,
  `output_fields`: container, pod, namespace). Grouped by rule and namespace, so one noisy rule is one notification.
- **Dashboard Security:** a panel of Falco events by priority, rule and namespace.
- **Wiring:** Argo CD's lists in the sandbox, testbed and production values; `test/scopes.yaml` (a new label `falco`, and
  `system/falco/**` mapped to it); the guide `docs/how-to-guides/runtime-detection.md` (reading an event, accepting one
  with a reason, what to do on an alert); CLAUDE.md (15g done, 15g-2 planned, "Before the first production deploy" naming
  what only the sandbox and the testbed proved).
- **No secret** is created: nothing to add to the rotation catalog.

## Proof

- **Offline:** the render passes `scripts/ci-check` (policy, mesh, env-config, backup groups: no PVC). An offline test
  checks that falcoctl's artifact install and follow are off, the driver is `modern_ebpf`, and every accepted exception
  carries a reason.
- **Sandbox, e2e spec `Falco`** (labels `sandbox`, `falco`):
  - in a namespace of its own, it runs a pod, opens a shell in it and reads `/etc/shadow`;
  - it proves the events reach Loki with their rule names, and that the deployed alert expressions select them.
  The Trivy Operator's spec uses the same pattern.
- **Sandbox, clean baseline:** after a full suite on a fresh sandbox, no `error`-or-above event remains that is neither
  the spec's own nor accepted.
- **Testbed, spec `TestbedFalco`** (labels `testbed`, `falco`): Falco is ready on all three nodes, its driver loaded, and
  it logs an event a test pod triggers.

## Non-goals

- Blocking or responding (15g-2).
- Tetragon, falcosidekick, k8s-metacollector.
- Falco's Kubernetes audit plugin: 15c reads the audit log.
- Falco on the workstation.
- Shortening Prometheus's or Loki's retention in the sandbox.
- The `Localhost` seccomp profile for Woodpecker's Buildah steps that would allow `seccomp-default` (15d's deferred
  minor): its own step, 15h.

## Anti-goals

- Killing or restarting a real workload because of a rule.
- An alert stream so noisy that alerts get ignored. The baseline must be clean before merge.
- A detector that stops without anyone knowing.
- Rules or drivers fetched from the internet at runtime.

## Left to the builder

- Exact rule names, the accepted exceptions, and `FalcoStalled`'s window.
- The vendored ruleset files and their versions.
- Requests and limits from the measurement.
- Whether `base_syscalls` is needed.
