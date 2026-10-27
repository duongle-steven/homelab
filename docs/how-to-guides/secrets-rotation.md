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

`make infra env=production` ends by naming the secrets due for rotation from the workstation (`lab secrets list --due
--workstation`): OpenTofu's (Cloudflare's tokens, R2's), since the cluster holds no Cloudflare credentials, and
Forgejo's `deploy` and `argocd` tokens, whose rotation needs Forgejo's admin password, which the CronJob never holds. `lab` shows the unit's plan with the
replacement and applies it only once you approve: anything else pending in that unit is applied with it.

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

### A run that died

A run killed with its node (a power cut) or one whose release found the API server restarting leaves its Lease, and
the next run is refused, naming the holder, until the Lease expires (the run's 30-minute wait plus 5 minutes). A run
that ends while the API server is back within two minutes releases its lock itself. When you know that run is dead,
remove its lock as `tofu force-unlock` does, then resume:

```sh
go -C cli run ./cmd/lab secrets force-unlock ntfy.alerts-token steven@workstation/12345   # the holder the refusal printed
go -C cli run ./cmd/lab secrets rotate ntfy.alerts-token                                  # resumes from the recorded phase
```

`force-unlock` shows the holder and its times and asks first (`--yes` without a terminal). It refuses another holder:
a live run (the CronJob's, or someone else's) keeps its lock.

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
rotation, and the interrupted ones. It never runs manual or workstation entries (OpenTofu's, Forgejo's tokens), and leaves an interrupted rotation by
command to a person. Its Kubernetes access names what those entries need (each namespace's ExternalSecrets, workloads
and Woodpecker's agent claims) and OpenBao's role `rotator` their paths; `lab secrets check` keeps both in step with
the catalog. It never holds Forgejo's admin password (which reaches `ops/homelab`, what Argo CD deploys): taken over,
it reaches the scheduled entries' secrets (ntfy's, zot's `ci` password, Woodpecker's), not the cluster.

- A failed run alerts (`SecretRotationFailed`, through ntfy and Telegram): `kubectl -n secrets-rotation logs
  job/<job>` names the secret; fix the cause, then `lab secrets rotate <name>` finishes it.
- No successful run for three days alerts (`SecretRotationOverdue`).
- Production's image comes from GitHub Actions (`lab-image`, on `master` when `cli/` changes): set its digest in
  `platform/secrets-rotation/values.yaml` after the release; the CronJob stays suspended until then.

## A new secret

Add its entry to the catalog in the same change: `lab secrets check` (in `scripts/ci-check`) fails while a generated
secret, an `infra-secrets` path, a confidential Kanidm client or a Forgejo token has none.
