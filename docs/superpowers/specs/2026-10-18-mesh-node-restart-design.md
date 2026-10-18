# The ambient mesh recovers after a node restart

Status: design approved in chat 2026-10-18; this written spec is waiting for review. Branch: `fix/mesh-node-restart`
(PR into `dev`), on top of the branch's two spike commits.

## Goal

After a node restarts, whether it rebooted, crashed or lost power, the pods on it rejoin the ambient mesh and serve
again without anyone stepping in. A sandbox test that restarts the node proves this on every `make dev`.

## Problem

On 2026-10-17, after PR #36 merged, `make dev` on `dev` failed. etcd's slow fdatasync made k3s lose leader election
and exit, and the k3d node container restarted. After the restart, every meshed pod on the node stayed `Pending`, and
ztunnel never became Ready. The mesh never recovered: one run was stuck for 92 minutes before it was stopped. A pod
that is deleted, and so gets a new UID, starts normally.

ztunnel's log repeats this line for each stuck pod:

```
error inpod::workloadmanager failed to start proxy: Bind([::1]:15053, Os { code: 99, kind: AddrNotAvailable, ... })
```

istio-cni's log shows `cmdAdd` timing out:

```
istio-cni cmdAdd failed to contact node Istio CNI agent: failed to send event request: Post "http://unix/cmdadd":
context deadline exceeded (Client.Timeout exceeded while awaiting headers)
```

Upstream knows the symptom, but nobody has explained it. istio/istio#52858 and #55794 ("after restarting the node")
and istio/ztunnel#1707 report it on AKS, kubeadm and k3s, from 1.24 to 1.28.

- A maintainer fixed a related race (istio/istio#56738, a deleted pod) but said the node-restart case is a different
  problem.
- They guessed that "the pod's netns changes without the UID changing", but have "never been able to reproduce it".
- #55794 was closed by the stale bot. Istio 1.31.2 (2026-10-15) does not change this behaviour.

## Investigation (2026-10-17/18, sandbox, Istio 1.31.1, k3s v1.37.1+k3s1, containerd 2.3.4)

Each run did `docker restart k3d-homelab-dev-server-0` on a healthy sandbox. A throwaway sampler inside the node
recorded, every second:

- every netns's loopback addresses;
- the pod sandboxes and their netns;
- the netns file descriptors ztunnel held.

It also collected the pod logs from `/var/log/pods` and the kubelet log.

### Timeline (Istio defaults: istio-cni patches `10-flannel.conflist`, IPv6 rules on)

| After the node is up | What happened |
|---|---|
| +7s | k3s's API server is up |
| +14s | ztunnel and istio-cni start. They are DaemonSet pods, so they restart in place, in parallel and in no fixed order: in another run istio-cni started at +15s and ztunnel at +42s |
| +14s → +70s | ztunnel waits for istiod's XDS. First DNS fails (CoreDNS is not up), then TCP fails (istiod is not up), and its backoff grows to 10.24s. ztunnel connects to istio-cni's socket only once XDS is established: "Stream established" and "new ztunnel connected" are 1 ms apart |
| +27s | CoreDNS and istiod are Deployment pods, so they are replaced by new pods (new names and UIDs), which are scheduled again. Their sandboxes are ready at +45s; CoreDNS is ready at +52s and istiod at +60s |
| +34s → +40s | The kubelet recreates the sandboxes of all 28 meshed pods. istio-cni's agent has no ztunnel connection, so it marks each pod `ambient.istio.io/redirection: pending` and returns an error. The plugin had already timed out after 5s. containerd tears each sandbox down, and the loopback plugin's DEL sets `lo` down: `127.0.0.1` stays on `lo`, but `::1` is removed |
| +70s | ztunnel connects. The agent sends its snapshot, which still holds the netns of the sandboxes that were torn down |
| after that | ztunnel starts a proxy in each dead netns. Binding `[::1]:15053` fails because the address is gone, so the entry stays pending and ztunnel stays not Ready. New sandboxes (same UID, new netns) keep timing out, so the loop never ends |

### Why it never ends: the agent cannot answer within 5 seconds

The plugin waits 5s for the agent (`cni/pkg/plugin/cnieventclient.go:49`, `Timeout: 5 * time.Second`, hardcoded). The
agent processes the pod adds that the kubelet keeps retrying one after another, and most of its time goes to
`iptables-nft-restore`, at about 0.36s per call on WSL. With IPv6 rules on, each pod needs an IPv4 and an IPv6
restore.

The agent's time per add, from "adding pod to the mesh" to its answer:

| istio-cni setting | Median per minute | Fastest | Adds under 5s |
|---|---|---|---|
| defaults (IPv6 rules on) | 12.3–13.2s | 9.6s | 0 in 5 minutes, so it never recovers |
| `cni.ambient.ipv6: false` | 5.1–5.4s | 4.6s | 2 per minute, rising to 31, so it recovers in 5–13 minutes |

No add ever succeeds while the agent's latency stays above 5s, so the backlog never shrinks. When a few adds do
succeed, the backlog shrinks, more adds fit under 5s, and the loop unwinds. It is a contest between how much work is
queued and how fast the agent works through it. Memory plays no part.

ztunnel itself handles a pod that comes back with the same UID and a new netns. `src/inpod/statemanager.rs` (1.31.1):

- If an add arrives with a different netns inode, ztunnel replaces the old proxy (lines 262–271).
- If that add succeeds, ztunnel drops the old pending entry (lines 243–244). Its test `workload_added_while_pending`
  is the regression test for #52858.
- ztunnel is Ready once it has the snapshot and no pending entries (lines 346–349).

So a few stuck pods can heal once a fresh add gets through. Many stuck pods keep the agent and ztunnel too busy for
that. Maintainers already note in istio/istio#53371 that the design, where the snapshot and the "PartialAdd" retries
act as a queue, has no reconciliation.

### Results

| Configuration | Restarts | Outcome |
|---|---|---|
| Defaults | 5 | Never recovered (10, 12, 20+ and 92 minutes observed) |
| `istioOwnedCNIConfig` only | 1 | Not recovered after 10 minutes |
| `istioOwnedCNIConfig` + `cni.ambient.ipv6: false` | 4 | Recovered after 5.5, 6.5, 6.5 and 13.4 minutes |
| Defaults, stuck for 92 minutes, then the istio-cni pod deleted | 1 | Recovered 3.7 minutes later |

After that last recovery:

- ztunnel served all 29 meshed workloads over HBONE, and every pod carried `redirection: enabled`.
- The apps answered as before the restart: draw, home, speedtest and budget 200, pairdrop 404, grafana 302, git 303.
- Restarting ztunnel alone does not help (tried on 2026-10-17), because the agent sends it the same snapshot again.
- Restarting istio-cni does help, because its agent rebuilds the snapshot.

### Other findings

- On every start k3s rewrites `10-flannel.conflist`, and the istio-cni plugin entry disappears from it: the entry was
  there before a restart and counted 0 after. Pods that the kubelet starts before the agent patches the file again
  never pass through istio-cni and run outside the mesh (istio/istio#55968, #60882).
- With `cni.istioOwnedCNIConfig: true`, Istio writes its own `02-istio-cni.conflist`, which k3s does not touch. It is
  the setting the maintainers point to for that race (istio/istio PR #56156; #60882: "This is solved by the
  ISTIO_OWNED_CNI setting"). It does not fix the deadlock.
- ztunnel's own `IPV6_ENABLED=false` would stop ztunnel from binding `[::1]`. In a dead netns ztunnel would then
  "succeed", turn Ready, and leave the real pod outside the mesh. It is rejected.
- istiod (HPA min 1) and k3s's CoreDNS run one replica each, as upstream `nixos` also does (`infra/_modules/platform/istio.tf`).
- The official `registry.k8s.io/kubectl` image has no shell. `alpine/kubectl` 1.37.1 has `sh` and BusyBox `date`.

## How likely this is in production

The deadlock needs pods to start while their node's ztunnel is not connected, and enough of them at once to keep the
agent above 5s.

- **kured reboot (drain first, one node at a time):** pods move away before the reboot and come back after the node
  is Ready, by which time ztunnel is usually connected. Rare.
- **One node crashes (no drain), istiod and CoreDNS on other nodes:** ztunnel gets XDS within seconds of starting.
  A container that starts late (+42s in one run) can still lose the race, but only about a third of the pods live on
  each node, and real CPUs run iptables faster than WSL. Unlikely, and a stuck node probably heals by itself.
- **The node running the only istiod or CoreDNS crashes:** Kubernetes waits about 5 minutes (the default NotReady
  toleration) before rescheduling them. Without CoreDNS, the whole cluster loses DNS in that window.
- **Power loss (every node restarts):** this is the sandbox's situation. It is unmeasured on real hardware.

## Decisions

| Topic | Decision |
|---|---|
| Spike commits | Keep them in the history (they record what was tried); later commits drop `ipv6: false` and fix the comment |
| `cni.ambient.ipv6: false` | Dropped. It helps (5–13 minutes instead of never), but it silently stops IPv6 redirection on an IPv6 or dual-stack host such as upstream's `hetzner-1` (`infra/production/metal/hosts.json`, IPv6 only). The watchdog covers what it covered |
| `cni.istioOwnedCNIConfig: true` | Kept. Its comment names the k3s rewrite of `10-flannel.conflist` and istio/istio#55968 |
| Watchdog | A CronJob `istio-cni-watchdog` in `istio-system`, in production and in the sandbox |
| Watchdog rule | For each node: if the ztunnel container has been running for more than 180s and is not Ready, and the istio-cni container on that node has been running for more than 180s, delete that node's istio-cni pod. A healthy ztunnel is Ready within seconds, and the 180s istio-cni age stops repeated restarts |
| Watchdog schedule | Every minute; `concurrencyPolicy: Forbid`; `startingDeadlineSeconds: 50`; the Job's `activeDeadlineSeconds: 50` and `ttlSecondsAfterFinished: 300`; history limits 1 |
| Watchdog image | `alpine/kubectl:1.37.1`, pinned by digest (it needs a shell; the official image has none). `runAsNonRoot` (UID 65534), read-only root filesystem, every capability dropped, small requests |
| Watchdog RBAC | ServiceAccount, Role and RoleBinding in `istio-system`: pods `get`, `list`, `delete`. The rule now looks only at ztunnel and istio-cni pods, so no cluster-wide read is needed |
| Watchdog network | `istio-system` is outside the mesh and outside `system/egress`, so the pod reaches the API server. The pod is excluded by namespace, so it starts while ztunnel is stuck. `scripts/check-mesh-policies` decides whether it needs a NetworkPolicy: it has no ports to open |
| istiod in production | `pilot.autoscaleMin: 2` and a `topologySpreadConstraints` entry on `kubernetes.io/hostname` (`maxSkew: 1`, `ScheduleAnyway`) in `values.yaml`. `values-dev.yaml` sets `autoscaleMin: 1` (one node) |
| CoreDNS in production | k3s manages it, and `kube-system` is outside Argo CD. Two replicas spread over nodes go into the production checklist for step 12 (NixOS k3s config) |
| Istio | Bump every Istio chart from 1.31.1 to 1.31.2, the latest stable, as the version rule requires when touching a component. Its notes are security fixes and do not touch this |
| Upstream report | A separate issue on istio/istio linking #55794, with the timeline and the latency table. It is public, so it is the user's call after this PR. Not part of this work |

## Test

`TestNodeRestart` in `test/node_restart_test.go`. The name avoids every word in `make dev`'s main filter (`Mesh` would
match `TestMeshSurvivesNodeRestart`, because `go test -run` is not anchored).

1. Record each app's status code through the Gateway, as `probe.sh` did (draw, home, speedtest, pairdrop, budget,
   grafana, git), and check the cluster is healthy first.
2. Run `docker restart` on the node container (the name comes from the test environment).
3. Within 12 minutes, all of these hold:
   - the API server answers;
   - every ztunnel pod is Ready;
   - no pod is outside `Running` or `Succeeded`, ignoring completed Jobs;
   - every pod in a meshed namespace carries `ambient.istio.io/redirection: enabled`;
   - every app returns the code recorded in step 1.
4. On failure, the test logs ztunnel's not-ready state and the pods that are not running.

`make dev` runs it on its own after the main parallel run: a second `smoke-test` call with
`smoke_filter='NodeRestart'`, because it takes the whole cluster down. That adds about 6 to 8 minutes.

- RED: before the watchdog exists, with `ipv6: false` dropped, the test fails at its 12-minute limit. This is already
  shown 5/5 times; the run is repeated on the branch.
- GREEN: with the watchdog, it passes. Expected recovery is the 180s threshold plus up to a minute for the schedule
  plus about 3.7 minutes, so 6 to 8 minutes, inside 12.

The existing checks (`render-chart`, `check-mesh-policies`, `check-env-config`, `check-secret-stores`, go vet and the
offline tests, `mkdocs build --strict`, `make -C infra check`) pass. `make dev-clean && make dev` passes from scratch
(Istio's bump changes the CNI config on a running sandbox).

## Documentation

- CLAUDE.md, Sandbox: the watchdog, why it exists, and that `make dev` ends with `TestNodeRestart`.
- CLAUDE.md, the production list (each item marked "only in the sandbox"):
  - the watchdog and node-restart recovery ran only on the one-node sandbox;
  - istiod's two replicas and their spread were only rendered;
  - CoreDNS needs two replicas spread over nodes in step 12;
  - power loss is unmeasured.
- `docs/concepts/` (networking or mesh page, whichever describes ambient): a short section on node restarts. It covers
  the deadlock in two sentences, the watchdog, `istioOwnedCNIConfig`, and where to look (`kubectl -n istio-system get
  pods`, the watchdog's Job logs).
- `docs/superpowers/deferred-minors.md`: this PR adds whatever minors its review defers.

## Out of scope

- Patching Istio (raising the 5s timeouts, dropping dead netns from the snapshot). That is upstream's fix.
- Measuring power loss on real hardware, which is step 12.
- CoreDNS replicas, which are step 12.
