# Step 12a-3: an ordered shutdown, startup, and every node losing power at once

Status: designed in chat 2026-11-03/04. Builds on 12a-1 and 12a-2 (`docs/superpowers/specs/2026-11-02-testbed-design.md`,
`docs/installation/testbed.md`): three NixOS k3s VMs, Argo CD, layers 1 and 2 (rook-ceph, cert-manager, Istio). It replaces
the "Shutdown and power cut" section of `docs/superpowers/specs/2026-10-31-staging-design.md`, written when staging was to
run most of the platform. Decisions marked *(user)* were made in chat; *(Claude's call)* marks the builder's own, open to
review.

## Intent

The homelab must come back by itself after losing power, with nothing it acknowledged writing lost, and nobody at home.
Production has no UPS for now and may get one later *(user)*, so every outage is a sudden cut of every node: that is the main
path. An ordered shutdown is for when someone chooses to stop the homelab (maintenance, moving it); a UPS will reuse it later.

## Goals

- Every node cut at once comes back by itself: etcd, the API at its VIP, Ceph, the mesh, the Gateway at its VIP, with data
  acknowledged on Ceph intact. Measured: how long it takes.
- `lab cluster shutdown` stops the cluster in order and turns the nodes off; they stay off until someone starts them.
- When nodes boot after an ordered shutdown, startup runs by itself and undoes it *(user)*; `lab cluster startup` runs the
  same thing by hand and waits for it.
- One measurement of CloudNativePG's three instances through the same cut.

## Non-goals

- **NUT and a UPS: a separate step, when a UPS is bought** *(user)*. `dummy-ups` would only prove a few lines of configuration,
  not a driver or a real UPS.
- BIOS settings, Wake-on-LAN and disks on hardware: step 12b.
- Forgejo and OpenBao on the testbed *(user)*. `TestNodePowerCut` proves on the sandbox that they keep acknowledged writes
  through a sudden cut; what several nodes add beneath them is Ceph, which this step tests directly. OpenBao's raft and Forgejo
  on Ceph first run on hardware and stay on the before-production list.
- healthchecks.io: it is still under Ideas, so there is nothing to pause. Whoever adds it adds that step.

## Anti-goals

- A startup that waits for something a suspended component would have fixed, and so never finishes (see the watchdog below).
- Resuming a CronJob that was suspended on purpose before the shutdown (production's rotation CronJob is, until the first
  release).
- A docs line that lets someone think the testbed proved real disks survive a power cut.
- Node health stuck behind a person: backups and rotation silently off after an outage because nobody ran a command.

## What differs from a real cut

`virsh destroy` kills QEMU. The VMs use `cache=unsafe`, so what the guest handed to QEMU survives in WSL's page cache, and a
guest's flush is not forced to any disk. The test proves the software recovers and that writes the guest never flushed do not
count as acknowledged. It does **not** prove that a real disk keeps what it acknowledged. Without a UPS that risk sits entirely
in the hardware: `docs/installation/testbed.md` and `CLAUDE.md`'s before-production list say so, and 12b prefers SSDs with
power-loss protection and repeats the cut on hardware before real data *(discussed 2026-11-04)*.

## `lab cluster shutdown`

Beside `lab cluster upgrade`, same flags (`--environment`, `--flake`, a timeout), reaching the nodes the same way. In order:

1. Write the marker: a ConfigMap (`kube-system`, name left to the builder) listing what this shutdown changes, before changing
   it, so a shutdown cut halfway still gets undone. It also records each node's boot ID, which startup needs below, and the
   controller's replicas.
2. Stop Argo CD's application controller (scale to zero). Applications self-heal, so it would undo the next step at once; Argo
   CD does not manage itself, so it stays stopped *(Claude's call)*.
3. Suspend every CronJob that is not already suspended, recording its name in the marker, except those labelled to keep running
   (below).
4. Wait for running Jobs, at most 5 minutes by default (a flag), then go on and name the Jobs it left *(Claude's call: a bounded
   wait, because a sudden cut is already proven survivable, and a UPS will have only 10-15 minutes)*.
5. Ceph `noout`, when Ceph exists.
6. Power off every node at about the same time, not one by one: losing them singly would make etcd and Ceph react to each loss.
   No drain. A node whose `poweroff` hangs (a volume's unmount stalling once the OSDs are gone) is forced off by a bounded systemd
   timeout, measured on the testbed: nobody is home to press the button, and a sudden cut is already proven survivable *(Claude's call)*.

Velero's `Schedule` is left alone and its Backups are not waited for: a backup cut by the shutdown ends `Failed` and the next one
runs, as 11c proved, and an off-site copy can outlast any wait worth having *(Claude's call)*.

It is idempotent: a second run after a partial one finishes the job and never records as "suspended by shutdown" what the first
run suspended twice over.

### The watchdog keeps running

`istio-system/istio-cni-watchdog` is a CronJob that breaks the mesh's deadlock after a node restart (`CLAUDE.md`). Suspending it
could leave the mesh deadlocked while startup waits. It carries a label that `shutdown` skips *(Claude's call)*.

## Startup

**One implementation, on the nodes** *(Claude's call)*: a small script in the node flake (`writeShellApplication`, using k3s's
kubectl and the node's `/etc/rancher/k3s/k3s.yaml`), run by a systemd unit at boot on every node, and run over SSH by `lab cluster
startup`. `lab` is not on the nodes, and packaging it there would tie the flake to `cli/`'s Go module for a few kubectl calls.

- No marker: do nothing and exit (a sudden cut recovers by itself, needs nothing undone).
- Marker, but a majority of its nodes still on the boot it recorded: do nothing yet. One node rebooting during `shutdown`'s wait must
  not resume what is being stopped; a cut halfway through a shutdown reboots them all, so it gets undone *(Claude's call)*. A
  `shutdown` that fails with the nodes up says to run `lab cluster startup`, which undoes it whatever the boot IDs say.
- Marker, and a majority of its nodes on a new boot: wait for the API (retrying with no limit: the other nodes may take any time to
  come back), then resume the CronJobs the marker names and scale Argo CD's controller back up. It does **not** wait for every node: a
  node that never returns must not leave backups and rotation off *(user)*.
- `noout` comes off when every OSD is back, or after 30 minutes at most, so Ceph copies what a dead node held instead of staying short
  of a replica forever *(user)*. The marker goes last, after `noout`. Every step is idempotent, and three nodes may run it at once.
- A marker that outlives its welcome raises an alert, so someone learns a node did not return or startup failed *(user)*. Rendered
  only on the testbed, which runs no monitoring; the sandbox's `TestMonitoring` style can prove the rule loads.
- CloudNativePG, when installed: report whether every `Cluster` is healthy, without holding the rest back. Unit-tested only here; the
  testbed does not list it *(user)*.
- `lab cluster startup` runs the script on the first node that answers and fails with a timeout, naming what it waits for.

A node booting with the marker does not wait for the Ceph that a marker-less boot would need nothing from; the unit must never hold up
boot or k3s (ordered after `k3s.service`, no `Before=` or `RequiredBy=` on anything it needs).

## Power on after an outage

*(user: without a UPS, Last State is right; the UPS step changes it.)*

- **No UPS (now):** BIOS "Restore on AC power loss" = Last State. Running when the power went: comes back. Turned off on purpose:
  stays off, even through an outage.
- **With a UPS (its own step):** an ordered shutdown leaves the last state "off", so Last State would never bring the nodes back.
  That step sets "Power On" and has NUT tell the UPS to cut and restore its output (`shutdown.return`), which also covers the power
  returning during a shutdown. The cost: a node turned off on purpose comes back after an outage. Choose a UPS that NUT drives
  with that command.

The roadmap in `CLAUDE.md` changes accordingly. None of this can run on VMs.

## Tests

On the testbed, from the main checkout, alone and never in `make testbed`'s run: they wreck its availability. A new target
(`make testbed-power`) runs them on a healthy testbed. They do not count towards the release mark *(Claude's call: the mark says a
release deployed and passed; these run when `cluster shutdown|startup`, the node profile or the recovery path change, and always before
12b)*. `TestTestbed*` is `make testbed`'s filter, so they need another prefix.

A write probe, shared: a small pod on a Ceph PVC appends numbered records, `fdatasync`s each, and prints the number afterwards. The test
follows its log from outside the VMs, so what it read is what was acknowledged. After recovery every number it read is in the file, in
order. The probe's PVC, its namespace and its mesh label are the test's; it is removed at the end.

**Every node cut (`virsh destroy` of all three, in one go, while the probe writes):** start them, then measure and require, with no
command run on the cluster:

- the API at its VIP, every node on a new boot and Ready (the API first reports what it knew before: as `TestNodePowerCut`, a new
  `bootID` is the proof)
- Ceph `HEALTH_OK`, mons and OSDs running
- the mesh between two nodes and the Gateway at its VIP, as `TestTestbedMesh` checks; **this is the check that would catch 12a-2's
  kube-vip finding** (its lease held with the VIP not answering), so a failure there is reported as that finding, not waited through
- the probe's data intact, and the probe able to write again
- no etcd member lost, no marker involved

**If the Gateway's address does not return (the kube-vip finding):** one bounded investigation, about half a day of machine time. Fixed: the
fix ships in this branch. Not fixed: the work stops and asks, with the logs. It is neither recorded as passing nor moved to 12b without the
user *(user)*, since coming back with nobody home is this step's whole point.

Limit: 30 minutes *(Claude's call, from 12a-2's install times; the first run sets the real number, recorded in the docs)*. Then do it again
during a Ceph-heavy moment only if the first run shows a reason.

**Ordered shutdown and startup:** before it, a fixture CronJob running, a fixture CronJob suspended on purpose, and a long Job. Run `lab
cluster shutdown`. Check the marker's contents before the nodes go, then every VM `shut off` and staying so. Start them, without calling `lab`:
the units undo it. Require: the running CronJob resumed, the suspended one still suspended, the watchdog never suspended, `noout` off, Argo CD's
controller back and its Applications Synced, the marker gone, the probe's data intact. Then a second run: `lab cluster startup` by hand with no
marker is a no-op that succeeds.

**A node that does not return:** shut down in order, start only two VMs. Require the CronJob resumed and Argo CD's controller back with the third
still off, and `noout` off after its limit. Start the third last, so the testbed ends healthy.

Unit tests, in `cli/internal/cluster` beside `upgrade_test.go`'s fakes: the order of shutdown, the marker written before anything changes, skipping
what is suspended and what is labelled, the Job wait's limit, idempotence. The script gets a test through a fake kubectl in `test/`, offline and wired
into `scripts/ci-check`'s `offline_tests`.

## CloudNativePG, one measurement *(user)*

Once, not on the list and not in the mark: with layer 2 stable, install `platform/cloudnative-pg` by hand and its three instances, write through
the primary, cut every node, and check that a primary returns with the write. **Also log I/O and memory inside each guest**, alongside each node's
vmstat: that answers what 12a-2 could not, why layer 3 lost its leases at 4 vCPUs with CPU idle. The result and the numbers go in
`docs/installation/testbed.md` as one measurement, whichever way it goes. A failure does not block this step: it moves to 12b's list with its
numbers. The bar is what the measurement reports, not a pass or fail of this step *(discussed 2026-11-03)*. It runs at 2 vCPUs, what this
workstation ships; 4 only if that run points at CPU *(Claude's call)*.

## Constraints

- `CLAUDE.md`'s rules: `test/scopes.yaml` for what `make dev` would run (these tests are not the sandbox's), `deferred-minors.md`, the before-production list.
- The node profile is a gated path (`infra/_modules/metal/**`), and `cli/internal/cluster/**` too: this branch's own change needs `make testbed`'s mark again.
- Never beside the sandbox, 30 GB free on `C:`, WSL above 1.5 GB; a run stopped for low memory is not restarted without asking.
- From the main checkout only. The VMs are the testbed's: `virsh destroy` is safe there and never used on anything else.

## Left to the builder

- The marker's name and format, the label's name, how `noout` is set (the toolbox or the operator's pod), the probe's image.
- Whether the systemd unit needs a timer or a retry loop, as long as it survives the API being away for any length of time.
- The test's file and prefix, and how many of its waits to share with `TestTestbedMesh` and `TestTestbedCeph`.
- Where the roadmap names the UPS step (it runs when a UPS is bought).
