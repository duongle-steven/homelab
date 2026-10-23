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
     client, then switch and delete the first). The spike found no class 3 secret.
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

Starting catalog. Task 1's spike (decision 10) confirmed the methods it covered; the builder fills in the rest the
same way:

| Secret | Readers | Method | Class if no overlap | Schedule |
|---|---|---|---|---|
| `forgejo.admin`, `grafana.admin`, `paperless.admin` | people; Forgejo's init container and `lab infra forgejo sync` use Forgejo's | change it in the app (its API or CLI), then in OpenBao | 1 (Forgejo's: run while sync and the init container are not running) | command only (human password) |
| `paperless.secret-key` | paperless (sessions, its Celery messages, the classifier file) | no overlap: paperless has no `SECRET_KEY_FALLBACKS` setting. Change it in OpenBao, refresh, restart paperless while no task is queued; the classifier file fails its check and is retrained by the hourly task | 2 (every session ends, people log in again) | command only |
| `woodpecker.agent` | Woodpecker agents (per-agent tokens) | overlap: Woodpecker 3's per-agent tokens. `lab` creates a new agent token (`POST /api/agents`, admin API), writes it to OpenBao, restarts the agent once no step pod runs (label `woodpecker-ci.org/step`), then deletes the old agent (`DELETE /api/agents/<id>`), which revokes its token. The server's shared `WOODPECKER_AGENT_SECRET` is dropped (empty: only per-agent tokens), because changing it never revokes the old value (amendment 3) | n/a | 90 days |
| `registry.ci` | zot's htpasswd, Woodpecker's org secret `registry_password` (set by hand today, `docs/how-to-guides/woodpecker.md`) | change in OpenBao and zot, then the org secret through Woodpecker's API | 2 (wait for no running pipeline) | 90 days |
| `ntfy.alerts-token` | ntfy (`NTFY_AUTH_TOKENS`), Alertmanager (`credentials_file`) | overlap: ntfy accepts several tokens per user. Add the new one (ntfy restarts), switch OpenBao's value (Alertmanager reads the file at every notification, no restart), then drop the old (ntfy restarts and removes it from `auth.db`) | n/a | 90 days |
| `ntfy.alerts-password` | none at runtime (only ntfy's own `NTFY_AUTH_USERS`; confirmed) | change it in OpenBao, refresh `ntfy-auth`, restart ntfy | 1 | 90 days |
| `ntfy.phone-token`, `ntfy.phone-password` | people's phones | overlap: list both tokens until people confirm, then drop the old | n/a | command only: people must re-enter them |
| `forgejo.deploy-token`, `forgejo.argocd-token` | `lab infra forgejo push`, Argo CD's `argocd-repo-forgejo` | overlap: a new Forgejo access token, then delete the old | n/a | 90 days |
| `forgejo.woodpecker-oauth` (`secret/woodpecker/forgejo`) | Woodpecker | Forgejo regenerates the OAuth2 app's secret (no overlap) | 2 (a login fails) | command only |
| `kanidm-client.<app>` (every confidential `KanidmOAuth2Client`) | the app's Secret `<client>-kanidm-oauth2-credentials` | one-off regeneration: the annotation `kaniop.rs/force-secret-rotation` on the client (kaniop resets the secret in Kanidm, rewrites the Secret and removes the annotation), then restart the readers that read it at start (Grafana, Actual Budget, paperless, Forgejo); Argo CD picks it up live. Deleting the Secret does not regenerate it | 2 (a login fails until the restart, open sessions stay) | command only: kaniop's own period restarts no reader |
| `kanidm.admin-passwords` (`admin`, `idm_admin`) | `lab`, the tests, kaniop | kaniop writes new ones when their Secret is deleted (confirmed on kaniop 0.19.0) | 1 | command only |
| `cloudflare.cert-manager`, `cloudflare.external-dns` | cert-manager, external-dns | no overlap for the readers: `-replace` of the `cloudflare_account_token` destroys the old token in the same apply, before `infra-secrets` pushes the new one; cert-manager reads the token at each DNS01 solve and retries, external-dns restarts (env variable) and retries on its next sync (amendment 1; to confirm in production) | 1 | 90 days, through `make infra env=production` (decision 5) |
| `cloudflare.tunnel` | cloudflared | Cloudflare's token refresh: `-replace=random_password.tunnel_secret` updates the same Tunnel in place (`PATCH .../cfd_tunnel/<id>` with a new `tunnel_secret`; same id, same record); connected cloudflared keeps serving until it restarts with the new `credentials.json` (to confirm in production) | 2 with one replica (the public routes drop for the restart); seamless with two | command only |
| `backup.r2-token` (`velero/repository`) | Velero | no overlap for the reader, like Cloudflare's: one apply replaces the token; a failed upload is retried by the next schedule; never at 02:00, the daily backup (to confirm in production) | 1 | 90 days, like Cloudflare's |
| `backup.kopia-password` (`velero/encryption`) | Velero's repositories | never: Velero uses one password for every repository and has no change-password, so a new value makes every existing repository unreadable; Kopia's own change-password only re-wraps the key, and the data keys stay the same, so it gains nothing against a leak. After a leak: new repositories (new password, new prefix) while the old ones age out, by hand | — | — |
| `renovate.github-app-key` | Renovate's initContainer | overlap: a GitHub App holds several private keys; generating one is manual (no API): `lab` stops and gives the steps, then deletes the old key | n/a | command only |
| `forgejo-db` (CloudNativePG's `forgejo-db-app`) | Forgejo | CloudNativePG's way: write the new password (and the keys derived from it: `uri`, `fqdn-uri`, `jdbc-uri`, `fqdn-jdbc-uri`, `pgpass`) into `forgejo-db-app`; the instance manager applies it to the role; restart Forgejo right after (it reads the password at start; its open connections bridge the gap) | 1 (2 at worst: a request fails while a new connection is refused) | command only |
| `telegram.bot-token` (new, decision 7) | Alertmanager (`bot_token_file`, read at every notification) | BotFather's `/revoke` (manual, no overlap), then the new token in OpenBao; no Alertmanager restart | 1 (Alertmanager retries; ntfy still delivers) | command only |
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
an annotation, chosen by the builder. `lab secrets list` shows the last rotation. There is no metric per secret
(amendment 2): the scheduled Job's own kube-state-metrics series raise the alerts.

### 7. Reporting: alerts through ntfy and Telegram

- A failed rotation raises an alert through Alertmanager like any other, e.g. `SecretRotationFailed`.
- A rotation more than a margin past its due date raises `SecretRotationOverdue`.
- A successful one is only logged and recorded; it sends no message.
- Alertmanager gains a second receiver, Telegram (`telegram_configs`), next to ntfy for every alert, so the
  existing alerts (`VeleroNoRecentBackup` and the others) also reach people while ntfy is down.
  - The bot token and the chat id live in OpenBao, never in git (the chat id identifies a person), and the person
    writes them with the admin role, as `docs/how-to-guides/alerts.md` will say.
  - Alertmanager reads them as files (`bot_token_file`, `chat_id_file`, both read at every notification), mounted
    from an ExternalSecret like the ntfy token: no rendered Alertmanager config Secret.
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

## Amendments

From Task 1's spike on the sandbox (2026-10-23; the evidence is in the plan's ledger, never values). Each changes the
catalog above or a decision:

1. **OpenTofu's Cloudflare and R2 tokens are class 1, not an overlap.** `cloudflare_account_token` has no in-place
   roll, so a new value is a `-replace`, and one apply destroys the old token before `infra-secrets` pushes the new
   one and the readers switch, `create_before_destroy` or not. The readers recover alone: cert-manager reads the
   token at each DNS01 solve and retries a failed challenge (a renewal starts weeks before expiry); external-dns reads
   it as an environment variable, so it restarts, and it retries on its next sync; Velero retries at its next
   schedule (never rotate at 02:00, its daily run). This comes from the provider's, cert-manager's and external-dns's
   source; no plan ran against production's state. It joins the production checklist: confirm at the first
   production rotation.
2. **No metric per secret.** Decision 6 dropped the per-secret Prometheus metric. Failures and overdue rotations come
   from the scheduled Job and CronJob through kube-state-metrics (a failed Job, a CronJob without a recent success),
   and `lab secrets list` shows each secret's last rotation and due date.
3. **Woodpecker's agent secret becomes per-agent tokens.** Changing the shared `WOODPECKER_AGENT_SECRET` never revokes
   the old value: a system agent stores the secret it registered with as its own token, and the server accepts that
   token from any agent. On the sandbox the agent's stored token was an earlier value, and it still authenticated. So
   the server's shared secret is dropped (empty) and every agent gets its own token from the admin API, which gives
   an official overlap: a new token beside the old, then the old agent deleted. Agent pods that share one token share
   one agent, so each replica needs its own token. `lab` needs a Woodpecker admin token for this (the admin is
   Forgejo's site admin, `WOODPECKER_ADMIN`). A running step pod carries the label `woodpecker-ci.org/step`.
4. **paperless's secret key has no fallback.** paperless 3.3 does not expose `SECRET_KEY_FALLBACKS`, so a new key
   ends every session (class 2). The key also signs paperless's Celery messages and its classifier file: restart
   while no task is queued; the classifier is retrained by its hourly task.
5. **kaniop's client secrets rotate by annotation, not by period.** `kaniop.rs/force-secret-rotation` on a
   `KanidmOAuth2Client` regenerates its secret once; deleting the Secret only writes the same value again. Only
   Argo CD reads the new secret live; Grafana failed every login (401 at the token exchange) until it restarted, and
   Actual Budget, paperless and Forgejo read it at start too. kaniop's `secretRotation` period restarts nobody, so it
   is not used: `lab` annotates, then restarts the readers.
6. **CloudNativePG's password is written into its own Secret.** The instance manager applies a new `password` in
   `forgejo-db-app` to the role within seconds, but leaves the derived keys (`uri`, `pgpass`, ...) stale, so `lab`
   writes them all. Forgejo keeps its open connections, so a restart right after makes it class 1.
7. **Kopia's repository password is never rotated.** Velero has one password for every repository and no
   change-password; Kopia's own change-password keeps the data keys, so it gains nothing against a leak. A leak is
   handled by new repositories while the old ones age out, by hand.
8. **The Tunnel's secret rotates in place.** Replacing `random_password.tunnel_secret` updates the same Tunnel
   (Cloudflare's token refresh, same id and record); connected cloudflared keeps serving until it restarts. Class 2
   with today's single replica, seamless with two. To confirm in production.
9. **Alertmanager needs no restart.** It reads `credentials_file`, `bot_token_file` and `chat_id_file` at every
   notification; ESO's refresh and kubelet's volume sync carry a new value. A rotation changes OpenBao and forces the
   ExternalSecret's refresh: ESO puts back a hand edit of its Secret at once.
10. **ntfy's overlap is confirmed, with restarts.** ntfy accepts two tokens for one user, and drops a token no longer
    listed from `auth.db` at its next start; each step of the overlap is an ntfy restart (`NTFY_AUTH_TOKENS` is an
    environment variable). `ntfy.alerts-password` has no runtime reader.
11. **No class 3 secret.** None of the secrets the spike covered loses data or stays stuck after a failed rotation.

## Left to the builder

The catalog's format, the record's location, the metric's name, the CronJob's namespace and RBAC, the overdue
margin, and the order `--all` uses. Pick sensible defaults and state them in the plan.
