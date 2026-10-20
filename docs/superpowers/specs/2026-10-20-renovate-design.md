# Step 10d: Renovate on GitHub through our own GitHub App

Status: design approved in chat 2026-10-20, written spec awaiting review. Branch: `feat/renovate` from `dev`.

## Goal

Renovate runs in the production cluster and opens pull requests into `dev` on GitHub, authenticated as a GitHub App
of our own installed only on chosen repositories, so its pull requests run `checks` (and, labelled `sandbox`, the
sandbox) like anyone's. No long-lived token: the App's private key stays in OpenBao and Renovate gets installation
tokens that live one hour.

## Context

- `platform/renovate` exists but targets Forgejo: chart 31.97.3, `platform: forgejo`, a token `renovate/forgejo` that
  nothing writes; `renovate.json5` extends the old `config:base`. Egress allows ports 80 and 443 for `renovate`.
- Upstream `master` ran Renovate in the cluster against Gitea with a token `scripts/hacks` minted through Gitea's
  admin API; upstream `nixos` has only `renovate.json5` and no bot pull requests on GitHub.
- Upstream `nixos`'s secrets are all created by OpenTofu (provider resources, `random_password`) or `lab`, never
  typed in. A GitHub App's private key cannot be created through an API (only in GitHub's UI, or the manifest flow,
  which also needs a browser): it is the first secret a person brings in, next to the state passphrase (the only
  secret that must be kept). It can be regenerated on GitHub at any time.
- External Secrets 2.12 has the generator `GithubAccessToken` (App ID, installation ID, private key from a Secret,
  optional `permissions` and `repositories`).
- Production does not exist before step 12; the sandbox takes no secret from outside.
- Today every push to `dev` runs the whole sandbox suite and none cancels another, and the maintainer labels every
  own pull request `sandbox` although `make dev` already ran the same scope locally.

## Decisions

1. **Key path (B'):** `infra/_modules/secrets` takes the App's private key once, from the file
   `HOMELAB_GITHUB_APP_KEY` names (read by `infra/production/secrets`), and keeps it in its encrypted state on R2
   (a `terraform_data` whose stored value later runs keep when the variable is empty; a new key replaces it with
   `-replace`). It joins the `secrets` map as `renovate/github-app` (property `private-key`), so it reaches OpenBao
   like the WireGuard key (`infra-secrets` → `platform/infra-secrets`). The file can be deleted after the first
   apply.
2. **Token (A):** an ExternalSecret in namespace `renovate` copies `renovate/github-app` into a Secret; a
   `GithubAccessToken` generator (App ID and installation ID from `platform/renovate/values.yaml`, which ESO needs as
   literals; neither is secret) mints an installation token with only `contents`, `pull_requests`, `workflows`,
   `issues` (write) and `checks`, `statuses` (read), for repository `homelab` only; a second ExternalSecret writes it
   into `renovate-secret` (`RENOVATE_TOKEN`) with `refreshInterval: 30m`, so a run starts with at least 30 minutes
   left. ESO mints a new token at every refresh; old ones expire on their own.
3. **The App** (created by hand, documented): `homelab-renovate`, permissions Contents, Pull requests, Workflows,
   Issues (the Dependency Dashboard) read and write, Commit statuses and Checks read, Metadata read; no webhook;
   installed on "Only select repositories": `homelab`. Renovate's `autodiscover` lists exactly the installation's
   repositories; `autodiscoverFilter: ["duongle-steven/*"]` guards against an installation elsewhere. Adding a
   repository is a change on GitHub only.
4. **Renovate:** chart 46.372.0 (the latest), CronJob daily at 09:00 in `global.timezone` (`timeZone`), production
   only (not among the sandbox's components). Global config in `values.yaml`: `platform: github`,
   `autodiscover`, `autodiscoverFilter`, `onboarding: false`, `requireConfig: required`, `gitAuthor` the App's bot
   (`<slug>[bot] <<bot user id>+<slug>[bot]@users.noreply.github.com>`, the id from `gh api /users/<slug>[bot]`).
5. **Repository config** (`renovate.json5`): `config:recommended`, `baseBranches: ["dev"]`, minor and patch grouped
   as today, majors separate, `lockFileMaintenance` weekly (`flake.lock`), `labels: ["sandbox"]`, no automerge. The
   default managers cover Helm charts and values, Go modules, OpenTofu providers and SHA-pinned actions.
6. **Sandbox runs:** a push to `dev` cancels the sandbox run of an older push still in progress (only the newest
   `dev` is tested in full; an older commit can be tested on demand with `revision`). Own pull requests are labelled
   `sandbox` only when they change what the local sandbox cannot prove (the first install, CI); Renovate's always
   are, since nobody runs `make dev` for them.
7. **Egress:** `renovate` keeps port 443 only.

## Failure handling

- A wrong App ID, installation ID, key or permission: the generator fails and `renovate-secret` is not written; the
  CronJob's pod waits for its Secret. The proof below catches this before production.
- A run longer than the token's remaining life fails part-way; the next day's run continues (Renovate is
  idempotent).
- OpenBao loses its data (no backup before step 11): the next `make infra env=production` writes the key again from
  the state. A lost or leaked key: generate a new one on GitHub, delete the old, apply with `-replace`.

## Proof (once, not a permanent test)

On the sandbox, with the key written to its OpenBao by the admin role: apply `platform/renovate` rendered for
production into namespace `renovate`, run its CronJob once (`kubectl create job --from=cronjob/...`), and see that the
generator mints a token (our IDs, key and permissions match the App), Renovate runs with our config and opens a
pull request into `dev`, and that pull request runs `checks` and `sandbox-label`. Then delete the namespace and the key
from the sandbox. Whether ESO refreshes on time is ESO's concern: `refreshInterval` is checked offline.

## Tests

- Offline (basic checks): `platform/renovate` rendered for production has the generator's permissions and
  repository, `refreshInterval: 30m`, the CronJob's `timeZone`, and no Forgejo leftovers; the sandbox does not deploy
  it; `renovate-config-validator` (nixpkgs `renovate`, in the dev shell and `scripts/lint`) accepts `renovate.json5`
  and the global config.
- State test: `infra/_modules/secrets` keeps the stored key when a later apply passes none, and `-replace` takes a
  new one.

## Documentation

- A how-to for Renovate: create and install the App, the first `make infra` with `HOMELAB_GITHUB_APP_KEY`, rotating
  the key, adding a repository.
- CLAUDE.md: 10d done with the App's real permissions; the production checklist (Renovate only rendered and proven
  once on the sandbox; App and installation IDs in `values.yaml`; the key's path); the `sandbox` label rule; `dev`
  runs cancel older ones. The egress table without port 80 for `renovate`.

## Out of scope

- Renovate on Forgejo repositories (blog and others: step 10e), automerge, Mend's hosted Renovate.
- Reusing a `dev` result for the `dev` → `master` check.
