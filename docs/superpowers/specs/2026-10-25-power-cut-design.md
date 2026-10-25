# Step 11c: what a sudden power cut does to the sandbox

Status: designed in chat 2026-10-25. Branch: `feat/power-cut` from `dev` (after step 11b's PR #54).

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
