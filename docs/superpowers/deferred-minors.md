# Deferred minors

Review findings graded Minor and left open, collected from every PR (checked against the code on 2026-10-17; PRs #4
to #29 had theirs fixed in the PR itself or in the cleanup PR #29). Every PR adds the minors it defers here and
removes the lines it fixes. "When" names the step that must handle it, or "any" for a cleanup PR.

| From | Minor | When |
|---|---|---|
| #30 (7a) | `infra/_modules/k3d` destroy ignores a failed `k3d cluster delete` | any |
| #30 (7a) | `docs/installation/sandbox.md` says `make dev` recreates the proxy; it is recreated only when the node address changes | any |
| #30 (7a) | `infra/_modules/argocd/bootstrap.sh`: the Forgejo check and its `yq \| kubectl` pipes run without pipefail | any |
| #30 (7a) | `make -C infra check` does not run `tofu validate` per module | any |
| #30 (7a) | `scripts/get-status`, `argocd-admin-password`, `onboard-user` default to production's kubeconfig | any |
| #31 (7b) | Argo CD's `argocd-notifications-cm` keeps the chart default `argocdUrl: https://argocd.example.com` | any |
| #31 (7b) | Forgejo's `[server]` ConfigMap duplicates what the chart derives from the route host | any |
| #31 (7b) | `global.timezone` is read by no chart | any |
| #31 (7b) | The tests hard-code `*.127-0-0-1.nip.io` | 12 (staging) |
| #31 (7b) | The seed `sed` breaks on `&` or `\|` in `seed_repository`; `SEED_REPOSITORY` duplicates `global.seedRepository` | any |
| #31 (7b) | woodpecker's and zot's config changes do not roll their pods (the woodpecker chart passes `podAnnotations` without `tpl`; Secrets ESO refreshes never would); 11b's rotations restart them explicitly, other changes still need a restart by hand | any (Reloader) |
| #31 (7b) | `scripts/check-env-config`'s untemplated-global check knows only four keys | any |
| #32 (7c) | `.direnv/` and `site/` are not git-ignored | any |
| #34 (8a) | `TestSecretsAudience` matches the plugin's "invalid audience" message | any |
| 8b | `infra/_modules/secrets/wait-namespace.sh` names OpenBao and `platform/openbao` (copied) instead of `infra-secrets` | any |
| 8b | `apply-secrets.sh`'s kubectl errors are hidden (OpenTofu suppresses output when `MANIFEST` is sensitive) | any |
| 8b | Losing `secrets/tfstate.json` on R2 (or the state passphrase) silently creates a new WireGuard key, which `Replace` pushes over OpenBao's (11a's backups hold volumes, not OpenTofu's state; 11b never rotates it) | 13 |
| 8b | Removing an `infra-secrets` entry leaves its Secret in the namespace; the docs' removal steps omit `kubectl delete secret` | any |
| 8b | `TestNtfyWatchdogScheduled` does not prove that heartbeats replace one scheduled message | any |
| 8b | `InfoInhibitor` reaches phones: the Alertmanager config override drops the chart's `null` receiver | any |
| 8b | `external-dns/cloudflare#zone_id` is pushed but never read (no `--zone-id-filter`) | any |
| 8b | The 8b spec lists hashicorp/random, which the `secrets` module does not use | any |
| 9 | `docs/installation/sandbox.md` still says the sandbox has no Loki; CLAUDE.md's "production-only allows … only rendered" bullet still lists Loki | any |
| 9 | Production's first Alloy start reads every current pod log from the start: Loki refuses lines older than 7 days and may answer 429 to the burst (default `ingestion_rate_mb` 4) | 12 (Nodes) |
| 9 (chat) | `system/rook-ceph` pins Rook 1.13.5 (latest v1.21.0): upgrade when Ceph first runs | 12 (Nodes) |
| mesh restart | `cni-watchdog.sh` logs "no running istio-cni pod" when a rollout briefly shows two istio-cni pods on a node (it skips that minute, which is safe) | any |
| mesh restart | `TestIstioCNIWatchdog` never exercises the BusyBox `date -D` fallback offline (the host has GNU date; `TestNodeRestart` covers it in the image) | any |
| mesh restart | The mesh-restart spec's RED bullet says the test "fails at its 12-minute limit" without saying the limit was 12 then | any |
| 14a | `make dev tests=` with a single quote breaks the recipe's `eval` quoting | any |
| 14a | The test scopes spec's map table still lists `scripts/check-*`, `render-chart` and `cli/**` as Smoke only; `test/scopes.yaml` skips them | any |
| 14a | `lab test scope --check` covers only `components` globs: top-level files, `test/` non-test files, `metal/` and `cli/` are not checked (a new file there runs the whole suite) | any |
| 14a | `scope.SplitTerms` ignores escaped parentheses and an alternation sign inside a character class; no term uses them yet | any |
| dev shell cleanup | `metal/roles/pxe_server/templates/dnsmasq.conf.j2` uses `ansible.netcommon.ipmath`, which nixpkgs' Ansible does not ship (it moved to `ansible.utils`): `metal/` fails before and after the cleanup | 12 (replaces `metal/`) |
| 8e | `make dev-fresh` on the workstation hits the main run's 60-minute `go test` timeout (the first sync's waits count inside the tests) | any |
| 8e | Browser SSO tests flake on Kanidm's login page (OpenCloud, Grafana, paperless: "login page never loaded", `chrome-error`); a rerun passes | any |
| 10e | Woodpecker's server generates a temporary `WOODPECKER_GRPC_SECRET` at each start (agents sign in again with the agent secret; 11b's agent rotation restarts the server anyway) | any |
| 10e | `TestNodeRestart` once took about 23 minutes (more than its 20): the watchdog had to restart istio-cni three times; a rerun passed in 10 | 12 (nodes) |
| 11a | A whole-cluster restore takes the manual apps from their own pushes, another moment than OpenBao's: since 11b rotates generated secrets, a manual app restored from before a rotation holds the old value (rotate it again after such a restore) | any |
| 11b | `workload.WaitReady`: StatefulSets with `OnDelete` or a partitioned update never meet `CurrentRevision == UpdateRevision` (none in the repo), and a swallowed `Get` error leaves a timeout without its cause | any |
| 11b | ESO's `refreshTime` is in whole seconds: a refresh in the same second as the previous one is not seen (documented in `workload.go`) | any |
| 11b | The catalog's ExternalSecret reference check matches any `name:`/`forceRename:`/`existingSecret:` value, and does not check reader workload names (rendering every chart would be slow) | any |
| 11b | A lock renewal that reached the API server but whose answer was lost is now kept; one that times out on both the update and the re-read still aborts the run as "lost the lock" (fail-safe, resumable) | any |
| 11b | `lab secrets list` and the `--scheduled` plan write the first-seen baseline (a write from a read path) | any |
| 11b | `NtfyCheck` passes the token in `wget`'s arguments inside ntfy's own pod (where it is already in the environment) | any |
| 11b | `OpenTofu.Prepare` replaces again if it crashes between the apply and recording it (no overlap: harmless) | any |
| 11b | lab's image is pinned by hand after a release: a release that changes the catalog and the CronJob's access together runs the old catalog until the digest is set; the GHCR package must be public | 12 (production) |
| 11b | `TestRotation`'s sub-tests read the "old" value after an earlier interrupted run may already hold the new one (handled for `woodpecker.agent` only) | any |
| 11c | `make dev-clean` is not idempotent: a second destroy of an already-clean sandbox fails (`argocd` reads `cluster`'s outputs, which no longer exist), so `make dev-fresh` stops there; `make dev scope=full` does the same on an empty sandbox | any |
| 11c | The prune ClusterRole may delete any `VolumeSnapshot`, the hourly restore points included (RBAC cannot select by label; a ValidatingAdmissionPolicy limiting it to `velero.io/backup-name` would) | 15 (security) |
| 11c | A cut after Velero set a snapshot's content to `Retain` may leave the storage snapshot when prune deletes the `VolumeSnapshot` (both classes delete by default; unchecked on Ceph) | 12 (nodes) |
| 11c | The "being rotated by" error suggests `lab secrets force-unlock <name> <holder>` without `--env` (the holder check refuses a wrong cluster's lock) | any |
| 14d | `system/kiali` copies kiali-server 2.33.0's templates by hand: a Renovate bump of Kiali's image does not show changes the chart made to its config, RBAC or probes (compare with the chart's render when bumping) | any |
| 14d | No test that an anonymous visitor to Kiali is sent to Kanidm (the API without a session answers 401, checked by hand in the review) | any |
| 14d | `TestBackupRestore`'s whole-cluster restore once read Forgejo right after `lab backup restore` and got 503 (Forgejo not ready yet; a rerun passed): wait for Forgejo before reading | any |
| 15a | The root `Dockerfile` (upstream's docs image) uses unpinned tags (`python:3.13-slim`, `nginx:1.27-alpine`); nothing here deploys it: pin or remove it | any |
| 15a | The blog's image in zot (`registry.<domain>/blog`) is private: `images` cannot pull it once its tag is pinned; give the check zot's read credentials or skip that registry | any |
| 15a | `.trivyignore.yaml`'s 90-day expiry for CVEs is a convention, not checked | any |
| 15a | `check-leaks` scans only the merge-commit range's own diffs: a change made only in a merge commit is not scanned; run directly without BASE it scans one commit | any |
| 15a | `TestCheckIaC` uses a fake Trivy: the ignore file's effect is proven only by the real run in CI | any |
