# Step 8c: OpenTofu state on Cloudflare R2, encrypted

Status: design approved in chat 2026-10-18, written spec awaiting review. Branch: `feat/state-r2` (PR into `dev`).

## Goal

Production's OpenTofu state leaves the workstation for a Cloudflare R2 bucket, so losing the workstation no longer loses
it. Every environment's state is encrypted at rest with OpenTofu's state encryption. Cloudflare credentials for OpenTofu
come from the `cf` CLI's OAuth login, as upstream `nixos` does, instead of a long-lived API token kept in
`~/.config/homelab/cloudflare.env`.

## Context

- `infra/root.hcl` gives every unit a local backend under `.state/<env>/<unit>/terraform.tfstate`, readable by the user
  only (an `after_hook` runs `chmod -R go-rwx .state`). Its comment says "Local state until step 8".
- Production state today: only `infra/production/cloudflare` has a non-empty state. It holds the tunnel secret
  (`random_password`), the tunnel, its DNS record and two `cloudflare_account_token`s. `argocd`, `openbao` and `secrets`
  are empty, because production has not been built yet.
  - `infra/production/openbao` will hold the OpenBao unseal key, and `secrets` the WireGuard private key. These are the
    states that must never be lost.
- The sandbox's units (`cluster`, `openbao`, `secrets`, `argocd`) are rebuilt by `make dev-clean && make dev`, so their
  state is disposable.
- `.envrc` loads `~/.config/homelab/cloudflare.env` (`CLOUDFLARE_API_TOKEN`), which
  `docs/installation/production/external-resources.md` tells the user to create. CLAUDE.md,
  `docs/concepts/secrets-management.md` and `docs/installation/post-installation.md` mention it.
- Upstream `nixos` `infra/root.hcl`:
  - Credentials: `cf --quiet auth whoami` (with `CLOUDFLARE_API_TOKEN`, `_API_KEY` and `_EMAIL` unset) returns JSON
    with `authSource` ("OAuth token from <file>") and `accounts`. The OAuth token is read from that file
    (`.oauth_token`).
  - Backend: `http`, address
    `https://api.cloudflare.com/client/v4/accounts/<account>/r2/buckets/homelab-<env>-tfstate/objects/<unit>/tfstate.json`,
    `update_method = "PUT"`, header `Authorization: Bearer <token>`. No lock.
  - The same token becomes `CLOUDFLARE_API_TOKEN` for `plan`, `apply`, `import` and `refresh`. Its Cloudflare module
    creates `cloudflare_account_token`s with it, so the OAuth scopes cover ours.
  - `init` runs `-reconfigure`, and a `before_hook` (`lab infra state ensure`) creates the bucket. `destroy` is refused.
  - No state encryption.
- `flake.nix` in upstream `nixos` builds `cf` (`cloudflare/cf`, pnpm workspace `cf`) with `stdenv.mkDerivation` until
  nixpkgs packages it (NixOS/nixpkgs#569193). It pins `1.0.0-beta.13`; the latest release is `cf@1.0.0-beta.18`, and
  every release so far is a beta.
- OpenTofu 1.11 (this flake's `unstable.opentofu`) supports state and plan encryption: `key_provider "pbkdf2"` with a
  passphrase of at least 16 characters, `method "aes_gcm"`, an optional `fallback` for migrating from unencrypted state,
  and `enforced = true`. The encryption block may reference variables.

## Decisions

| Topic | Decision |
|---|---|
| Which state goes to R2 | Production only. The sandbox stays local in `.state/sandbox/` (disposable, and `make dev` must not need a Cloudflare login) |
| Bucket | `homelab-production-tfstate`, one object per unit: `<unit>/tfstate.json` (upstream's layout) |
| Backend | `http` with upstream's address, `update_method = "PUT"` and a Bearer header with the OAuth token. No lock: one operator, and R2's object API has no lock endpoint |
| Credentials | `cf --quiet auth whoami`, read once per Terragrunt run (`run_cmd --terragrunt-global-cache --terragrunt-quiet`), with Cloudflare's env variables unset and `CF_SEND_TELEMETRY=false`. Account: `CLOUDFLARE_ACCOUNT_ID` if set, else the first account. Production only: sandbox units never call `cf` |
| Provider token | The OAuth token becomes `CLOUDFLARE_API_TOKEN` for `plan`, `apply`, `import`, `refresh` and `destroy` of production units. `~/.config/homelab/cloudflare.env` and its `.envrc` line are removed |
| Bucket creation | `scripts/ensure-state-bucket <account> <bucket>`: a POSIX script that uses `curl` with the OAuth token. It creates the bucket when `GET` says it is missing, and does nothing otherwise. It runs as a `before_hook` on production's `init` |
| `cf` CLI | Built in `flake.nix` from upstream's derivation, pinned to `cf@1.0.0-beta.18` (no stable release exists; the comment says so) with its source and pnpm hashes |
| Encryption | Every environment. `key_provider "pbkdf2"` with `passphrase = var.state_passphrase` (the generated file also declares that variable, sensitive), `method "aes_gcm"`, applied to `state` and `plan`, `enforced = true`. Terragrunt generates it as `encryption.tf` |
| Passphrase | `HOMELAB_STATE_PASSPHRASE` in `~/.config/homelab/state.env` (mode 600, loaded by `.envrc`), plus a copy in the user's password manager. Terragrunt passes it as `TF_VAR_state_passphrase` from the environment, so it never lands in a generated file. A missing variable fails before any OpenTofu command. The user creates it; it is never printed, logged or committed |
| Migration, sandbox | `make dev-clean`, then `make dev` with encryption (fresh state, no fallback needed) |
| Migration, production | One time, with the user: `! cf auth login`, the user confirms, then a temporary `fallback { method = method.unencrypted.plain }` in `encryption.tf`. Per unit, `terragrunt init -migrate-state` moves the local state to R2, encrypted. `terragrunt plan` must then show no changes for `cloudflare`, and the empty units must stay empty. Afterwards the fallback is removed, and the local `.state/production/` files are kept until the user deletes them |
| `destroy` | Production `prevent_destroy` guards stay as they are. Sandbox `destroy` keeps working (`make dev-clean`) |
| `.state/` permissions hook | Kept for the sandbox; production writes nothing under `.state/` any more |

## Failure handling

- `cf` not logged in: `run_cmd` fails, and Terragrunt prints `cf`'s error. The docs say to run `cf auth login`.
- No `HOMELAB_STATE_PASSPHRASE`: `get_env` without a default fails at parse time with the variable's name.
- Wrong passphrase: OpenTofu refuses to decrypt and changes nothing.
- An expired OAuth token: `cf auth whoami` refreshes it, as upstream relies on.
- R2 unreachable: `init`, `plan` or `apply` fail. Nothing is written locally.

## Tests

The flow needs a Cloudflare login and the production account, so no test runs it unattended. Checks:

- `make -C infra check` (HCL validity and formatting, with the new generate blocks).
- Offline: `scripts/ensure-state-bucket` against a fake `curl` on `PATH`, written as a Go test in `test/` like
  `TestIstioCNIWatchdog`:
  - the bucket is missing, so it is created;
  - the bucket exists, so nothing is created;
  - the API answers an error, so the script exits non-zero with the message.
- Sandbox: `make dev-clean && make dev` passes with encrypted local state. A check that `.state/sandbox/*/terraform.tfstate`
  is encrypted (the JSON has `encrypted_data`, and no resource attributes in clear text) is part of the plan's
  verification.
- Production, with the user: after the migration, `terragrunt plan` in `infra/production/cloudflare` shows no changes;
  the R2 object exists and is encrypted (read back through the API, it shows `encrypted_data`).

## Documentation

- `docs/installation/production/external-resources.md`:
  - replaces the API-token steps with `cf auth login`;
  - adds creating the passphrase (`openssl rand -base64 32` into `~/.config/homelab/state.env` with `chmod 600`, and the
    password manager copy);
  - says what losing the passphrase means: the state cannot be read, and the unseal key and WireGuard key inside it are
    lost.
- CLAUDE.md:
  - Sandbox bullet: "State is local in `.state/` until step 8" becomes "sandbox state is local in `.state/sandbox/`,
    production's is on R2; both encrypted".
  - Production checklist: the `cloudflare.env` sentence becomes the `cf` login, and the OpenBao bullet's "its unseal key
    lives in `.state/production/openbao` until 8c" becomes R2.
  - Roadmap: 8c done.
- `docs/concepts/secrets-management.md` and `docs/installation/post-installation.md`: the `cloudflare.env` mentions
  become the `cf` login and `state.env`.

## Out of scope

- A state lock (one operator).
- Backups of the R2 bucket, which are step 11.
- Rotating the passphrase. It can be done later with OpenTofu's fallback; the docs name the procedure.
