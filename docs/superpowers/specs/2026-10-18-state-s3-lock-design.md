# Step 8d: production state through R2's S3 API, with a lock

Status: design approved in chat 2026-10-18, written spec awaiting review. Branch: `feat/state-s3-lock`, stacked on
`feat/test-scopes` (PR #39); its PR targets `dev` once #39 is merged.

## Goal

Production's OpenTofu state moves from the `http` backend (R2's REST object API, Bearer OAuth token in `backend.tf`,
no lock) to OpenTofu's `s3` backend on R2's S3 endpoint with a lock file, as upstream `nixos` does. Expiring R2
credentials come from `lab infra state credentials`, ported from upstream, so no token is written into `backend.tf`.
The `infra/production/cloudflare` unit works through `cf`'s OAuth login, and the docs say how to get the scope it
needs.

## Context

- Step 8c shipped the `http` backend. Two problems are now known:
  - `infra/production/cloudflare` returned 403 on account API tokens. The spike on 2026-10-18 found the cause: on the
    `cf auth login` consent page, **Account API Tokens** is an option the user must enable; it is off by default.
    With it enabled, `cf auth whoami` lists `account_api_tokens:create` (476 scopes instead of 475), the token API
    answers (listing account tokens and permission groups: 200; creating and deleting a 10-minute R2 token: 200), and
    `terragrunt plan` in `infra/production/cloudflare` reports no changes.
  - The OAuth token is part of the generated `backend.tf`. After a new login (or a refresh), `plan` fails with
    "Backend initialization required: please run tofu init" until someone runs `terragrunt init`. The 8c reviewer
    reported this; the fake-R2 test did not reproduce it, the production unit did on 2026-10-18.
- Upstream `nixos` (`infra/root.hcl`, `cli/internal/cli/{cloudflare,infra_state}.go`, `cli/internal/state/r2.go`):
  - `remote_state` with backend `s3`: bucket `homelab-<env>-tfstate`, key `<unit>/tfstate.json`, region `auto`,
    endpoint `https://<account>.r2.cloudflarestorage.com`, `use_lockfile = true`, profile `r2-state` from a generated
    `r2-state.config` whose `credential_process` is `lab infra state credentials --account-id … --bucket …`, and the
    `skip_*` settings R2 needs;
  - `lab infra state credentials`: reads the `cf` OAuth login, finds the permission group "Workers R2 Storage Bucket
    Item Write", deletes this credential's expired tokens, creates an account token for that bucket only that expires
    in one hour, waits until R2 accepts it (signed `HEAD` on the bucket), and prints AWS credential-process JSON
    (access key = token id, secret = SHA-256 of the token value);
  - `lab infra state ensure`: creates the bucket when it is missing (cloudflare-go `GetR2Bucket`/`CreateR2Bucket`);
  - dependencies: `github.com/cloudflare/cloudflare-go` v0.115.0, `github.com/aws/aws-sdk-go-v2` (SigV4 signer).
- This fork's `cli/` (step 14a) has the cobra root and `lab test scope`; the Makefile runs it with `go run`.
- The R2 object written by the `http` backend, `homelab-production-tfstate/cloudflare/tfstate.json`, is the key the
  `s3` backend uses for the same unit, so the migration moves no data.

## Decisions

| Topic | Decision |
|---|---|
| Backend | Production: `s3` on `https://<account>.r2.cloudflarestorage.com` (upstream's settings, plus `use_path_style = true` so a fake endpoint works in tests; R2 accepts it), key `<unit>/tfstate.json`, `use_lockfile = true`. The sandbox stays `local` |
| Credentials | A generated `r2-state.config` with `credential_process` running `lab infra state credentials --account-id <account> --bucket <bucket>`; nothing secret lands in a generated file |
| Running `lab` | `go -C <repo>/cli run ./cmd/lab …` (as in 14a: no Nix package yet). `HOMELAB_LAB` overrides the command (tests) |
| Port | Upstream's `cli/internal/state` (`r2.go` and its tests) and `cli/internal/cli/{cloudflare,infra_state}.go` with tests, imports rewritten to `github.com/duongle-steven/homelab/cli`; `infra` and `infra state` commands join the root. Behaviour unchanged except: `--account-id` is always passed by `root.hcl` (the account `scripts/cloudflare-credentials` picks), so upstream's "exactly one account" rule only applies without it |
| Bucket creation | `lab infra state ensure` replaces `scripts/ensure-state-bucket` (and `TestStateBucket`) in the `init` hook |
| Provider token | Unchanged: `scripts/cloudflare-credentials` gives the OAuth token to the Cloudflare provider through `CLOUDFLARE_API_TOKEN` for `plan`, `apply`, `import`, `refresh`, `destroy` |
| `init` | Production keeps `-reconfigure` (the backend changes from `http` to `s3` once; afterwards the config is stable) |
| Migration | One time, with the user: `terragrunt init` in each production unit switches to `s3`; the existing object is read as is (already encrypted); `state list` shows 8 resources in `cloudflare` and `plan` shows no changes; `argocd`, `openbao`, `secrets` stay empty. No `HOMELAB_STATE_MIGRATE` needed |
| Docs for the login | `cf auth login`: on the consent page, enable **Account API Tokens** (off by default); `cf auth whoami` must list `account_api_tokens:create`, otherwise `cf auth login --force` and enable it |
| 8c's warning | The "`infra/production/cloudflare` cannot plan until 8d" warning and its `--filter '!cloudflare'` workaround leave CLAUDE.md and the docs |
| `lab auth login` | Not added: `cf auth login` is enough once the option is enabled |

## Failure handling

- `cf` not logged in, or the login lacks `account_api_tokens:create`: `credential_process` fails, OpenTofu reports
  the credential error, and `lab` says to run `cf auth login` (upstream's messages, plus the consent-page hint).
- R2 slow to accept a new token: `lab` retries the signed `HEAD` for up to 40 seconds (upstream).
- A stale lock (an interrupted `apply`): `terragrunt force-unlock <id>` (OpenTofu's message names it); the docs say so.
- Token pile-up: every `credential_process` call creates a one-hour token; the next call deletes this credential's
  expired ones (upstream's pruning).

## Tests

- Ported upstream unit tests for `state` (`r2_test.go`, `credentials_test.go`, `r2_activation_test.go`) and
  `cli/cloudflare_test.go`, adjusted to the import path; `make -C cli test`.
- Offline OpenTofu tests (`test/state_*`): a small fake S3 server in Go (object GET/PUT/HEAD/DELETE, conditional PUT
  for the lock file, ListObjectsV2) replaces the fake R2 `http` server; `HOMELAB_LAB` points `credential_process` at a
  stub printing static credentials and `HOMELAB_STATE_ENDPOINT` at the fake. They prove:
  - production state lands encrypted in the fake bucket, and a lock file appears while a command runs;
  - a changed OAuth token leaves `backend.tf` unchanged and `plan` needs no `init` (the 8c bug);
  - rotation still skips units without an object and re-encrypts the others;
  - `ensure` runs on `init`.
- Production, with the user: after the migration, `state list` (8) and `plan` (no changes) in `cloudflare`; the R2
  bucket holds `cloudflare/tfstate.json` (encrypted) and no stray lock after the commands finish.
- `test/scopes.yaml`: the new `cli/internal/state` and `cli/internal/cli` files are under `cli/**` (skipped: offline
  tests cover them); `infra/root.hcl` selects the whole suite.

## Documentation

- `docs/installation/production/external-resources.md`: the consent-page option, the `whoami` check, the warning and
  workaround removed, the lock (and `force-unlock`), where state lives (`s3` backend on R2, one object per unit plus a
  lock file during commands).
- CLAUDE.md: roadmap 8d (done); the Sandbox bullet's state sentence names the `s3` backend and its lock; the production
  checklist's Cloudflare sentence loses "cannot plan or apply until step 8d"; the OpenBao bullet's
  `--filter '!cloudflare'` note goes; the Ideas entry for the Go CLI lists `infra state ensure|credentials` as ported.
- `docs/superpowers/deferred-minors.md`: the 8b line about losing `.state/production/secrets` is reworded for state on
  R2 (losing the R2 object, or the passphrase).

## Out of scope

- The rest of upstream's `lab` (cluster, sso, user, secrets) and its Nix package (the CLI step).
- Backups of the R2 bucket (step 11).
