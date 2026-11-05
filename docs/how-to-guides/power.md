# Power

The homelab comes back by itself after losing power, with nobody home. There is no UPS for now, so every outage is a sudden
cut of every node, and that is the main path. Nothing runs on the cluster to recover from one.

## After an outage

Set each node's BIOS "Restore on AC power loss" to **Last State**: a node running when the power went comes back, and one
turned off on purpose stays off, even through an outage.

The nodes boot, k3s and etcd form a cluster again, then Ceph and the mesh follow. On the [testbed](../installation/testbed.md)
this takes about 3 minutes from the VMs starting, with only Ceph and the mesh on it. The whole platform takes longer: on the
one-node sandbox a cut took from 5.5 to 23 minutes, the mesh's restart deadlock accounting for the slowest
(`docs/reference/architecture/networking.md`), so wait that long before going home to look. Hardware has its own numbers: the
testbed does not prove a real disk keeps what it acknowledged, so 12b prefers SSDs with power-loss protection and repeats the
cut before real data.

## Turning the homelab off

```sh
lab cluster shutdown --environment production
```

In order:

1. It writes the marker, ConfigMap `kube-system/cluster-shutdown`, naming what it is about to change.
2. It stops Argo CD's application controller, which would undo the next step.
3. It suspends every CronJob that is not already suspended, except those labelled `homelab/power-keep: "true"` (the mesh's
   `istio-cni-watchdog`).
4. It waits for running Jobs, at most `--job-wait` (5 minutes), and names those it left.
5. It sets Ceph's `noout`.
6. It powers off every node at once, without draining.

Velero's `Schedule` is left alone: a backup the shutdown cuts ends `Failed` and the next one runs.

A node's poweroff can hang: one holding a Ceph volume waits for OSDs that are gone. A unit started with the poweroff
(`poweroff-deadline`) cuts its power after 180 seconds without syncing. For what was left to write that is a sudden cut, which the
power tests prove survivable. On the testbed a node with no volume is off in 20 to 90 seconds.

Running it again after a partial run finishes the job and keeps what the first run recorded. If it fails with the nodes up, it says
to run `lab cluster startup`.

## Turning it on

Power on the nodes (Wake-on-LAN or the button). Nobody needs to run anything: the unit `cluster-startup` runs on every node at boot.

- No marker: it does nothing, as after a sudden cut.
- A marker, with most of its nodes still on the boot it recorded: it does nothing yet, so one node rebooting during a shutdown does
  not resume what is being stopped.
- A marker, with most of its nodes on a new boot: it waits for the API, resumes the CronJobs the marker names and starts Argo CD's
  controller. It then takes `noout` off and removes the marker.

To run it by hand and wait for it, whatever the boot IDs say:

```sh
lab cluster startup --environment production
```

## A node that does not return

Two nodes are enough. CronJobs and Argo CD come back without the third, so backups and rotation do not stay off.

`noout` stays until every OSD answers, or for 30 minutes from startup's first run. After that Ceph copies what the dead node held
rather than staying short of a replica. The marker goes last, so it lasts as long as `noout`.

## When the marker stays

The alert `ClusterShutdownMarkerStuck` fires when the marker is over two hours old: what the shutdown stopped may still be stopped.

```sh
ssh root@<node> journalctl -u cluster-startup
kubectl -n kube-system get configmap cluster-shutdown -o yaml
lab cluster startup --environment production
```

## With a UPS

A separate step, when a UPS is bought. An ordered shutdown leaves the last state "off", so Last State would never bring the nodes
back. That step sets the BIOS to **Power On** and has NUT tell the UPS to cut and restore its output (`shutdown.return`), which also
covers the power returning during a shutdown. The cost: a node turned off on purpose comes back after an outage. Choose a UPS that
NUT drives with that command.
