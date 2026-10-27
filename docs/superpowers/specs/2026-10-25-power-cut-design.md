# Step 11c: what a sudden power cut does to the sandbox

Status: designed in chat 2026-10-25; spike run and decisions taken the same day. Branch: `feat/power-cut` from `dev` (after step 11b's PR #54).

## Goal

Find out what a sudden power cut does, rather than require that the cluster survives one. The answers decide what to
fix, what to accept and document, and whether a UPS (step 12) is needed:

- does the cluster come back by itself, and how long does it take;
- what is left stuck, and what a person must do;
- what data is lost.

A power cut is a disaster, rarer than a planned shutdown. Losing what was in flight at the cut is acceptable; what
matters is getting back to a working state with as little loss as possible.

`TestNodeRestart` already proves a graceful restart (`docker restart`). This step looks at the hard case on the
one-node sandbox. The multi-node case (etcd's quorum, Ceph, CloudNativePG's failover, kube-vip, real disks) is
step 12's, in staging and on the hardware.

## How (the user's decisions, in chat)

1. **A spike first, on the sandbox, without a fixed test.** Cut the power (`docker kill` of the k3d node's container:
   SIGKILL, no flush, no graceful stop), start it again, and watch. One cut per run; no second cut during the
   recovery.
2. **Situations, one cut each:**
   - idle;
   - while writing (a loop creating Forgejo issues, ntfy messages, OpenBao secrets, Kanidm people);
   - while an off-site backup runs (Velero's data mover copying);
   - while a secret's rotation runs (`lab secrets rotate`, cut between its phases).
3. **For each, record:** whether it recovered by itself and how long it took (the `restartBlockers` checks of
   `TestNodeRestart`: ztunnel Ready, pods running and redirected, apps answering as before, every Application Synced and
   Healthy); what stayed stuck and what was done by hand; which data written before the cut is missing (separating
   what was acknowledged seconds before the cut from what settled long before).
4. **Report to the user, item by item, with a proposal:** fix (code, with a test), accept (documented), or a reason
   for a UPS. The user decides each.
5. **Then act on the decisions:** the fixes with tests (e.g. `lab secrets force-unlock <name> <holder>`, like `tofu
   force-unlock LOCK_ID`, only if a rotation's lock left by a killed run turns out to need it); a fixed
   `TestNodePowerCut` (alone, after `TestNodeRestart`) only for what the user decides must hold; the UPS conclusion in
   step 12's roadmap line.

## Not in this step

- A second cut during the recovery.
- Several nodes, Ceph, etcd's quorum, CloudNativePG's failover, kube-vip, the order network gear comes back, real
  disks without power-loss protection: step 12.
- An ordered shutdown command and the UPS itself: step 12.

## Record

Findings go in the plan's ledger as the spike runs, then into an "## Findings" section of this spec with the user's
decision for each, before any code.

## Findings (spike, 2026-10-25, the sandbox built from step 11b)

Four cuts (`docker kill`, then `docker start`), one situation each:

| Cut | Recovered by itself | Time | What stayed |
|---|---|---|---|
| Idle | yes | ~330 s | nothing |
| While writing (Forgejo issues on PostgreSQL, ntfy messages on SQLite, ~3 acknowledged writes a second) | yes | ~313 s | nothing lost: Forgejo 80/80, ntfy 65/65, the last acknowledged about 4 s before the cut |
| While an off-site backup ran (data movers in flight) | yes | ~331 s | Velero marked the backup Failed and its DataUploads Canceled; a new backup completed; prune deleted the failed one once a newer one completed; **a VolumeSnapshot Velero made for the failed backup stayed, owned by nothing** |
| While a rotation ran (`ntfy.alerts-token`, cut after its switch began) | yes | ~325 s | the workstation's run failed ("apiserver not ready") and **its lock release failed silently**, so the Lease held for its full duration, **2100 s (35 minutes)**; then the rerun resumed from the recorded phase and finished, with no third value |

Measurement: right after the restart the API server still reports the pods' states from before the cut, so Argo CD's
Healthy alone is a false recovery. Recovery is the node Ready after the cut, no pod Unknown or not ready, and the apps
answering through the Gateway.

## Decisions (the user's, after the findings)

1. **Accepted:** the cluster comes back by itself in about five and a half minutes, and acknowledged writes survive.
2. **Fixed: orphaned snapshots.** prune also deletes the VolumeSnapshots Velero labelled with a backup that no longer
   exists, with a test.
3. **Fixed: a dead run's rotation lock.** `lab secrets force-unlock <name> <holder>` (like `tofu force-unlock LOCK_ID`:
   the holder the "being rotated by" error prints; it asks first, `--yes` skips), and the lock's release retries
   while the API server comes back, so a run that survives the cut usually frees its own lock. Both with tests.
4. **A fixed test, `TestNodePowerCut`:** alone after `TestNodeRestart`, one cut while idle, recovery measured as
   above within 20 minutes, and data written before the cut read back (a Forgejo issue, an ntfy message, an OpenBao
   secret).
5. **The UPS:** on one node a power cut breaks nothing that matters once 2 and 3 are fixed, so a UPS is not needed for
   correctness; it would save the five-minute outage and spare real disks. The decision waits for step 12's staging
   cut of all three VMs (etcd's quorum, Ceph, CloudNativePG's failover), recorded in step 12's roadmap line.
