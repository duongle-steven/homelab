# Backup and restore

Git rebuilds every resource of the cluster: Argo CD applies it all from the repository, and OpenTofu's state (with
OpenBao's unseal key) is already on R2, encrypted. What git cannot rebuild is the data in the volumes, and that is
what backups hold, never Kubernetes resources.

## Three layers

| Layer | Where | Covers | Does not cover |
|---|---|---|---|
| Ceph's replicas | the nodes' disks | power loss, a sudden shutdown, one disk or node lost | a deletion or corruption (copied to every replica) |
| Hourly local snapshots, kept 48 hours | Ceph, on the nodes | a deletion, corruption, a failed upgrade: back one hour | losing several nodes or Ceph itself, new hardware |
| Off-site copies, the newest 30 kept | R2 bucket `homelab-production-backup` | losing every node, new hardware | losing R2 or the state passphrase |

- **Switching the nodes off or losing power** only restarts the cluster that exists: nothing is restored.
- **Local snapshots** are `VolumeSnapshot`s taken by the CronJob `velero/snapshots`. Taking them and restoring from
  them needs no internet.
- **Off-site copies** are Velero's (`system/velero`): CSI snapshots moved into a Kopia repository (deduplicated,
  compressed, encrypted with a password from OpenTofu) on R2.

## Which volumes

Every volume claim declares its group with the label `homelab/backup` (`scripts/check-backup-groups` fails the basic
checks otherwise):

| Group | Volumes | Off-site |
|---|---|---|
| `daily` | OpenBao (its hourly raft snapshot, `openbao-snapshots`), Kanidm, Forgejo (`forgejo-data` and PostgreSQL `forgejo-db`), Woodpecker's server, ntfy, Matrix (Dendrite's media and queues, its PostgreSQL) | every day at 02:00 |
| `manual` | Actual Budget, paperless, OpenCloud | only with `lab backup push <app>` |
| `none` | zot, Loki, ollama, jellyfin, everything else | never: rebuilt or downloaded again |

Both `daily` and `manual` get local snapshots.

To add a volume:

1. Label its claim (in an app file `persistence.<name>.labels`, or the chart's PVC labels).
2. Add it to `EXPECTED` in `scripts/check-backup-groups` (claims it does not name must be `none`).

A database's volume is copied as it is: PostgreSQL and SQLite recover from a crash-consistent snapshot. Forgejo's two
volumes come from the same run, snapshotted one right after the other. A few seconds between them can leave Forgejo
slightly behind its repositories, which `forgejo doctor check --all --fix` repairs.

## lab backup

Every command takes `--env production` (the default) or `--env sandbox`.

```sh
lab backup list                    # the off-site copies, newest first, and whether backups are on
lab backup enable                  # writable location, schedules on (asks first)
lab backup disable                 # read-only, schedules paused
lab backup push paperless          # one app off-site now: prints its size against R2's free tier, asks first
lab backup restore forgejo         # one app from its newest local snapshot (--at <RFC 3339>, --from offsite)
lab backup restore                 # the whole cluster after a fresh install (see below)
```

Without a terminal, `enable`, `push` and `restore` refuse unless given `--yes`.

### Read-only until enabled

Every new cluster starts with the location `ReadOnly` and the schedule paused: git declares both, and Argo CD
ignores the two fields afterwards. Velero then writes and deletes nothing, so a new cluster can never replace or prune
the backups it may need to restore from.

- **A brand-new homelab:** run `lab backup enable` once after the first deploy.
- **After a restore:** run it once the restored data was checked.
- **Retention by count:** the CronJob `velero/prune` keeps the newest 30 completed copies of the schedule and of each
  app. It runs only while the location is writable. Velero's own expiry is ten years, so a cluster that was down for
  months loses nothing.
- **A copy cut short** (a power cut while its data movers copied) ends `Failed`, and prune deletes it once a newer copy
  of its group completed. Velero may leave a `VolumeSnapshot` it made for that copy (labelled
  `velero.io/backup-name`): prune deletes every such snapshot whose backup no longer exists, in either mode, since no
  restore can need it.
- **The alert `VeleroNoRecentBackup`** (through ntfy) fires when no daily copy succeeded for 26 hours. That includes
  a cluster never enabled.

## Check the space on R2

R2's free tier is 10 GB-month, account-wide (the state bucket uses a few MB). `lab backup push` prints the app's
volumes and the bucket's current size against it before it uploads. Kopia deduplicates, so keeping 30 copies costs
little more than one copy plus what changed. Beyond 10 GB, R2 charges about $0.015 per GB-month.

## Restore one app

From the newest local snapshot, back one hour at most (no internet needed):

```sh
lab backup restore forgejo                                   # newest snapshot
lab backup restore forgejo --at 2026-10-22T14:00:00+07:00   # newest at or before that time
lab backup restore forgejo --from offsite                    # the newest off-site copy holding it (--backup <name>)
```

`lab` does the following:

1. Pauses Argo CD's automated sync of the app.
2. Stops its workloads. CloudNativePG clusters are hibernated with their reconciliation paused, and kaniop's Kanidm is
   stopped through its replicas.
3. Replaces the claims, giving back the owner references Velero drops.
4. Starts the app again.
5. Resumes the sync, then waits for Argo CD to report the app Synced and Healthy.

If a step fails, the app stays paused and stopped, and the error says so. Fix the cause and run the same command again.

After restoring Kanidm, `lab` deletes the Secrets kaniop writes only once (`kanidm-admin-passwords` and every
`*-kanidm-oauth2-credentials`), so kaniop writes them again from the restored data, and it restarts their readers.

## Restore the whole cluster (new hardware, every node lost)

1. Keep the state passphrase (`~/.config/homelab/state.env`). Without it, OpenTofu's state on R2 cannot be read, and
   with it OpenBao's unseal key, the Kopia password and the R2 credentials.
2. Build the nodes and run `make infra env=production`, as for a first install. Argo CD builds everything from git.
   Velero starts read-only and paused, so it syncs the backup list from R2 and touches nothing.
3. Check what is there: `lab backup list`.
4. Run `lab backup restore`. It takes the newest scheduled copy that completed before this cluster was created (or
   `--backup <name>`). It restores OpenBao first, from its raft snapshot, then every app with a daily volume, then each
   manual app from its newest push. Finally it refreshes every ExternalSecret and restarts every reader.
5. Run `make infra env=production` again. The restored Forgejo holds `ops/homelab` as it was at the backup, so Argo CD
   would otherwise run that older commit; this pushes `master` again.
6. Check the data:
   - sign in through Kanidm (passwords and passkeys come back);
   - open Forgejo's repositories and issues; if a repository looks behind, run
     `kubectl -n forgejo exec deploy/forgejo -- forgejo doctor check --all --fix`;
   - check that the other apps have their data;
   - confirm every Argo CD Application is Synced and Healthy.
7. Run `lab backup enable`. Daily copies start again, and the prune job keeps the newest 30.

## The sandbox

The sandbox runs the same path:
- csi-driver-host-path gives it CSI snapshots (`system/csi-hostpath`, its default StorageClass);
- an S3 server in namespace `velero` (versitygw) stands in for R2;
- `TestBackupRestore` backs up, loses a repository, its issue and an OpenBao key, and restores them, the whole
  cluster and then Forgejo alone.
