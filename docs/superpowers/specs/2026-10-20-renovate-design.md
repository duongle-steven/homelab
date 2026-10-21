# Step 10d: Renovate on GitHub through our own GitHub App

Status: design approved in chat 2026-10-20; decision 2 revised in chat 2026-10-21 (the token is minted in Renovate's
pod, not by ESO's generator, so ESO needs no internet). Branch: `feat/renovate` from `dev`.

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
- External Secrets 2.12 has the generator `GithubAccessToken`, but it runs in ESO's controller, which reads every
  Secret: with default-deny egress (step 6d-2) it would need the internet, so it is not used (decision 2).
- GitHub mints an installation token for a JWT signed with the App's key (`POST /app/installations/<id>/access_tokens`,
  optional `permissions`), and a token revokes itself (`DELETE /installation/token`). The Renovate chart has
  `cronjob.initContainers`, `cronjob.preCommand` (before `renovate`, in one `bash -c` with `set -e`), `extraVolumes` and
  `extraVolumeMounts`.
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
2. **Token, minted per run in Renovate's pod:** an ExternalSecret in namespace `renovate` copies
   `renovate/github-app` into the Secret `renovate-github-app` (ESO reads OpenBao as usual and needs no internet). The
   CronJob's initContainer `github-token` (Renovate's own image: node signs the JWT, no other image to pin; the script
   in a ConfigMap) mounts only that Secret, signs a JWT that lives 9 minutes, mints an installation token with only
   `contents`, `pull_requests`, `workflows`, `issues` (write) and `checks`, `statuses` (read) for every repository
   the App is installed on, and writes it to an `emptyDir` in memory. The Renovate container never mounts the key:
   its `preCommand` reads the token into `RENOVATE_TOKEN` and traps `EXIT` to revoke it (`DELETE
   /installation/token`), so the token ends with the run, failed or not (a killed pod's token expires within the
   hour). The pod has `automountServiceAccountToken: false`. App ID and installation ID live in
   `platform/renovate/values.yaml` (neither is secret). NetworkPolicies apply to the whole pod: `renovate` keeps 443
   (Renovate reaches many registries), `external-secrets` gets no internet.
3. **The App** (created by hand, documented): `duongle-lab-renovate`, permissions Contents, Pull requests, Workflows,
   Issues (the Dependency Dashboard) read and write, Commit statuses and Checks read, Metadata read; no webhook;
   installed on "Only select repositories": `homelab`. Renovate's `autodiscover` lists exactly the installation's
   repositories; `autodiscoverFilter: ["duongle-steven/*"]` guards against an installation elsewhere. Adding a
   repository is a change on GitHub only.
4. **Renovate:** chart 46.372.0 (the latest), CronJob daily at 05:00 in `global.timezone` (`timeZone`), so its pull requests and their sandbox runs are done before the working day, production
   only (not among the sandbox's components). Global config in `values.yaml`: `platform: github`,
   `autodiscover`, `autodiscoverFilter`, `onboarding: false`, `requireConfig: required`, `gitAuthor` the App's bot
   (`<slug>[bot] <<bot user id>+<slug>[bot]@users.noreply.github.com>`, the id from `gh api /users/<slug>[bot]`).
5. **Repository config** (`renovate.json5`): `config:recommended`, `baseBranches: ["dev"]`, minor and patch grouped
   as today, majors separate, `lockFileMaintenance` weekly (`flake.lock`), `labels: ["sandbox"]`, no automerge. The
   default managers cover Helm charts and values, Go modules, OpenTofu providers and SHA-pinned actions.
6. **Sandbox runs:** a push to `dev` cancels the sandbox run of an older push still in progress (only the newest
   `dev` is tested in full; an older commit can be tested on demand with `revision`): `sandbox.yaml`'s concurrency
   group is the workflow and branch for a push, and the run for a `workflow_dispatch`, so a manual run and a `dev`
   run never cancel each other. The other workflows' groups already start with the workflow's name and the pull
   request (`sandbox-label`: the pull request and the label; `sandbox-release`, `sandbox-retarget`: the pull
   request), so a label run on one pull request (ours or Renovate's) cancels only an older label run on the same
   pull request, never another pull request's run, a `dev` run or a release. Own pull requests are labelled
   `sandbox` only when they change what the local sandbox cannot prove (the first install, CI); Renovate's always
   are, since nobody runs `make dev` for them. Labelling needs Triage or more on the repository (today only the
   owner); the App labels through its Issues and Pull requests write permissions, and a label added by an App, unlike
   one added with `GITHUB_TOKEN`, triggers workflows.
7. **Egress:** `renovate` keeps port 443 only.

## Failure handling

- A wrong App ID, installation ID, key or permission: the initContainer fails with GitHub's status and message (never
  the key, the JWT or a token), the Job fails and Renovate does not start. The proof below catches this before
  production. The key not yet given: the pod waits for its Secret.
- A run longer than an hour fails part-way; the next day's run continues (Renovate is idempotent).
- OpenBao loses its data (no backup before step 11): the next `make infra env=production` writes the key again from
  the state. A lost or leaked key: generate a new one on GitHub, delete the old, apply with `-replace`.

## Proof (once, not a permanent test)

On the sandbox, with the key written to its OpenBao by the admin role: apply `platform/renovate` rendered for
production into namespace `renovate` and run its CronJob once (`kubectl create job --from=cronjob/...`) with
`RENOVATE_DRY_RUN=lookup`: the initContainer mints a token (our IDs, key and permissions match the App), Renovate
logs in and reads `homelab` with our config, and once the Job ends the token is revoked (a GitHub API call with it
returns 401; the token is read from the pod's log of a hash only, never printed). The real run that opens a pull
request into `dev` labelled `sandbox` waits for the next `dev` → `master` release: Renovate reads `renovate.json5`
from the default branch. Then delete the namespace and the key from the sandbox.

## Tests

- Offline (basic checks): `platform/renovate` rendered for production mounts the key only in the initContainer, the
  token's `emptyDir` in memory, `automountServiceAccountToken: false`, no `GithubAccessToken`, the CronJob's 05:00
  and `timeZone`; the mint script against a fake GitHub API signs a valid RS256 JWT for the App ID, asks for exactly
  the six permissions, writes the token, prints neither the token nor the key, and fails on an API error; and no Forgejo leftovers; the sandbox does not deploy
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
