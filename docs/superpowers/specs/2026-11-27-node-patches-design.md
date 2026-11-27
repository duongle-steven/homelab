# G11: the nodes' packages kept current, and their store checked

The threat model's G11 (`docs/superpowers/specs/2026-11-14-threat-model-design.md`), its first part: the nodes' kernel
and packages come from the node flake's `flake.lock` (`infra/_modules/metal/flake`), which nothing has bumped since it
was made (2026-10-29). The nodes must not age silently. Its second part comes with it *(user, 2026-11-27)*: a weekly
check of the nodes' Nix store, which needs the same pieces (a metric file on each node, node-exporter's textfile
collector, an alert).

## Why

Vulnerabilities are found and exploited faster now that AI helps both *(user, 2026-11-27)*, so a node's packages may lag
by about two weeks at most, not a month.

## Decisions

- **Renovate bumps the node flake's lock** *(user, 2026-11-27)*, weekly, in a pull request of its own into `dev`: not
  inside the lock maintenance of every other lock file (the dev shell's flake, `go.sum`), so a node bump that breaks
  does not hold the rest back and the rest does not hold patches back. Renovate already opens the pull requests (10d) with
  the GitHub App's token, so the required `checks` run on them; a GitHub Actions workflow would open them with
  `GITHUB_TOKEN`, which starts no workflow.
- **Rolled out by a release, as today** *(user, 2026-11-27)*: the merge, the testbed's proof (the node flake is a gated
  path) and `just infra deploy production`, whose `lab cluster upgrade` drains and rebuilds one node at a time. No
  `system.autoUpgrade`: it would skip the testbed and the drain.
- **An alert at 14 days** *(user, 2026-11-27)*: `NodePackagesStale` when the nixpkgs a node runs is more than 14 days
  old. One missed weekly bump stays quiet, two alert. Age is the date of the locked nixpkgs the node was built from, not
  the time since its last rebuild, so redeploying an old lock does not silence it. It measures how far the nodes trail
  their channel, not missing patches: `nixos-26.05` moves almost daily, so every weekly bump changes it, and a patch
  reaches the nodes only by following the channel. A channel stalled for 14 days (Hydra stuck) alerts falsely; the
  alert's description says to check status.nixos.org first. Matching packages to CVEs (`vulnix`) is not this step: it
  reports too many false matches to alert on.
- **The store checked weekly** *(user, 2026-11-27, from the threat model's G11)*: `nix-store --verify --check-contents`
  on each node, alerting on a failure and on no recent success.
- **`clamd` gets its own step after 12b-3** *(user, 2026-11-27)*: it scans OpenCloud's uploads, paperless's consume and
  Transmission's downloads, which production runs only from 12b-3. The Roadmap and the threat model's order say so.

## Design *(Claude's calls)*

- `renovate.json5`: a rule that gives the lock maintenance of `infra/_modules/metal/flake/flake.lock` its own branch
  and pull request, Mondays like the rest. It takes no `sandbox` label: the sandbox runs no node.
- The nodes (`infra/_modules/metal`): a file `/var/lib/node-exporter/nixpkgs.prom` written at activation, holding
  `node_nixpkgs_last_modified_seconds` (nixpkgs' `lastModified`, known at build time) and, for the k3s package, the
  same for `nixpkgs-unstable`.
- `system/monitoring-system`: node-exporter's textfile collector reads that directory (through its host root mount),
  and a `PrometheusRule` `node-patches` alerts `NodePackagesStale` (`time() - node_nixpkgs_last_modified_seconds > 14 *
  86400`, for an hour, `warning`), with what to do: merge Renovate's pull request and release.
- The store check (`infra/_modules/metal`): a systemd service and weekly timer running `nix-store --verify
  --check-contents`, staggered per node (a `RandomizedDelaySec` or an offset by the node's place in `hosts.json`) and at
  `IOSchedulingClass=idle` with `Nice=19` (it reads the whole store, on etcd's disk, and `homelab-1`'s disk is the slow
  one), writing `/var/lib/node-exporter/nix-store-verify.prom`: `node_nix_store_verify_success` (1 or 0) and
  `node_nix_store_verify_last_run_seconds`. Alerts in the same `PrometheusRule`: `NodeStoreVerifyFailed` (a failure,
  `warning`: a corrupted or changed store path; the description says to run it by hand and read its output, `nix-store
  --repair-path` or a reinstall) and `NodeStoreVerifyMissing` (no run for 9 days).
- *(the final review, 2026-11-27)* Changed after the merge: the nodes' NVMe disks run scheduler `none`, where
  `IOSchedulingClass=idle` does nothing, so the check's reads are capped at 40 MB/s (`IOReadBandwidthMax` on
  `/nix/store`, cgroup `io.max`); the timer has no catch-up (`Persistent`, `RandomizedDelaySec` dropped: after a power
  cut the three nodes would check at once while Ceph and etcd recover), so `NodeStoreVerifyMissing` waits 15 days, one
  missed Sunday allowed; and lock file maintenance's window became `before 7am on monday`, since `before 5am` never held
  Renovate's 05:00 run.
- The three alerts also fire for a Ready node that has no such metric at all (joined on the node's name), so a node whose
  file was never written is not quiet. They are on in production only: the sandbox's k3d node is no NixOS node and
  would always alert (`values-local.yaml` turns them off; the testbed runs no monitoring).
- Proven: offline, the rule renders and Renovate's config validates; on the testbed, a spec reads the metric from
  node-exporter's file on every node (the testbed runs no monitoring), and the store check run once on a node and its metrics read; once, Renovate run locally in dry-run against this
  repository shows the node lock's own branch and that its image updates `flake.lock` (Renovate has never run for real:
  its first run otherwise comes with 12b-3).
- Docs: `docs/how-to-guides/renovate.md` (the node pull request and what a merge means), the threat model's G11, the
  alert in the alerts guide.

## Approaches

- **Chosen: Renovate**, separate pull request, release by hand, alert at 14 days.
- GitHub Actions weekly `nix flake update`: works before 12b-3, but its pull request starts no check unless it holds the
  App's key too, a second place for it.
- `system.autoUpgrade` on the nodes: fastest, but past the testbed and the drain.
- A recipe run by hand plus the alert: simplest, but every bump waits for someone to remember.

## Non-goals

- The shared `clamd`: its own step after 12b-3 (above).
- The store check as tamper-proofing: an attacker with root on a node can change the check or its result; it finds
  corruption and changes made without hiding, which is what it is for (the threat model's G11).
- Patching between releases, or releases on a schedule.
- k3s minors: the nodes pin `k3s_1_37`, so a bump brings its patches only; a minor is its own change (one at a time).

## Accepted

- Until 12b-3 Renovate does not run (it runs only in production's cluster), and the alert needs production's monitoring:
  the gap is a few steps long. The kernel follows `linuxPackages_latest`, so a bump can bring a new kernel major.
- Each weekly bump costs a testbed proof (about 1h45) at release.
