# Mesh watchdog Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** the watchdog also acts on pods stuck behind istio-cni's failed add, and escalates to ztunnel.

**Spec:** `docs/superpowers/specs/2026-11-07-mesh-watchdog-design.md`

## Global Constraints

- POSIX sh on BusyBox (the image `cniWatchdog.image`); RFC 3339 times through the existing `epoch()`.
- Window `NOT_READY_SECONDS` (180) for every age.

### Task 1: the signal, the escalation and RBAC

**Files:** `system/istio-system/files/cni-watchdog.sh`, `system/istio-system/templates/cni-watchdog.yaml`,
`test/cni_watchdog_test.go`.

- [ ] The fake `kubectl` answers three queries: ztunnel pods as `name node ready startedAt`, istio-cni per node as
  `name startedAt`, `FailedCreatePodSandBox` events as `namespace/name lastTimestamp message`, and a pod as
  `nodeName phase creationTimestamp` (`get pod <name>`). Existing cases move to the new ztunnel format.
- [ ] New cases in `TestIstioCNIWatchdog`, run first and seen failing: `stuck pods, ztunnel ready` deletes istio-cni;
  `stuck pods, istio-cni restarted after ztunnel` deletes ztunnel; `old event` (outside 180 s), `other plugin`,
  `pod running`, `young pod` delete nothing; `ztunnel just restarted` with stuck pods deletes nothing.
- [ ] The script: collect stuck nodes from the events, judge each ztunnel line by either signal, decide by the two
  start times.
- [ ] The ClusterRole and its binding (events list, pods get in every namespace).
- [ ] `go test -run TestIstioCNIWatchdog`, `scripts/render-chart system/istio-system`, `scripts/ci-check all`.

### Task 2: proof and docs

- [ ] `make dev tests='^(TestNodeRestart|TestNodePowerCut)$$'`, times recorded.
- [ ] `docs/reference/architecture/networking.md` and CLAUDE.md's watchdog lines; the deferred minor removed.
