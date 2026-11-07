# Mesh watchdog: the stuck pods, not only ztunnel's Ready

Before the first production deploy (CLAUDE.md: "Fix the watchdog in its own PR first"). Agreed in chat on 2026-11-07.

## Why

`istio-system/istio-cni-watchdog` (`system/istio-system/files/cni-watchdog.sh`, step 6, `docs/superpowers/specs/2026-10-18-mesh-node-restart-design.md`)
restarts a node's istio-cni when that node's ztunnel has run 180 s without becoming Ready. ztunnel reports Ready while
the mesh on its node is still stuck:

- 12a-3: one `TestNodePowerCut` of three took about 23 minutes, 15 of them with the watchdog seeing nothing to do,
  while ztunnel retried dead network namespaces (`Bind [::1]:15053`).
- 15f: a sandbox started from stopped left pods in `ContainerCreating` for minutes, every sandbox failing with
  `plugin type="istio-cni" name="istio-cni" failed (add): istio-cni cmdAdd failed to contact node Istio CNI agent`,
  ztunnel Ready; deleting both istio-cni and ztunnel by hand cleared it.

## What

1. **A second signal: the symptom.** A node is stuck when a pod on it has had no sandbox for more than
   `NOT_READY_SECONDS` (180) (`Pending` with `PodReadyToStartContainers` False: not a pod waiting in an init container
   or an image pull) and a `FailedCreatePodSandBox` event for that pod, seen within the same window, says istio-cni's
   add failed (`type="istio-cni"` and `failed (add)`). The watchdog lists the events and the `Pending` pods once each,
   whatever their number: after a power cut there are hundreds, and its Job has 50 s. The first signal stays: ztunnel running and not
   Ready for more than 180 s. Events are read through the API: no log access, and no dependence on ztunnel's log
   wording, which changes between releases. A pod stuck for another reason (image pull, volume) has no such event and is
   left alone.
2. **Escalation, without state.** For a stuck node, nothing happens while its istio-cni or its ztunnel started less
   than 180 s ago (a restart is still taking effect). Otherwise the watchdog restarts istio-cni if istio-cni started
   before ztunnel (as today), and both istio-cni and ztunnel if istio-cni started after it (istio-cni was already
   restarted and the node is still stuck); the pods' start times are the only state. First built as ztunnel alone: two
   recorded power cuts on the sandbox (2026-11-07) cleared only once both were new (istio-cni then ztunnel, or ztunnel
   then istio-cni), each step costing another 180 s, so the second step restarts both.
3. **RBAC.** A ClusterRole to list events and pods in every namespace; deleting stays limited to `istio-system`'s
   pods (the Role).

## Proof

- `TestIstioCNIWatchdog` (offline, fake `kubectl`): the new signal, an event outside the window, an event for another
  plugin, a pod no longer `Pending`, a pod whose sandbox exists, a young pod, a pod of the same name elsewhere, the escalation to both pods, and the number of API calls with hundreds of events (`TestIstioCNIWatchdogFewCalls`).
- The sandbox: `TestNodeRestart` and `TestNodePowerCut` (they rely on the watchdog), with the time each took.

## Not in scope

- ztunnel's own health (its admin port, its metrics): the stuck pods are what matter, and they are visible through the
  API.
- Several nodes at once (each node is judged alone, as today); the testbed's multi-node runs come with 12b.
