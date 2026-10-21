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
| #32 (7c) | `.direnv/` and `site/` are not git-ignored | any |
| #34 (8a) | Two destinations of one generated secret drift if only one is lost (a partial restore): the push gives the lost one a new value (8e removed the copy that regenerated; restore both, or copy the survivor's value first) | 11 (backups) |
| #34 (8a) | `TestSecretsAudience` matches the plugin's "invalid audience" message | any |
| 8b | `infra/_modules/secrets/wait-namespace.sh` names OpenBao and `platform/openbao` (copied) instead of `infra-secrets` | any |
| 8b | `apply-secrets.sh`'s kubectl errors are hidden (OpenTofu suppresses output when `MANIFEST` is sensitive) | any |
| 8b | Losing `secrets/tfstate.json` on R2 (or the state passphrase) silently creates a new WireGuard key, which `Replace` pushes over OpenBao's | 11 (backups) |
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
| dev shell cleanup | `.woodpecker/static-checks.yaml` still runs the removed `TestToolsVersions` and pre-commit | 10c (Woodpecker) |
| 8e | `make dev-fresh` on the workstation hits the main run's 60-minute `go test` timeout (the first sync's waits count inside the tests) | any |
| 8e | Browser SSO tests flake on Kanidm's login page (OpenCloud, Grafana, paperless: "login page never loaded", `chrome-error`); a rerun passes | any |
