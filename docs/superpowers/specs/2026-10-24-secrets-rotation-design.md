# Step 11b: secrets rotation

Status: design approved in chat 2026-10-24. Branch: `feat/secrets-rotation` from `dev`.

## Goal

A secret must be easy to replace in two situations:

- **On a schedule:** this is the best practice that limits the damage of a leak nobody has noticed.
- **In an emergency:** a secret is known to have leaked, or might have, and is perhaps being used. You can replace
  just the one you suspect, or every secret at once ("assume everything leaked").

Treat the homelab as production from the start, so this step never has to be rebuilt. Rotation should cause no
downtime, ideally by letting the old and the new value work side by side for a while.

The user decided the following in chat (2026-10-23/24):

- One command handles both cases: one secret by name, or all of them.
- Rotation follows each secret's official way of changing it, when it has one.
- Production will run several replicas wherever an app or tool needs them. That is not done in this step. The
  sandbox keeps one replica where resources require it, and a short outage there is fine.
- Results reach people through ntfy, with Telegram as a second path.

## Context

- Upstream `nixos` (`fe353778`, 2026-10-23) has nothing to reuse. Its roadmap lists "Secrets rotation" unchecked,
  and its `docs/concepts/secrets-management.md` only says to reapply and restart after a rotation. Follow its layout:
  commands in `lab` (`cli/`, cobra) and secrets in OpenBao.
- Best practice (OWASP Secrets Management Cheat Sheet, NIST SP 800-57 and SP 800-63B, Vault/OpenBao guidance):
  - Short-lived credentials beat rotation. Three are already short-lived here: Renovate's token is minted for each
    run, R2's state credentials expire, and TLS certificates come from cert-manager.
  - Automate scheduled rotation per kind of secret.
  - Human passwords are not rotated on a schedule (NIST 800-63B), only when they may have leaked.
  - Every secret needs a ready emergency procedure: where it lives, who reads it, what to restart, and whether the
    old value is really revoked.
  - Rotate without an outage: where both sides must change, keep an overlap window or change both together.
- Where secrets live today (`docs/concepts/secrets-management.md`):
  - generated secrets: `platform/generated-secrets` pushes each one once (`IfNotExists`) to
    `secret/<namespace>/generated/<name>`;
  - OpenTofu's secrets: `infra/<env>/secrets` → namespace `infra-secrets` → `platform/infra-secrets` (`Replace`);
  - Kanidm's OAuth2 client secrets and admin passwords: written by kaniop;
  - Forgejo's `deploy` and `argocd` tokens and Woodpecker's Forgejo OAuth client: written by
    `lab infra forgejo sync`;
  - OpenBao's unseal key: `infra/<env>/openbao`;
  - the state passphrase: the workstation (`scripts/rotate-state-passphrase`).
- kaniop's `KanidmOAuth2Client` has `spec.secretRotation` (`enabled`, `periodDays`): the operator regenerates the
  client secret periodically. This is the official way for those secrets.
- Step 11a gives backups and `lab backup restore`, so a failed rotation can be undone.

## Decisions

### 1. One command, one secret or all of them

- `lab secrets rotate <name>` replaces one secret.
- `lab secrets rotate --all` replaces every secret in the catalog (decision 3). It prints the list and the expected
  effect of each item (what restarts, who must act) and asks for confirmation. It then rotates them one after
  another and reports each result. A failure stops the run and names what is done and what is left.
- `lab secrets list` prints the catalog: each secret's method, class, schedule and last rotation.
- Names are the catalog's, e.g. `grafana.admin`, `ntfy.alerts-token`, `forgejo.deploy-token`,
  `kanidm-client.grafana`, `cloudflare.cert-manager`.
- `--yes` skips the confirmation, like `lab backup`. In an emergency the person runs the command from the
  workstation. The scheduled run uses the same code (decision 5).

### 2. How a secret is rotated: the official way first

For each secret, in this order:

1. **Official overlap.** The other side supports two valid values at once, or a new credential beside the old one:
   several tokens per user, a new API token before the old one is deleted, several keys per GitHub App. Create
   the new value, move every reader to it, check that they work, then revoke the old one. This needs no further
   justification.
2. **No official overlap.** Classify what a short failure would do:
   - **Class 1, nobody notices or it retries itself:** change both sides close together and restart the readers.
     A failed request is retried automatically (Alertmanager re-sends, kubelet retries pulls, Argo CD refetches),
     or nothing reads the secret at runtime (admin passwords used only by people, occasionally).
   - **Class 2, a person can retry:** a login fails or a CI pipeline fails and is restarted. Accept it, but make it
     unlikely: wait until the app is idle (no running Woodpecker pipeline) and run scheduled rotations at a quiet
     hour.
   - **Class 3, cannot be skipped:** a failure loses data or leaves something stuck until a person repairs it. Only
     here does the builder design a workaround that creates an overlap (e.g. a second zot user or a second OAuth2
     client, then switch and delete the first). The spec found no class 3 secret yet; the spike confirms it.
3. The catalog records each secret's method and class, and the reason for that class.

### 3. The catalog

The catalog is a file in the repository, e.g. `cli/internal/secrets/catalog.yaml` or Go code. The builder chooses
its form. Each entry gives:

- the name, and where the secret lives (OpenBao path, OpenTofu resource, kaniop, Forgejo);
- its readers (namespace, ExternalSecret or Secret, and the workloads to restart);
- the method (decision 2) and the class with its reason;
- whether it rotates on a schedule, and its period;
- for secrets `lab` cannot change alone, the steps a person follows.

A check fails when a secret exists that the catalog does not know, like `scripts/check-backup-groups` does for
PVCs. That check covers:

- every generated secret in `platform/generated-secrets/values.yaml`;
- every `infra-secrets` path;
- every `KanidmOAuth2Client`;
- the Forgejo tokens.

A new secret therefore cannot ship without a rotation entry. Entries that are never rotated stay in the catalog
with the reason.

Starting catalog. Methods marked *spike* are confirmed by Task 1 (decision 10), and the builder fills in the rest
the same way:

| Secret | Readers | Method (expected) | Class if no overlap | Schedule |
|---|---|---|---|---|
| `forgejo.admin`, `grafana.admin`, `paperless.admin` | people; Forgejo's init container and `lab infra forgejo sync` use Forgejo's | change it in the app (its API or CLI), then in OpenBao | 1 (Forgejo's: run while sync and the init container are not running) | command only (human password) |
| `paperless.secret-key` | paperless | *spike*: Django's `SECRET_KEY_FALLBACKS`, if paperless exposes it | 2 (sessions end, people log in again) | command only |
| `woodpecker.agent` | Woodpecker server and agents | *spike*: Woodpecker 3's per-agent tokens | 2 (wait for no running pipeline) | 90 days |
| `registry.ci` | zot's htpasswd, Woodpecker's org secret `registry_password` (set by hand today, `docs/how-to-guides/woodpecker.md`) | change in OpenBao and zot, then the org secret through Woodpecker's API | 2 (wait for no running pipeline) | 90 days |
| `ntfy.alerts-token` | ntfy (`NTFY_AUTH_TOKENS`), Alertmanager (`credentials_file`) | overlap: ntfy accepts several tokens per user | n/a | 90 days |
| `ntfy.alerts-password` | none at runtime (*spike*: confirm) | change both sides | 1 | 90 days |
| `ntfy.phone-token`, `ntfy.phone-password` | people's phones | overlap: list both tokens until people confirm, then drop the old | n/a | command only: people must re-enter them |
| `forgejo.deploy-token`, `forgejo.argocd-token` | `lab infra forgejo push`, Argo CD's `argocd-repo-forgejo` | overlap: a new Forgejo access token, then delete the old | n/a | 90 days |
| `forgejo.woodpecker-oauth` (`secret/woodpecker/forgejo`) | Woodpecker | Forgejo regenerates the OAuth2 app's secret (no overlap) | 2 (a login fails) | command only |
| `kanidm-client.<app>` (every confidential `KanidmOAuth2Client`) | the app's Secret `<client>-kanidm-oauth2-credentials` | kaniop's `secretRotation` or a one-off regeneration (*spike*: how to trigger one), then restart the readers | 2 (a login fails, open sessions stay) | command only, or kaniop's period if the spike shows readers restart cleanly |
| `kanidm.admin-passwords` (`admin`, `idm_admin`) | `lab`, the tests, kaniop | kaniop recovers them when their Secret is deleted (11a's finding) | 1 | command only |
| `cloudflare.cert-manager`, `cloudflare.external-dns` | cert-manager, external-dns | overlap: OpenTofu creates the new token before it destroys the old (`create_before_destroy`, `-replace`), then `infra-secrets` pushes it | n/a | 90 days, through `make infra env=production` (decision 5) |
| `cloudflare.tunnel` | cloudflared | *spike*: rotating a Tunnel's secret | 2 | command only |
| `backup.r2-token` (`velero/repository`) | Velero | overlap: a new account token first | n/a | 90 days, like Cloudflare's |
| `backup.kopia-password` (`velero/encryption`) | Velero's repositories | *spike*: Kopia can change a repository's password, Velero may not support it | never if not supported (reason documented) | — |
| `renovate.github-app-key` | Renovate's initContainer | overlap: a GitHub App holds several private keys; generating one is manual (no API): `lab` stops and gives the steps, then deletes the old key | n/a | command only |
| `forgejo-db` (CloudNativePG's `forgejo-db-app`) | Forgejo | *spike*: CloudNativePG's own rotation | 2 | command only |
| `telegram.bot-token` (new, decision 7) | Alertmanager | BotFather's `/revoke` (manual, no overlap) | 1 (Alertmanager retries; ntfy still delivers) | command only |
| `openbao.unseal-key`, `wireguard.private-key` | — | never: the first holds all of OpenBao's data, the second is in every peer | — | — |
| `state.passphrase` | OpenTofu | `scripts/rotate-state-passphrase` (existing); `lab` points to it | — | command only |
| Renovate's token, R2's state credentials, TLS certificates | — | already short-lived | — | automatic |

### 4. Safety around every rotation

Before rotating, `lab` checks for a completed off-site backup from the last 24 hours. Without one it stops and
offers `lab backup push` (a manual group) or waits for the daily run. `--skip-backup-check=<reason>` overrides
this; the reason is recorded.

`lab` then checks each reader after the change: the ExternalSecret has refreshed, the workloads are ready, and the
credential works (an authenticated request where one is cheap).

On a failure `lab` keeps what it can roll back. With an overlap it never revokes the old value, so the readers keep
working. It reports the failure (decision 7).

Restarts are rolling (`kubectl rollout restart`), so an app with several replicas has no outage. An app with one
replica on a `ReadWriteOnce` volume has a short one, and `lab` names those apps in its confirmation.

### 5. Scheduled rotation

- Only catalog entries marked with a period rotate on a schedule. The starting default is 90 days, at a quiet hour
  (03:00 in `global.timezone`).
- In the cluster this runs as a CronJob. It runs `lab secrets rotate --scheduled`, which picks the entries that are
  due, with a service account and OpenBao role allowed only those entries' paths and restarts.
  - This needs `lab` as a container image. My call: built by GitHub Actions from `master` into
    `ghcr.io/duongle-steven/homelab-lab`, pinned by digest, bumped by Renovate. The builder may propose another
    source if this one fights the deploy path.
- OpenTofu's entries (Cloudflare, R2) cannot rotate from inside the cluster, which holds no Cloudflare account
  credentials. Their due date is checked by `make infra env=production`: it warns when one is due, and
  `lab secrets rotate <name>` runs the `-replace`.
- A scheduled run that finds Woodpecker busy waits for it, within a limit, then fails and alerts.

### 6. Record

Each rotation records the secret, the time, the method, the result and the person or job. OpenBao keeps every
value's versions; `lab` adds the record in a place it can read back, e.g. custom metadata on the OpenBao entry or
an annotation, chosen by the builder. `lab secrets list` shows the last rotation, and Prometheus exposes it as a
metric for each secret.

### 7. Reporting: alerts through ntfy and Telegram

- A failed rotation raises an alert through Alertmanager like any other, e.g. `SecretRotationFailed`.
- A rotation more than a margin past its due date raises `SecretRotationOverdue`.
- A successful one is only logged and recorded; it sends no message.
- Alertmanager gains a second receiver, Telegram (`telegram_configs`), next to ntfy for every alert, so the
  existing alerts (`VeleroNoRecentBackup` and the others) also reach people while ntfy is down.
  - The bot token and the chat id live in OpenBao, never in git (the chat id identifies a person), and the person
    writes them with the admin role, as `docs/how-to-guides/alerts.md` will say.
  - Egress: `monitoring-system` on 443 to the internet (`system/egress`).
  - The sandbox renders the receiver but does not deliver to Telegram.
- An external dead man's switch (healthchecks.io) for a cluster that is down entirely is **not** part of 11b. It
  stays in the ideas, which gain one line: pause the check before switching the homelab off on purpose (the
  healthchecks.io API), e.g. in a shutdown command.

### 8. Not in this step

- More replicas for the apps that need them: each app and step 12 (three nodes) decide that.
- Detecting leaks: GitHub secret scanning and push protection, gitleaks, audit logs of OpenBao, Cloudflare and
  GitHub. Record this as an idea or a later step.
- People's own passwords (Kanidm accounts) and their zot or Woodpecker personal tokens: people change their own.
- Matrix's signing key (its federation identity), if production has one: catalogued as never, with the reason.

### 9. Anti-goals

- A rotation that leaves a reader on a revoked value without saying so.
- A rotation that silently changes something people must re-enter, like the phone's ntfy credentials, on a
  schedule.
- Secrets or the chat id in git, in logs, in command arguments or in test output.
- A scheduled job with more rights than its due entries need.

### 10. Tests and the spike

- **Task 1 is a spike on the sandbox**, as 11a's was. It confirms the *spike* methods in the catalog:
  - Woodpecker's agent tokens;
  - `SECRET_KEY_FALLBACKS` in paperless;
  - how to trigger kaniop's rotation once, and whether its readers restart cleanly;
  - CloudNativePG's password rotation;
  - the Tunnel's secret;
  - Kopia's password through Velero;
  - whether Alertmanager re-reads `credentials_file`, so no restart is needed.

  It also confirms that no secret is class 3. Its findings amend this spec before the plan continues.
- `make dev` gains `TestSecretsRotation`. For each method, on at least one secret, it proves:
  - the new value works;
  - with an overlap, both values work during the window;
  - the old value is refused afterwards;
  - the readers are ready again.

  It also proves `--all` on the sandbox's entries, and that a failing rotation raises `SecretRotationFailed`. The
  sandbox has one replica per app, so outages there are expected and not asserted.
- Unit tests in `cli/` cover the catalog check, the ordering, the backup check, the idle wait and the failure
  reports.
- OpenTofu's rotations, the GitHub App key and Telegram can only be planned or rendered in the sandbox. They join the
  "Before the first production deploy" list.

## Left to the builder

The catalog's format, the record's location, the metric's name, the CronJob's namespace and RBAC, the overdue
margin, and the order `--all` uses. Pick sensible defaults and state them in the plan.
