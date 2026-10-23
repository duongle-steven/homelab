# Secrets Rotation (Step 11b) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `lab secrets rotate <name>|--all` replaces any secret in a catalog by its official method (overlap when the
other side allows it), a daily CronJob rotates the due ones, and a failure alerts through ntfy and Telegram.

**Architecture:**
- A new Go package `cli/internal/rotate` holds the catalog (an embedded YAML), the engine (safety checks, ordering,
  the record) and one `Method` per kind of secret.
- Refreshing ExternalSecrets, restarting workloads and waiting for them move out of `cli/internal/backup` into
  `cli/internal/workload`, which both use.
- The CronJob (`platform/secrets-rotation`) runs `lab secrets rotate --scheduled` from the `lab` image, which GitHub
  Actions builds with ko.

**Tech Stack:** Go (cobra, client-go, `github.com/openbao/openbao/api/v2`, the Forgejo SDK already in `cli/`), ko, Helm
charts as in `platform/`, Alertmanager `telegram_configs`, Terragrunt/OpenTofu for Cloudflare's tokens.

**Spec:** `docs/superpowers/specs/2026-10-24-secrets-rotation-design.md`

## Global Constraints

- Latest stable release of every tool, chart, image and Go module, pinned exactly (images by digest when they have no
  version tag); hold one back only for a stated compatibility constraint.
- No secret value in git, command arguments, logs, error messages or test output: errors name the path, never the
  value (as `cli/internal/secrets` does). The Telegram chat id is treated as a secret.
- Branch `feat/secrets-rotation` from `dev`; PR into `dev`; never commit to `dev` or `master`.
- Before each commit: `direnv exec . scripts/ci-check all` passes. Run `make dev` only from `/home/nixos/homelab`, never
  from a worktree. The last `make dev` before the PR covers the whole branch, and a `make dev-fresh` at the end.
- A new component gets its entry in `test/scopes.yaml` in the task that adds it.
- Every commit ends with the attribution lines of the session's system reminder.
- Settings that depend on the environment come from `infra/<env>/cluster/config.json` globals; charts hold no domain
  or address.
- Deferred minors go in `docs/superpowers/deferred-minors.md`; rulings and findings go in the ledger
  (`.superpowers/sdd/2026-10-24-secrets-rotation/progress.md`).
- Tell the user about GitHub Actions runs that would repeat local tests or pile up before adding them
  (memory `flag-ci-compute-cost`).

## Review Focus

1. **A rotation interrupted half-way** (Ctrl-C, the Job's pod killed, OpenBao restarting) after the new value was
   written but before every reader switched: rerunning the same command must finish it. It must not create a third
   value, and with an overlap it must never revoke the old value before the readers moved. Test:
   `TestRotateResumesAfterInterruption` (Task 4).
2. **`--all` or `--scheduled` reaching a manual entry** (GitHub App key, Telegram, Tunnel) without a terminal: skip it
   and name it in the report, never hang waiting for input. Test: `TestRotateSkipsManualWithoutTerminal` (Task 4).
3. **A reader that does not come back** (its ExternalSecret cannot refresh, its pod crash-loops): stop, keep the old
   value valid where there is an overlap, and report which reader failed. Test: `TestRotateKeepsOldOnReaderFailure`
   (Task 4).
4. **A mistyped name:** an error naming the closest catalog names, with nothing touched. Test:
   `TestRotateUnknownName` (Task 4).
5. **Values leaking in errors**, e.g. an OpenBao or Forgejo error that echoes a request body: every error a method
   returns is checked to contain no generated value. Test: `TestMethodErrorsHideValues` (Task 5).

---

### Task 1: Spike on the sandbox (no product code)

**Files:**
- Modify: `docs/superpowers/specs/2026-10-24-secrets-rotation-design.md` (the catalog's *spike* rows and an
  "Amendments" section)
- Ledger: `.superpowers/sdd/2026-10-24-secrets-rotation/progress.md`

Answer each question on the running sandbox, by hand. Record the answer and the evidence (commands, never values) in
the ledger, then amend the spec's catalog row. Undo every hand change, or run `make dev-fresh` at the end of the spike
if one cannot be undone.

- [ ] **Step 1: Woodpecker.** Do Woodpecker 3's per-agent tokens let a new agent connect while the old one still
  runs? Which pod label marks a running step pod (used for the idle wait)?
- [ ] **Step 2: paperless.** Does paperless read `PAPERLESS_SECRET_KEY` fallbacks (Django's `SECRET_KEY_FALLBACKS`),
  or does a new key end every session?
- [ ] **Step 3: kaniop.**
  - How do you trigger one regeneration of a `KanidmOAuth2Client` secret (`spec.secretRotation`, an annotation, or
    deleting the Secret)?
  - Does any reader pick up the new Secret without a restart?
  - Does deleting `kanidm-admin-passwords` still regenerate both admin passwords (11a's finding)?
- [ ] **Step 4: CloudNativePG.** How do you rotate `forgejo-db`'s app password (CloudNativePG's documented way), and
  does Forgejo need a restart?
- [ ] **Step 5: Alertmanager and ntfy.**
  - Does Alertmanager re-read `credentials_file` on each notification?
  - Does ntfy accept two tokens for one user in `NTFY_AUTH_TOKENS`?
  - Does the pinned Alertmanager support `chat_id_file` in `telegram_configs`? If not, render the whole Alertmanager
    config Secret through ESO.
- [ ] **Step 6: Velero and Kopia.** Can a repository's password change through Velero, or is `backup.kopia-password`
  "never" (reason: every repository is encrypted with it, and Velero has no change-password)?
- [ ] **Step 7: OpenTofu's tokens.** With `create_before_destroy` on `cloudflare_account_token`, one apply destroys
  the old token before cert-manager and external-dns switch. Confirm that both retry (cert-manager's DNS01 and
  external-dns's next sync), which makes them class 1 instead of overlap. Plan only, against production's state:
  `terragrunt plan -replace=...`, with the user's approval. The same applies to `backup.r2-token`: avoid 02:00,
  when Velero's daily backup runs.
- [ ] **Step 8: Cloudflare Tunnel.** What is the official way to rotate the Tunnel's secret (provider argument or
  API), and does cloudflared reconnect without a new Tunnel?
- [ ] **Step 9: Amend the spec.** Rewrite the *spike* rows, record any class 3 found, and commit:

```bash
git add docs/superpowers/specs/2026-10-24-secrets-rotation-design.md
git commit -m "docs: 11b's spike findings"
```

### Task 2: Shared reader handling (`cli/internal/workload`)

**Files:**
- Create: `cli/internal/workload/workload.go`, `cli/internal/workload/workload_test.go`
- Modify: `cli/internal/backup/restore.go` (use the new package; behaviour unchanged)

**Interfaces:**
- Produces:
  - `type Client struct{ Kube kubernetes.Interface; Dyn dynamic.Interface; Poll, Wait time.Duration }`
  - `func (c *Client) RefreshExternalSecrets(ctx context.Context, namespace string, names []string) error`: annotates
    `force-sync` as `refreshReaders` does today, then waits until each one's `status.refreshTime` is newer and it is
    `Ready`.
  - `func (c *Client) Restart(ctx context.Context, namespace string, workloads []Ref) error` and
    `func (c *Client) WaitReady(ctx context.Context, namespace string, workloads []Ref) error`, with
    `type Ref struct{ Kind, Name string }` (Deployment or StatefulSet).
  - `Restart` is a rolling restart (the `restartedAt` annotation), skipping Kanidm-owned StatefulSets as
    `restartWorkloads` does. `WaitReady` restarts crash-looping pods once, like `waitWorkloads`.

- [ ] **Step 1: Move the tests.** Move `TestRestoreClusterRestartsOnlyReaders`' reader assertions to
  `TestRefreshExternalSecretsWaitsForRefresh` and `TestRestartSkipsKanidmStatefulSets` in `workload_test.go`. Run
  `go test ./internal/workload/` and expect it to fail with undefined names.
- [ ] **Step 2: Implement `workload.go`.** Make `backup.Client` call it. Delete the old private helpers.
- [ ] **Step 3: Run the tests.** `make -C cli test check` passes, all of `TestRestore*` included.
- [ ] **Step 4: Commit.** `refactor(lab): reader refresh and restarts in cli/internal/workload`

### Task 3: The catalog and its completeness check

**Files:**
- Create:
  - `cli/internal/rotate/catalog.yaml` (embedded)
  - `cli/internal/rotate/catalog.go`, `cli/internal/rotate/catalog_test.go`
  - `cli/internal/rotate/check.go`, `cli/internal/rotate/check_test.go`
  - `cli/internal/cli/secrets.go` (`lab secrets check`; `list` and `rotate` come in Task 4)
- Modify:
  - `scripts/ci-check` (check `rotation-catalog`: `go -C cli run ./cmd/lab secrets check --root .`)
  - `.github/workflows/checks.yaml` (its matrix)
  - `test/scopes.yaml` (`cli/internal/rotate/**` maps to `TestSecretsRotation`: the catalog changes what rotates)

**Interfaces:**
- Produces:

```go
type Method string // generated, app-password, ntfy-overlap, forgejo-token, forgejo-oauth, kaniop-client,
                   // kanidm-admin, opentofu, manual, script, never, automatic, plus any the spike adds
type Reader struct {
	Namespace       string
	ExternalSecrets []string
	Workloads       []workload.Ref
}
type Entry struct {
	Name     string        // e.g. grafana.admin
	Method   Method
	Class    int           // 0 = official overlap, 1, 2 or 3
	Reason   string        // why this class, or why never
	Period   time.Duration // 0 = command only
	Paths    []string      // OpenBao paths under secret/ (every destination of a generated secret)
	Readers  []Reader
	Steps    string        // what a person does, for manual and script entries
	Idle     string        // "woodpecker": wait for no running pipeline
	Settings map[string]string // method-specific: app, Forgejo account, OpenTofu unit and resource, kaniop client
}
func Load() ([]Entry, error)           // parses and validates the embedded catalog
func Find(entries []Entry, name string) (Entry, error)
func Check(root string, entries []Entry) []string // problems; empty when complete
```

- `Load` refuses:
  - a duplicate name;
  - an unknown method;
  - class 3 without `Settings["workaround"]`;
  - a period on a manual entry, or on an entry whose readers include people (the ntfy phone entries);
  - `never` without a reason.
- `Check` finds every secret the repository creates and reports each one missing from the catalog:
  - every `secrets[].name` and `extraSecrets` in `platform/generated-secrets/values.yaml*`;
  - every `path` of `platform/infra-secrets/values.yaml*`;
  - every rendered `KanidmOAuth2Client` with `public` not true. Use `scripts/render-chart` output, as
    `scripts/check-backup-groups` does for PVCs.
  - `forgejo.DeployPath`, `forgejo.ArgoCDPath` and `forgejo.WoodpeckerPath`.
- The catalog starts with every row of the amended spec. Periods are 90 days (`2160h`) where the spec says 90 days.

- [ ] **Step 1: Write the failing tests.**
  - `TestLoadRefusesBadEntries`, table-driven over the refusals above.
  - `TestCatalogLoads`: the real catalog loads.
  - `TestCheckFindsMissingSecret`: a fixture root with one generated secret, one infra-secrets path and one
    confidential client that the catalog lacks returns exactly those three names.
  - `TestCheckRepository`: `Check("../../..", Load())` is empty.
- [ ] **Step 2: Run the tests.** `go test ./internal/rotate/` fails.
- [ ] **Step 3: Implement.** Write `catalog.go`, `check.go`, the catalog, and the `lab secrets check --root`
  subcommand.
- [ ] **Step 4: Run the checks.** `go test ./internal/rotate/` and `direnv exec . scripts/ci-check rotation-catalog`
  both pass.
- [ ] **Step 5: Commit.** `feat(lab): the secrets catalog, and a check that every secret has an entry`

### Task 4: The engine and `lab secrets rotate|list`

**Files:**
- Create: `cli/internal/rotate/rotate.go`, `cli/internal/rotate/record.go`, `cli/internal/rotate/rotate_test.go`
- Modify: `cli/internal/cli/secrets.go`, `cli/internal/cli/root.go`

**Interfaces:**
- Consumes: `Entry`, `Load`, `Find` (Task 3); `workload.Client` (Task 2); `secrets.Store` (existing);
  `backup.Client.Runs` (existing).
- Produces:

```go
// A Method changes one entry in phases, so an interrupted run resumes: Prepare writes the new value (or finds the one
// a previous run wrote), Switch moves the readers to it, Verify checks they work, Revoke ends the old value (a no-op
// without an overlap).
type Method interface {
	Prepare(ctx context.Context, e Entry) error
	Switch(ctx context.Context, e Entry) error
	Verify(ctx context.Context, e Entry) error
	Revoke(ctx context.Context, e Entry) error
}
type Options struct {
	All, Scheduled, Yes, Interactive bool
	SkipBackupCheck                  string // the reason; empty = check
	Now                              time.Time
}
type Result struct {
	Name    string
	Outcome string // rotated, skipped (and why), failed (and the phase)
	Err     error
}
type Rotator struct {
	Entries  []Entry
	Methods  map[Method]Method // by entry method
	Store    Records           // record.go
	Backups  func(ctx context.Context) ([]backup.Run, error)
	Idle     func(ctx context.Context, what string) (bool, error)
	Out      io.Writer
	Poll, Wait time.Duration
}
func (r *Rotator) Plan(names []string, o Options) ([]Entry, error) // the order --all uses; unknown names error
func (r *Rotator) Run(ctx context.Context, plan []Entry, o Options) ([]Result, error)
```

- **Record (`record.go`):** the custom metadata of the entry's first OpenBao path holds the rotation state, through a
  new `secrets.Store.Metadata(ctx, path) (map[string]string, error)` and
  `secrets.Store.SetMetadata(ctx, path, map[string]string) error`.
  - Keys: `rotation-phase` (`prepared`, `switched`, `verified`, `done`), `rotated-at` (RFC 3339), `rotated-by`,
    `rotation-method`, `rotation-reason` (the backup-check override).
  - Entries without an OpenBao path (OpenTofu's, GitHub's) use the same keys on `secret/rotation/<name>`. That path
    is reachable only by the admin role and the new `rotator` role (Task 9).
- **Order for `--all`:**
  1. generated secrets and ntfy;
  2. Forgejo;
  3. Kanidm clients;
  4. Woodpecker and zot (with the idle wait);
  5. OpenTofu's;
  6. manual ones last.

  `never`, `script` and `automatic` entries are listed with their reason and skipped.
- **`Run`:**
  - checks the backup first: a Completed scheduled run started less than 24 hours before `o.Now`, unless
    `SkipBackupCheck`;
  - then, per entry: waits for idleness, resumes from the recorded phase, runs the phases, records each one and stops
    at the first failure;
  - returns every entry's result.
- **`lab secrets list`** prints each entry's name, method, class, period, last rotation and next due date.
- **`lab secrets rotate <name>... | --all`:**
  - prints the plan, the workloads that will restart (marking those with one replica, which stop briefly) and the
    people who must act, then asks for confirmation
    (`confirm` from `cli/backup.go`);
  - `--yes` skips the confirmation;
  - `--scheduled` implies `--yes`, picks only due entries and skips manual ones;
  - `--skip-backup-check=<reason>` overrides the backup check;
  - exits non-zero when any entry failed or a due entry is past its due date plus 14 days.

- [ ] **Step 1: Write the failing tests.** They use fake methods that record their calls, and a fake `Records`.
  - `TestRotateRunsPhasesInOrder`
  - `TestRotateResumesAfterInterruption`: the phase is `switched`, so only `Verify` and `Revoke` run.
  - `TestRotateKeepsOldOnReaderFailure`: `Verify` fails, `Revoke` is never called, and the result names the phase.
  - `TestRotateSkipsManualWithoutTerminal`
  - `TestRotateUnknownName`: the error contains the closest name, and nothing ran.
  - `TestRotateRefusesWithoutRecentBackup`, and its override, which records the reason.
  - `TestRotateWaitsForIdle`: it times out with an error naming Woodpecker.
  - `TestScheduledPicksDueOnly`
  - `TestScheduledFailsWhenOverdue`
  - `TestPlanOrderAll`
- [ ] **Step 2: Run the tests.** `go test ./internal/rotate/` fails.
- [ ] **Step 3: Implement.** Write `rotate.go`, `record.go`, the two `Store` methods (with fake-OpenBao tests in
  `cli/internal/secrets/openbao_test.go`) and the commands.
- [ ] **Step 4: Run the tests.** `make -C cli test check` passes.
- [ ] **Step 5: Commit.** `feat(lab): lab secrets rotate and list, resumable, behind a backup check`

### Task 5: Generated secrets and app passwords

**Files:**
- Create: `cli/internal/rotate/generated.go`, `cli/internal/rotate/apppassword.go`, their tests
- Modify: `cli/internal/cli/secrets.go` (register the methods)

**Interfaces:**
- Consumes: `Method`, `Entry` (Task 4); `workload.Client` (Task 2); `secrets.Store`.
- Produces:
  - `type Generated struct{ Store *secrets.Store; Workloads *workload.Client; Random func(Entry) (string, error) }`.
    `Prepare` writes one new value to every `Paths` destination under `Settings["key"]`. Its length and character set
    come from `platform/generated-secrets/values.yaml`'s entry (`length`, `special`), copied into `Settings`, and a
    test keeps both in step. `Switch` refreshes the readers' ExternalSecrets and restarts their workloads. `Verify`
    waits until they are ready. `Revoke` is a no-op.
  - `type AppPassword struct{ ... }` for `forgejo.admin`, `grafana.admin` and `paperless.admin`. `Prepare` sets the
    new password through the app itself, then writes it to OpenBao:
    - Forgejo: the admin API with the current password;
    - Grafana: `PUT /api/admin/users/1/password` as admin;
    - paperless: `manage.py changepassword` in its pod, the password on stdin.

    A person's open session stays valid. `Switch` refreshes the ExternalSecrets with no restart.
- The class of `paperless.secret-key`, `woodpecker.agent` and `registry.ci` follows Task 1's findings. When the spike
  found no overlap they use `Generated` plus the idle wait. `registry.ci`'s `Switch` also updates Woodpecker's org
  secret `registry_password` (`PATCH /api/orgs/<id>/secrets/registry_password`) with the admin token the docs use.

- [ ] **Step 1: Write the failing tests.**
  - `TestGeneratedWritesEveryDestination`: `ntfy.alerts-token` writes both of its paths with the same value.
  - `TestGeneratedMatchesGeneratorSettings`
  - `TestAppPasswordChangesTheAppFirst`: the app's fake records the change before OpenBao's write.
  - `TestMethodErrorsHideValues`: every method in the package is fed failing fakes, and each error is checked to not
    contain the value.
- [ ] **Step 2: Run the tests.** They fail.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run the tests.** They pass, and `make -C cli test check` passes.
- [ ] **Step 5: Commit.** `feat(lab): rotate generated secrets and the apps' admin passwords`

### Task 6: ntfy's overlap

**Files:**
- Modify: `apps/ntfy.yaml` (`NTFY_AUTH_TOKENS` also lists `previous_token` when present), `test/ntfy_test.go`
- Create: `cli/internal/rotate/ntfy.go`, its test

**Interfaces:**
- Produces: `type NtfyOverlap struct{ ... }`. It works per user (`alerts`, `phone`):
  - `Prepare` keeps the current token as `previous_token` and writes a new `token`, in every destination of the
    entry.
  - `Switch` refreshes `ntfy-auth` and Alertmanager's `ntfy` Secret, and restarts ntfy. It restarts Alertmanager only
    if the spike found it does not re-read `credentials_file`.
  - `Verify` publishes with the new token and expects 200.
  - `Revoke` removes `previous_token` and restarts ntfy again. For `phone` it waits for the person's confirmation
    (interactive only).

- [ ] **Step 1: Write the failing tests.**
  - `TestNtfyAcceptsPreviousToken`, a render test: with `previous_token` set, `NTFY_AUTH_TOKENS` lists
    `alerts:tk_<new>,alerts:tk_<old>`.
  - `TestNtfyOverlapRevokesAfterVerify`, a unit test.
- [ ] **Step 2: Run the tests.** They fail.
- [ ] **Step 3: Implement.** Change the template and write `ntfy.go`.
- [ ] **Step 4: Run the tests.** They pass, and `scripts/render-chart apps/ntfy.yaml dev` renders.
- [ ] **Step 5: Commit.** `feat(ntfy): two tokens per user during a rotation`

### Task 7: Forgejo's tokens and Woodpecker's OAuth client

**Files:**
- Modify: `cli/internal/forgejo/sync.go`, `cli/internal/forgejo/sync_test.go`
- Create: `cli/internal/rotate/forgejo.go`, its test

**Interfaces:**
- Produces:
  - In `forgejo`: `func RotateToken(ctx context.Context, ep Endpoint, adminPassword string, tokens Tokens, name string) (old string, err error)`
    creates a token named `<token>-<unix time>` beside the existing one and writes it to OpenBao. It returns the old
    token's name.
  - `func DeleteToken(ctx context.Context, ep Endpoint, adminPassword, name, tokenName string) error`.
  - `func RotateWoodpeckerClient(ctx context.Context, ep Endpoint, adminPassword string, tokens Tokens) error`
    regenerates the OAuth2 application's secret (no overlap).
  - `ensureToken` keeps accepting any valid token, whatever its name, so `sync` never undoes a rotation.
  - In `rotate`: `type ForgejoToken struct{...}` (`Switch` refreshes Argo CD's `argocd-repo-forgejo` through
    `refreshArgoCDCredential`, `Revoke` deletes the old token) and `type ForgejoOAuth struct{...}` (class 2).

- [ ] **Step 1: Write the failing tests** against the existing fake Forgejo (`fake_test.go`).
  - `TestRotateTokenKeepsOldUntilDeleted`
  - `TestSyncKeepsRotatedToken`
  - `TestRotateWoodpeckerClient`
- [ ] **Step 2: Run the tests.** They fail.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run the tests.** They pass.
- [ ] **Step 5: Commit.** `feat(lab): rotate Forgejo's deploy and argocd tokens with an overlap`

### Task 8: Kanidm, CloudNativePG, OpenTofu and manual entries

**Files:**
- Create: `cli/internal/rotate/kanidm.go`, `cli/internal/rotate/cnpg.go`, `cli/internal/rotate/opentofu.go`,
  `cli/internal/rotate/manual.go`, and their tests
- Modify: `infra/_modules/cloudflare/main.tf` and `infra/_modules/backup/main.tf` (`lifecycle { create_before_destroy = true }`
  on the account tokens, if Task 1 kept it)

**Interfaces:**
- Produces:
  - `KaniopClient`: the trigger Task 1 found, then wait for the Secret's new `resourceVersion`, then restart the
    readers.
  - `KanidmAdmin`: deletes `kanidm-admin-passwords`, then waits for kaniop to recreate it.
  - `CNPGPassword`: CloudNativePG's documented way from Task 1.
  - `OpenTofu`: in `infra/<env>/<unit>`, runs `terragrunt apply -replace=<Settings["resource"]>`, then the
    `secrets` unit's apply. Then it refreshes the `infra-secrets` push and the readers. It runs only from the
    workstation; `--scheduled` skips it and reports it as due.
  - `Manual`: prints `Steps`, waits for the person to confirm, then runs the entry's `Switch` and `Revoke` settings.
    For the GitHub App key, it checks that Renovate's next token mint works before telling the person to delete the
    old key.
  - `make infra env=production` warns about every OpenTofu entry that is due (`lab secrets list --due --method opentofu`).

- [ ] **Step 1: Write the failing tests.**
  - `TestKaniopClientRestartsReaders`
  - `TestKanidmAdminWaitsForRecreate`
  - `TestOpenTofuRunsReplaceThenSecrets`: a fake `terragrunt` on `PATH` records its arguments.
  - `TestManualNeedsConfirmation`
  - `TestMakeInfraWarnsDue`, in `test/ci_scripts_test.go` next to `TestMakeInfraProduction`.
- [ ] **Step 2: Run the tests.** They fail.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run the tests.** They pass, and `make -C infra check` passes.
- [ ] **Step 5: Commit.** `feat(lab): rotate Kanidm, CloudNativePG and OpenTofu's secrets; guided manual ones`

### Task 9: The scheduled rotation in the cluster

**Files:**
- Create:
  - `.github/workflows/lab-image.yaml`: on a push to `master` touching `cli/**`, ko builds `cli/cmd/lab` into
    `ghcr.io/duongle-steven/homelab-lab:<sha>` (no `latest` tag), with `packages: write`. This is one short job per
    release, not a sandbox run.
  - `platform/secrets-rotation/` (`Chart.yaml`, `values.yaml`, `values-dev.yaml`, `templates/`)
  - `scripts/build-lab-image`: `ko build --local`, then `k3d image import` for the sandbox.
- Modify:
  - `platform/openbao/values.yaml`: role `rotator`, bound to `secrets-rotation/secrets-rotation`, with audience
    `openbao`. Its policy has read/write on `secret/data/<path>` and `secret/metadata/<path>` of the scheduled
    entries' paths and `secret/*/rotation/*`.
    OpenBao configures itself only on first start, so the PR body says a running sandbox needs `make dev-fresh`.
  - `system/argocd/values-dev.yaml`: the new component.
  - `test/scopes.yaml`: `platform/secrets-rotation/**` and `cli/internal/rotate/**` map to `TestSecretsRotation`.
  - `Makefile`'s `dev` target: runs `scripts/build-lab-image` before the tests.
  - `system/monitoring-system`'s rules: `SecretRotationFailed` and `SecretRotationOverdue` (below).
  - `scripts/check-backup-groups`: no PVC is expected.

**Interfaces:**
- Values:
  - `image` is `ghcr.io/duongle-steven/homelab-lab@sha256:<digest>`, which Renovate bumps; `values-dev.yaml` uses
    the locally imported `homelab-lab:dev`.
  - `schedule` is `"0 3 * * *"` in `global.timezone`. `concurrencyPolicy: Forbid`, `backoffLimit: 0`.
- RBAC: a ClusterRole that can `patch` the readers' ExternalSecrets and Deployments/StatefulSets and `get`/`delete`
  the kaniop Secrets in the catalog's namespaces, nothing else. A test renders it and compares it with the catalog's
  scheduled readers.
- Alerts:
  - `SecretRotationFailed`: `kube_job_status_failed{namespace="secrets-rotation"} > 0`, for the newest job.
  - `SecretRotationOverdue`: `time() - kube_cronjob_status_last_successful_time{namespace="secrets-rotation"} > 3*86400`.
  - Both are `severity: warning` and route like every alert. This replaces the spec's "metric for each secret":
    `lab secrets list` shows each secret, and the Job's own metrics carry the alerts. Record this as an amendment in
    the spec.

- [ ] **Step 1: Write the failing tests.**
  - `TestSecretsRotationRender`: the CronJob, its schedule, `Forbid`, the image digest in production, and RBAC
    limited to the catalog's namespaces.
  - `TestOpenBaoRotatorPolicy`: the policy reaches only the scheduled paths.
  - `TestSecretRotationAlerts`: promtool unit tests in the existing rules test pattern.
- [ ] **Step 2: Run the tests.** They fail.
- [ ] **Step 3: Implement.** Write the chart, the workflow (actions pinned by SHA, latest releases), the script, the
  OpenBao role and the alerts. `actionlint` passes.
- [ ] **Step 4: Run the checks.** `direnv exec . scripts/ci-check all` passes.
- [ ] **Step 5: Commit.** `feat: the daily scheduled rotation (platform/secrets-rotation) and its alerts`

### Task 10: Telegram, a second path for every alert

**Files:**
- Modify:
  - `system/monitoring-system/values.yaml`: a receiver `telegram`, with the bot token and chat id from
    `secret/monitoring-system/telegram` through an ExternalSecret, or the whole config through ESO if Task 1 found
    no `chat_id_file`. Every route continues to both `ntfy` and `telegram` (`continue: true`). `values-dev.yaml`
    leaves `telegram` out.
  - `system/egress/values.yaml`: `monitoring-system` on 443.
  - `scripts/check-secret-stores` expectations, and `docs/how-to-guides/alerts.md`: create a bot with BotFather, find
    the chat id, and write both with the admin role.
  - The catalog: `telegram.bot-token` and `telegram.chat-id` (manual).

- [ ] **Step 1: Write the failing test.** `TestAlertmanagerTelegramProductionOnly`: production renders the receiver
  and both routes, and dev renders neither.
- [ ] **Step 2: Run the test.** It fails.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run the checks.** The test passes, and `scripts/check-env-config` and `scripts/check-secret-stores`
  pass.
- [ ] **Step 5: Commit.** `feat(monitoring): alerts also reach Telegram`

### Task 11: Sandbox test, docs, and the full run

**Files:**
- Create: `test/secrets_rotation_test.go`, `docs/how-to-guides/secrets-rotation.md` (added to `mkdocs.yml`)
- Modify:
  - `docs/concepts/secrets-management.md` (a Rotation section)
  - `CLAUDE.md`:
    - roadmap 11b marked done, with its summary;
    - a rule: a new secret needs a catalog entry;
    - the production checklist: OpenTofu's rotations, the GitHub App key, Telegram and the CronJob's image are
      unproven in the sandbox;
    - the ideas list: healthchecks.io, with "pause before a planned shutdown", and leak detection.
  - `docs/superpowers/deferred-minors.md`: remove the 11a → 11b line this step fixes (manual apps from another moment
    than OpenBao, if the rotation's backup check covers it; otherwise keep it with its new owner)
  - `scripts/ci-check`'s `offline_tests` (the new offline tests)

**Interfaces:**
- Consumes: everything above.

- [ ] **Step 1: Write `TestSecretsRotation`.** It runs `lab` from `cli/` as the other tests do, with these sub-tests:
  - `generated`: `grafana.admin`. The new password logs in to Grafana's API, and the old one is refused.
  - `overlap`: `ntfy.alerts-token`. During the window both tokens publish. After `Revoke` the old one gets 401, and
    Alertmanager's test alert reaches ntfy.
  - `forgejo`: `forgejo.argocd-token`. Argo CD refreshes `homelab` and stays Synced, and the old token gets 401.
  - `kaniop`: `kanidm-client.grafana`. A Kanidm login to Grafana works after the rotation.
  - `idle`: `woodpecker.agent`. It waits while a pipeline runs, then a new pipeline passes.
  - `all`: `--all --yes` on the sandbox's entries. Every result is rotated or skipped with a reason.
  - `scheduled`: a Job created from the CronJob succeeds. A Job forced to fail raises `SecretRotationFailed` in
    Prometheus.

  The sandbox has one replica per app, so outages are not asserted.
- [ ] **Step 2: Run it alone.** `make dev tests='SecretsRotation'` from the main checkout. Fix what fails, with a test
  first.
- [ ] **Step 3: Write the docs and the CLAUDE.md changes.** `mkdocs build --strict` passes.
- [ ] **Step 4: Run the full checks.** `direnv exec . scripts/ci-check all`, then `make dev`.
- [ ] **Step 5: Commit.** `test: TestSecretsRotation; docs: rotating secrets`
- [ ] **Step 6: Run `make dev-fresh`.** It must pass entirely, including `TestBackupRestore` and `TestNodeRestart`.
  The PR body carries the rulings, the spike's amendments and the deferred minors, with the label `sandbox` (the
  first install changes: OpenBao's role, a new component).
