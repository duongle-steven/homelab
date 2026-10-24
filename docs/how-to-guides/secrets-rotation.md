# Rotating secrets

Every secret the homelab makes is in a catalog (`cli/internal/rotate/catalog.yaml`) that says where it lives, who
reads it, how it is replaced and how often. `lab` replaces it by its own method, with no outage where the other side
allows two values at once (several tokens per user, a new token before the old one goes), and otherwise with the
shortest one it can: a restart of the readers, after waiting for Woodpecker to have no running pipeline. The design is
[step 11b's](../superpowers/specs/2026-10-24-secrets-rotation-design.md).

## What is due

```sh
go -C cli run ./cmd/lab secrets list                 # every secret: its method, class, period, last rotation, next due date
```

`make infra env=production` ends by naming OpenTofu's secrets that are due (Cloudflare's tokens, R2's): only the
workstation can rotate them, since the cluster holds no Cloudflare credentials.

## Rotate one, or all

```sh
go -C cli run ./cmd/lab secrets rotate grafana.admin    # one (or several names)
go -C cli run ./cmd/lab secrets rotate --all            # assume everything leaked
```

`lab` prints the plan first: what restarts (a workload with one replica stops briefly), who must act, and what it
skips with the reason (never rotated, another environment's, a person's step). It wants a completed off-site backup
from the last 30 hours (a day, plus the daily run still copying at 03:00), so a failed rotation can be restored; `--skip-backup-check='<reason>'` overrides it and keeps
the reason. It stops at the first failure and says what is done and what is left; running the same command again
resumes an interrupted rotation where it stopped, without making a third value. A rotation by a person and the
CronJob's never run at once on one secret (a Lease `secrets-rotation/rotate-<name>`).

Some steps need a person, and `lab` stops to give them:

- **Renovate's GitHub App key:** generate a new key in the App's settings (GitHub has no API for it), give it to
  OpenTofu (`docs/how-to-guides/renovate.md`), run `make infra env=production`, then delete the old key.
- **Telegram's bot token:** BotFather's `/revoke`, then write the new one ([Alerts](alerts.md)).
- **ntfy's phone token and password:** people re-enter them in their apps; the old token stays valid until a person
  confirms they did (`lab secrets rotate ntfy.phone-token` again on a terminal).

Never rotated, with the reason in the catalog: OpenBao's unseal key (it holds every secret), WireGuard's key (every
peer has it) and Kopia's repository password (Velero cannot change it; after a leak, new repositories). The state
passphrase has its own script (`scripts/rotate-state-passphrase`).

## The daily rotation

The CronJob `secrets-rotation/secrets-rotation` runs `lab secrets rotate --scheduled` at 03:00 (in
`global.timezone`): the entries with a period (90 days) whose turn came, counted from their creation until their first
rotation, and the interrupted ones. It never runs manual or OpenTofu entries, and leaves an interrupted rotation by
command to a person. Its access is only what those entries need: a role per namespace and OpenBao's role `rotator`
(`lab secrets check` keeps both in step with the catalog).

- A failed run alerts (`SecretRotationFailed`, through ntfy and Telegram): `kubectl -n secrets-rotation logs
  job/<job>` names the secret; fix the cause, then `lab secrets rotate <name>` finishes it.
- No successful run for three days alerts (`SecretRotationOverdue`).
- Production's image comes from GitHub Actions (`lab-image`, on `master` when `cli/` changes): set its digest in
  `platform/secrets-rotation/values.yaml` after the release; the CronJob stays suspended until then.

## A new secret

Add its entry to the catalog in the same change: `lab secrets check` (in `scripts/ci-check`) fails while a generated
secret, an `infra-secrets` path, a confidential Kanidm client or a Forgejo token has none.
