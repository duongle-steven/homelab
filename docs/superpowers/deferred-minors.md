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
| #31 (7b) | woodpecker's and zot's config changes do not roll their pods | 10 (Git and CI) |
| #31 (7b) | `scripts/check-env-config`'s untemplated-global check knows only four keys | any |
| #32 (7c) | `.envrc` exports the Cloudflare token to every process in the repo shell | 8c (OAuth through `cf`) |
| #32 (7c) | `.direnv/` and `site/` are not git-ignored | any |
| #34 (8a) | Two destinations of one generated secret can drift if one is lost after its source Secret was regenerated (`Replace` would overwrite restored values) | 11 (backups) |
| #34 (8a) | `TestSecretsAudience` matches the plugin's "invalid audience" message | any |
| 8b | `infra/_modules/secrets/wait-namespace.sh` names OpenBao and `platform/openbao` (copied) instead of `infra-secrets` | any |
| 8b | `apply-secrets.sh`'s kubectl errors are hidden (OpenTofu suppresses output when `MANIFEST` is sensitive) | any |
| 8b | Losing `.state/production/secrets` silently creates a new WireGuard key, which `Replace` pushes over OpenBao's | 8c |
| 8b | Removing an `infra-secrets` entry leaves its Secret in the namespace; the docs' removal steps omit `kubectl delete secret` | any |
| 8b | `TestNtfyWatchdogScheduled` does not prove that heartbeats replace one scheduled message | any |
| 8b | `InfoInhibitor` reaches phones: the Alertmanager config override drops the chart's `null` receiver | any |
| 8b | `external-dns/cloudflare#zone_id` is pushed but never read (no `--zone-id-filter`) | any |
| 8b | The 8b spec lists hashicorp/random, which the `secrets` module does not use | any |
| 9 | `docs/installation/sandbox.md` still says the sandbox has no Loki; CLAUDE.md's "production-only allows … only rendered" bullet still lists Loki | any |
| 9 | Production's first Alloy start reads every current pod log from the start: Loki refuses lines older than 7 days and may answer 429 to the burst (default `ingestion_rate_mb` 4) | 12 (Nodes) |
| 9 (chat) | `system/rook-ceph` pins Rook 1.13.5 (latest v1.21.0): upgrade when Ceph first runs | 12 (Nodes) |
