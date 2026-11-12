# Step 12b-2: production's platform, in stages

Status: designed in chat 2026-11-12. Decisions marked *(user)* were made in chat; *(Claude's call)* marks the builder's own,
open to review. Builds on 12b-1 (`docs/superpowers/specs/2026-11-11-production-nodes-design.md`): three NixOS k3s servers
on the HP mini PCs, nothing from Argo CD yet.

## Intent

Bring production's platform up on the hardware before any app, so each layer is checked on its own:

- Ceph on the nodes' `rook-data` partitions;
- the mesh, the Gateways, OpenBao, Kanidm, Forgejo, monitoring and logs;
- backups to R2.

Then repeat the power cut with real Ceph-backed data, before the apps and their data arrive in 12b-3.

## Goals

- **A stage switch for a new cluster's first deploy** *(user: option B)*. `infra/production/argocd` writes a stage on Argo
  CD's cluster Secret (the GitOps Bridge pattern of 14g). With `platform`, the `apps` ApplicationSet generates nothing,
  while `root` deploys `system/*` and `platform/*`. With `all`, the default, everything deploys as today.
- **The first production deploy,** with the stage set to `platform`, from a release on `master` (`just infra deploy
  production`: `lab infra forgejo verify`, apply, `sync`, `push`, the bootstrap). Argo CD switches from the seed to
  `ops/homelab` on Forgejo.
- **Ceph healthy on the hardware:** three OSDs on `rook-data` (173.5 GB each), two replicas, and a volume written on one
  node read back on another.
- **The platform's specs pass on production:** what the sandbox proves, run against production where the spec can
  (Smoke for the platform's apps, Argo CD, the mesh, OpenBao, Kanidm SSO, logs, monitoring).
- **An alert on etcd's disk,** following 12b-1's finding (`homelab-1`'s disk, accepted and watched *(user)*). It fires
  when a member's WAL fsync p99 stays over 10 ms, or when the leader changes too often.
- **A power cut with data:** writes acknowledged before a cut of every node are intact after it, on Ceph. Forgejo's
  database, OpenBao's raft and a plain Ceph volume, like the sandbox's `TestNodePowerCut`.
- **Backups:** Velero to R2 runs, `lab backup enable` is run once, and a restore of one volume from R2 is proven.

## Non-goals

- Apps (`apps/*`): 12b-3, by switching the stage to `all`.
- Immich, the teaching app, a NAS, a second disk.
- A UPS.

## Anti-goals

- **Lowering the stage from `all` to `platform` on a cluster that runs apps.** The `apps` ApplicationSet would delete every
  Application, and with them their volumes. The bootstrap refuses it: the stage only moves forward.
- **Real data before the cut with data has passed.** The person's own data waits for 12b-3.
- **A routine deploy that needs the stage set.** Unset keeps the cluster's stage, a new cluster gets `all`, and only a
  person moves it.

## Decisions

### 1. The stage *(user: B; details Claude's call)*

- **The variable:** `HOMELAB_PRODUCTION_STAGE=platform|all` reaches `infra/production/argocd` as an input.
  `cluster-secret.sh` writes it on Secret `argocd/in-cluster` as the label `homelab/stage` (sandbox and testbed always
  `all`).
  - Unset, it keeps the label the cluster already carries; only a cluster with none gets `all`. So 12b-2's repeated deploys
    never move the stage by accident.
- **Generators:** whatever deploys an app requires `homelab/stage: all`:
  - the `apps` ApplicationSet's `clusters` generator;
  - `root`'s generator for `apps/*` directories (Matrix, the only app chart);
  - `system/public-scan`, whose targets are the public apps.

  `root`'s other directories keep their selector.
- **New clusters only:** a label added to a selector would, on an existing cluster, delete its apps until the Secret
  carries it (CLAUDE.md's three-release rule). Production has no apps yet, and the sandbox's and testbed's bootstrap
  applies the Secret before the ApplicationSets. The plan proves the order on the sandbox, whose apps must survive the
  change.
- **Forward only:** the bootstrap reads the label already on the cluster and refuses `platform` where it is `all`.

### 2. The release path

`lab infra forgejo verify` deploys only `origin/master` with `checks` passed and its pull request's `sandbox` passed, plus
the testbed's proof mark (`release_proof: testbed`). So 12b-2 runs in this order:

1. The branch (stage switch, etcd alert, specs).
2. Its PR into `dev`.
3. A `dev` → `master` release: a fresh sandbox, and `just testbed` for the mark.
4. The deploy from `master`.

### 3. What the first deploy needs from the person

These come from the before-production list; the plan's install task asks for each:

- **Cloudflare:**
  - check that the state on R2 is complete;
  - delete the local unencrypted copies (`.state/production/cloudflare/terraform.tfstate` and `.pre-r2`);
  - rotate the Cloudflare tokens they held (`lab secrets rotate` for OpenTofu's entries).
- **The GitHub App's key** for Renovate (`HOMELAB_GITHUB_APP_KEY`). Without it, Renovate waits for its Secret, which is
  harmless.
- **Telegram's bot**, optional: Alertmanager runs without it.
- **The state passphrase** (`~/.config/homelab/state.env`), `cf auth login`, and the production SSH key in the agent.

### 4. The etcd alert *(Claude's call)*

Prometheus already scrapes etcd on every node (job `kube-etcd`, port 2381). The rules go beside the other node alerts in
`system/monitoring-system`, warning severity:

- **`EtcdSlowDisk`:** `histogram_quantile(0.99, rate(etcd_disk_wal_fsync_duration_seconds_bucket[10m])) > 0.010` for 1 hour,
  per instance.
- **`EtcdLeaderChanges`:** more than 3 leader changes in an hour.

They tell when 12b-1's accepted disk must be fixed, by CLAUDE.md's options.

### 5. The cut with data *(Claude's call)*

A production spec labelled `hardware-proof`, like 12b-1's `ProductionPowerCut`.

- **Before the cut, it writes and records:**
  - a marker in OpenBao;
  - a commit pushed to a scratch repository in Forgejo (its database on CloudNativePG, three instances on Ceph);
  - a file on a scratch Ceph volume, written with `sync`.
- **The person cuts the power strip** (nodes and router) and switches it back on.
- **After the cut, it reads each back.** Recovery is measured, as in 12a-3 and 12b-1: every node Ready, every pod ready, the
  data intact.

### 6. Public parts in the platform stage *(Claude's call)*

- **cloudflared and external-dns** deploy in 12b-2 with nothing behind them: their public targets are apps (blog,
  excalidraw, Element, ntfy), which come in 12b-3.
- **`public-scan`** comes with the apps (decision 1): with no target it would only report failed scans.
- **Alerts:** ntfy's path comes with the apps too, so until 12b-3 alerts reach Telegram (if set) and Alertmanager's page.

### 7. Side effects while the platform is brought up *(Claude's call)*

- **Renovate:** its GitHub App key is not given until 12b-3. Renovate waits for its Secret, harmlessly, and no daily
  pull request labelled `sandbox` starts a CI sandbox meanwhile.
- **kured: removed** *(found while building, 2026-11-12)*. Its sentinel was `needs-restarting --reboothint`, a RHEL
  tool NixOS lacks: the shell's 127, negated by `!`, says "reboot needed", so kured would reboot the nodes in turn
  forever. NixOS nodes change only through `lab cluster upgrade`, which already reboots when the kernel, initrd or
  modules change, drains first and waits for Ceph between nodes, which kured does not. Upstream keeps kured, with a
  NixOS sentinel comparing `/run/booted-system` to the system profile, because it rebuilds nodes by hand; this fork
  automates that rebuild. Bring kured back, with upstream's sentinel and a night window, only if nodes ever update
  outside `lab cluster upgrade` (`system.autoUpgrade`).
- **The rotation CronJob:** stays suspended until a release builds lab's image, as today.

### 8. Accounts and test data on production *(Claude's call)*

- **No test accounts or test clients on production.** The SSO specs need the sandbox-only `smoke` client and the
  `dev-*` accounts, with known credentials. On production the person signs in to Grafana, Argo CD and Forgejo with their
  own account, made by `scripts/onboard-user`.
- **The cut's scratch data** (a Forgejo repository, an OpenBao marker, a Ceph volume) is deleted by the spec once read
  back.
- **The cut runs before `lab backup enable`**, so none of it reaches R2.
- **The same cluster carries on:** 12b-1's install continues into 12b-3, with no reinstall.

### 9. Fixes found while bringing the platform up *(user)*

- Every fix goes through a pull request into `dev` and a `dev` → `master` release with the sandbox's check (the ruleset
  and `verify` require it). Fixes are batched into releases to save CI time.
- The testbed's mark is needed only when a fix touches `infra/testbed/release-paths.yaml`'s paths or the testbed's
  components; `verify` reuses an earlier mark otherwise.
- `--skip-proof=<reason>` only for a fix the testbed's VMs cannot judge, such as a Ceph OSD on the real NVMe disks.
  It is recorded as `production/skipped`.

## Proofs

- **The stage, offline:**
  - with `platform`, the rendered `apps` ApplicationSet selects no cluster;
  - with `all` (and by default), as today;
  - the bootstrap refuses `all` → `platform`.
- **The stage on the sandbox:** `just dev` passes, and the sandbox's apps survive the selector change.
- **The release:** the `sandbox` check, and `just testbed`'s mark.
- **On production:**
  - Argo CD synced;
  - Ceph `HEALTH_OK` with three OSDs, and the cross-node volume;
  - the platform's specs (Smoke for the platform's apps, mesh, OpenBao, SSO, logs, monitoring);
  - Velero's backup to R2 and a restore;
  - the etcd alerts loaded;
  - the cut with data.

## Docs

- `docs/installation/production/deployment.md`: the stage, and the first deploy's checklist.
- `CLAUDE.md`:
  - 12b-2 done;
  - the before-production items it settles (Ceph, OpenBao, Forgejo on Ceph, Velero to R2, Loki and Alloy on the nodes,
    Argo CD's switch to Forgejo, the mesh on the nodes, the cut with data), each checked against the list.
