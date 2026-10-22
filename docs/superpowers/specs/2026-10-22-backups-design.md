# Step 11a: backups

Status: design approved in chat 2026-10-22. Branch: `feat/backups` from `dev`.

## Goal

A cluster that is switched off, loses power or loses one disk or node comes back with its data. The step itself does
nothing for that case: Ceph's replicas already cover it. What 11a adds:

- **Mistakes:** a person deletes something or an app corrupts its data, and the volume can go back an hour.
- **Loss of every node, or new hardware:** the state that is not in git can be restored from off-site, by a command
  someone chooses to run.

Git rebuilds everything else. Argo CD recreates every resource from the repository, and OpenTofu's state (with
OpenBao's unseal key) is already on R2. Backups therefore hold **volume data only, never Kubernetes resources**.

## Context

- **Storage:**
  - Production: rook-ceph (`system/rook-ceph`, chart 1.13.5), pools with 2 replicas, default `VolumeSnapshotClass`es
    and a snapshot-controller already installed.
  - Sandbox: k3d's local-path provisioner, with no CSI snapshots.
- **VolSync:** `system/volsync-system` (chart 0.9.1) is installed but used by nothing. Namespaces get the label
  `volsync.backube/privileged-movers` (`system/argocd/values.yaml`). `docs/how-to-guides/backup-and-restore.md`
  still describes upstream `master`'s removed `make backup`.
- **Upstream `nixos`** (commit 481903b7, 2026-10-22) replaced VolSync with Velero
  (`docs/reference/architecture/decision-records.md` there):
  - `infra/_modules/backup` creates the R2 bucket `<prefix>-backup`, an account token that can write only that bucket,
    and a `random_password` for the repository, all with `prevent_destroy`.
  - Velero (chart 12.2.1, plugin `velero-plugin-for-aws` v1.14.4) runs with `deployNodeAgent`, CSI snapshot data
    movement and an hourly schedule of the whole cluster.
  - Their restore is manual: `backup_read_only` makes the location `ReadOnly` and disables the schedule, then
    `velero restore create`.
  - Their open items: OpenBao's data ("TODO: Add tested backups for secrets storage"), and databases that "may still
    need backup hooks".
- **R2's free tier** (account-wide, Standard class): 10 GB-month, 1 million Class A and 10 million Class B
  operations per month, egress free. The state bucket uses a few MB of it.
- **Deferred minors that point to step 11:**
  - #34: a partial restore makes two destinations of one generated secret drift.
  - 8b: losing `secrets/tfstate.json` creates a new WireGuard key.

## Decisions

### 1. Velero replaces VolSync

This follows upstream `nixos`. `system/volsync-system`, its AuthorizationPolicy, its `scopes.yaml` entry and the
`privileged-movers` label go away. Velero lives in `system/velero`, as Argo CD components do here (upstream installs
it from OpenTofu). It uses the latest chart and plugin, pinned. If the chart's app version lags Velero's latest
release, the image is pinned to the latest one.

### 2. Volumes only, never resources

Backups include PersistentVolumeClaims and their data. They include no Deployment, Secret, ConfigMap or custom
resource: Argo CD rebuilds those from git, and restoring them over Argo CD would fight it.

### 3. Three layers

| Layer | Where | Covers | Does not cover |
|---|---|---|---|
| Ceph replicas (existing) | the nodes' disks | power loss, sudden shutdown, one disk or node lost | deletion, corruption (copied to every replica) |
| Local snapshots (new) | Ceph, on the nodes | deletion, corruption, a failed upgrade: back one hour | losing several nodes or Ceph itself, new hardware |
| Off-site copies (new) | R2 bucket `homelab-production-backup` | losing every node, new hardware | losing R2 or the state passphrase |

- **Local snapshots:**
  - Ceph `VolumeSnapshot`s of every volume in groups A and B below, every hour, keeping 48 hours.
  - Taking and restoring one must work without the internet. A layer whose metadata lives only on R2 does not
    qualify.
- **Off-site copies:** Velero with Kopia (deduplicated, compressed, encrypted) to R2.

### 4. What goes off-site

Membership is declared next to each component: a label on its PVC or namespace. A new app gets no off-site copy unless
it says so.

| Group | Volumes | Off-site |
|---|---|---|
| A: automatic | OpenBao, Kanidm, Forgejo (`forgejo-data` and PostgreSQL `forgejo-db`), Woodpecker's server, ntfy, Matrix (Dendrite and its PostgreSQL), and every other app published on the internet that keeps data | a daily schedule, kept 30 days |
| B: on demand | Actual Budget, paperless, OpenCloud | only when someone runs the command (decision 7), kept 30 days |
| C: never | zot, Loki, ollama, jellyfin (one PVC holds its config, the *arr apps' config and the media; config without the media is worth nothing, and only losing Ceph loses it) | nothing; they are rebuilt or downloaded again |

Group C still gets local snapshots where that is cheap. That is the builder's call, given the disk space Ceph has.

### 5. Consistent copies

Each backup must be restorable, not just a file copy of a running database:

- **OpenBao** goes off-site as a raft snapshot (`bao operator raft snapshot save`), not as a copy of its data
  directory. The snapshot restores with the same unseal key, which survives in OpenTofu's state on R2.
- **PostgreSQL** (`forgejo-db`, Matrix): its volume goes the same way as every other volume. A crash-consistent
  snapshot is enough, because PostgreSQL replays its WAL (the log it writes before changing data) at start.
- **Forgejo's two volumes** (`forgejo-data`: repositories, attachments; `forgejo-db`: users, issues, pull requests,
  branches) are restored from the same backup run and snapshotted one right after the other. A few seconds between
  the two can leave them slightly apart; `forgejo doctor` repairs that.
- **No point-in-time recovery (CloudNativePG's barman-cloud backups), and none planned:** it restores a database to
  any second, but Forgejo's repositories live on `forgejo-data`, which only has the backup moments, and the two must
  match. So Forgejo could only go back to those moments anyway; point-in-time would buy an exact match instead of
  a `forgejo doctor` run, at the price of a second restore path. CloudNativePG stays as the database operator.
- **SQLite apps** (Kanidm, Forgejo in the sandbox, Woodpecker, ntfy): use the app's own online backup if it has
  one (Kanidm does). Otherwise accept a crash-consistent snapshot, which SQLite recovers from.
- **Restoring OpenBao and the apps:** they come from the same backup run. Their passwords have to match, which also
  settles deferred minor #34 for a full restore.

### 6. Credentials outside the cluster

- **The R2 bucket and its credentials:** a new OpenTofu unit `infra/production/backup` (upstream's
  `infra/_modules/backup`, with `prevent_destroy`) creates the bucket and a token that can write only that bucket.
- **The Kopia repository password:** the same unit generates it as a `random_password`. It must survive the loss of
  the cluster, so it cannot exist only in OpenBao, which is itself in the backup.
- **Into the cluster:** both reach Velero through `infra/production/secrets` → `infra-secrets` → OpenBao, as
  OpenTofu's other secrets do.
- **Network:** Velero's namespace gets egress 443 in production (`system/egress`).

### 7. Commands (in `lab`)

- **`lab backup push <app>`:** an on-demand off-site copy of one app (group B, or any group A app before a risky
  change).
  - Before uploading, it prints the volume's used size and the bucket's current size against R2's 10 GB free tier.
  - It asks for confirmation, then creates the Velero Backup and waits for it.
- **`lab backup restore`:** a whole-cluster restore after a fresh install, on a cluster Argo CD has already built from
  git.
  - The person picks the backup run. Its default is the newest one that completed.
  - Order: OpenBao first, from its raft snapshot. Then each group A volume, with that app's workloads stopped and its
    empty volume replaced. Then the apps start.
  - Group B apps are restored too when that run contains them.
- **`lab backup restore <app> [--from local|offsite] [--at <time>]`:** one app, from a local snapshot or an off-site
  copy.
- **Visibility:** a status listing of backups and snapshots is the builder's call. So are the flag names.

### 8. Restore never destroys the backups

- **Read-only mode:** `make infra env=production restore=1` puts Velero's location in `ReadOnly` and turns the
  schedules off, as upstream's `backup_read_only` does.
- **The mode must stick:** it stays on until the person clears it. A later `make infra` without the flag must not
  switch the schedules back on by accident. The builder chooses where that state lives.
- **Anti-goal: expiry must not eat the last good backups.** A cluster dead for more than 30 days would otherwise
  delete them through Velero's expiry the moment the location becomes writable again. Before switching back to
  normal, the restore procedure keeps the backup it restored from (a longer expiry, or another mechanism).
- **Before switching back:** the first new scheduled backup must not run before the restored data was checked. The
  docs say how to check it.

### 9. The sandbox proves the path

The sandbox has no R2 and no Ceph.

- **S3:** a small S3-compatible server in the sandbox, sandbox only, maintained upstream and pinned. MinIO's community
  edition is no longer maintained. Garage or similar is the builder's call.
- **Velero:** file-system backup through the node agent on local-path. The production CSI data-mover path is only
  rendered.
- **Forgejo on PostgreSQL:** the sandbox runs Forgejo on CloudNativePG as production does (instead of SQLite), so
  the test restores both of Forgejo's volumes. This removes "CloudNativePG and Forgejo on PostgreSQL were only
  rendered" from the production checklist. Measured 2026-10-22: about 4.7 GiB of the workstation's memory is free;
  CloudNativePG, one PostgreSQL instance, Velero, its node agent and the S3 server are estimated at 400 to 600 MiB
  at rest.
- **`TestBackup`:**
  1. It writes a marker into Forgejo (a repository with a commit, and an issue) and a key into OpenBao.
  2. It takes a backup, then deletes the app's volume and the key.
  3. `lab backup restore` brings back the repository, its commit, the issue and the key, and Forgejo works.
  4. It shows that `restore=1` leaves the bucket unchanged.
  5. It shows that `lab backup push` prints the size and asks before uploading.
- **Isolation:** the test must not disturb the other tests. A raft snapshot restore rolls back all of OpenBao, so it
  either runs alone, as `TestNodeRestart` does, or restores only what it wrote since the snapshot. The builder
  decides.
- **Wiring:** a `test/scopes.yaml` entry, the Argo CD lists for `system/velero`, and resource limits sized to the
  sandbox's free memory, measured before and after.
- **Production checklist** (CLAUDE.md), for what the sandbox cannot run: Ceph snapshots, the CSI data mover, R2,
  and the hourly local layer.

### 10. Docs

- **`docs/how-to-guides/backup-and-restore.md`** is rewritten:
  - the three layers and which failure each one covers;
  - groups A, B and C, and how to add an app to A or B;
  - checking R2 usage;
  - restoring one app;
  - restoring the whole cluster on new hardware, end to end: OpenTofu state, passphrase, `restore=1`,
    `lab backup restore`, checking, leaving read-only.
- **Other docs:** a decision record (Velero, volumes only), the roadmap line 11a in CLAUDE.md, the production
  checklist, and `secrets-management.md` (where the repository password lives).

## Non-goals

- Backing up Kubernetes resources, Argo CD's state or cert-manager's certificates.
- Automatic restore on a fresh install. Restore is always a command someone runs.
- Backing up group C, media, or OpenTofu's state (already on R2, encrypted).
- A second off-site provider. Upstream's "TODO migrate to Backblaze B2" stays an idea.
- Rotation of the backup credentials (step 11b) and node hardware: UPS and shutdown order (step 12).

## Anti-goals: a failure even if it works

- A restore, a schedule or an expiry that deletes the only good backup.
- A backup that cannot be restored without the cluster it came from, for example a key that exists only in OpenBao.
- OpenBao restored from one point in time and the apps from another.
- Uploading group B data to R2 without the person seeing its size first.

## Left to the builder

- Velero's exact settings, the local-snapshot mechanism (Velero without data movement, a snapshot scheduler or a
  CronJob) and how group membership is declared, all within decisions 3 and 4.
- The flags and output of the `lab backup` commands, and where the read-only state is kept.
- The sandbox's S3 server, and whether `TestBackup` runs alone.
- Velero's mesh policies (AuthorizationPolicy, the node agent's and data-mover pods' needs), its egress and its
  metrics.
