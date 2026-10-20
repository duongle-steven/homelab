# Step 10c: production deploys by an explicit push

Status: design approved in chat 2026-10-20, written spec awaiting review. Branch: `feat/forgejo-deploy` from `dev`.

## Goal

Production receives only the commits someone deliberately deploys. GitHub stays where work happens (pull requests,
`checks`, the sandbox); production's Argo CD syncs `ops/homelab` on Forgejo, which changes only through
`lab infra forgejo push`, run from the workstation, and only for a commit on `origin/master` whose checks passed.
`make infra env=production` does the whole deploy in one command, the first install included, as upstream `nixos`'s
`make infra` does.

## Context

- Upstream `nixos` has `lab infra forgejo sync` (with `forgejo_admin`: org `ops`, an empty `ops/homelab`, a team
  `developers` with write, the Woodpecker OAuth client into OpenBao) and `lab infra forgejo push` (HEAD to
  `ops/homelab` `master` as `forgejo_admin`, through `GIT_ASKPASS`; no guard). Its `make infra` runs
  `terragrunt run --all apply`, then `sync`, then `push`. It reads OpenBao with the root token through the API
  server's service proxy, and OpenTofu installs Forgejo and OpenBao from their charts, so Argo CD reads Forgejo from
  the start.
- Here, Argo CD installs Forgejo and OpenBao from the GitOps repository, so production's first install reads the seed
  (`values-seed.yaml`, `global.seedRepository` on GitHub) until Forgejo serves `ops/homelab`. A Job `forgejo-seed`
  (`platform/forgejo/templates/seed-job.yaml`) copies the seed into `ops/homelab` once, as `forgejo_admin`.
  `infra/_modules/argocd/choose-values.sh` switches the bootstrap to `values.yaml` once a Forgejo pod runs, and never
  back.
- Argo CD reads `ops/homelab` with `forgejo_admin`'s password (`system/argocd-config/templates/forgejo-repository.yaml`,
  OpenBao `argocd/generated/forgejo-admin`, a second destination of `forgejo.admin` in `platform/global-secrets`).
- OpenBao has no root token: admins log in with the role `admin` as service account `openbao/openbao-admin`
  (audience `openbao`; `docs/concepts/secrets-management.md`).
- `cli/` (`lab`) has `test scope` and `infra state ensure|credentials` (cobra); `make infra env=production` refuses
  anything but `origin/master` without changes (`production-checkout`, `force=1` overrides).
- Forgejo users come from Kanidm (auto-registration; `homelab-admins` become Forgejo admins at each login). Nothing
  declares Forgejo's orgs, teams or repositories.
- Production does not exist yet: its nodes come in step 12. Before then the push can only reach the sandbox's Forgejo,
  which the sandbox's Argo CD does not read (it reads GitHub branch `sandbox`).

## Decisions

1. **Port upstream's commands into `cli/`** (Go: `forgejo-sdk`, OpenBao's API client, `client-go`), with unit tests
   against fakes and sandbox tests against the real Forgejo and OpenBao. Rejected: a Job doing the sync (shell, hard
   to test, unlike upstream) and shell scripts (tokens in logs, against the plan to grow `lab`).
2. **`lab infra forgejo sync|push|verify [--env production|sandbox]`**, `production` by default. The domain comes from
   `infra/<env>/cluster/config.json`, the kubeconfig is `infra/<env>/kubeconfig.yaml`, the checkout is the repository
   `lab` runs in (no `--source`, no `--domain`, no `--kubeconfig`). The guards apply to `production` only; `sandbox`
   exists for the tests.
3. **No seed Job.** `sync` creates an empty `ops/homelab`; the first `push` is the first deploy and passes the same
   guards. `choose-values.sh` switches only once Argo CD's own credential reads `ops/homelab`'s `master`.
4. **`sync` owns org `ops` and nothing else in Forgejo** (idempotent: what is right is kept, what is wrong is fixed,
   what is missing is created):
   - org `ops`; repository `ops/homelab`, private, default branch `master`, content never touched;
   - local users `deploy` and `argocd`, `restricted` (they see only repositories they collaborate on), created through
     the admin API with a random one-time password that is used to create their token and never stored;
   - collaborators on `ops/homelab`: `deploy` write, `argocd` read; no team has write (unlike upstream);
   - `master`'s branch protection: push allowlist with only `deploy`, force-push disabled for everyone (a protected
     branch cannot be deleted);
   - tokens: `deploy`'s `lab` (`write:repository`), `argocd`'s `argocd` (`read:repository`). A token stored in OpenBao
     that still authenticates (`GET /api/v1/user`) is kept; otherwise the token of that name is deleted and a new one
     created. They are written to OpenBao at `secret/forgejo/deploy` and `secret/argocd/forgejo` (property `token`);
     after a change `sync` force-syncs the ExternalSecret `argocd/argocd-repo-forgejo` and waits until its Secret
     holds the new token, so the `terragrunt apply` that follows (decision 9) can already switch to Forgejo.
   - `lab` reads `forgejo_admin`'s password from the Secret `forgejo/forgejo-admin` and logs in to OpenBao as
     `openbao/openbao-admin` (a TokenRequest with audience `openbao`, role `admin`) through the API server's service
     proxy; it revokes its OpenBao token when done. No token or password reaches an argument, the output or a log.
   - `sync` waits for Forgejo and OpenBao to be ready (a first install takes about 20 minutes) and fails after
     30 minutes.
5. **Guards** (`verify`; `push` runs them first), in order, stopping at the first failure:
   1. no uncommitted change to tracked files;
   2. `git fetch origin master`, HEAD equals `origin/master`;
   3. GitHub's API, without a token (the repository is `seed_repository` from `infra/production/cluster/config.json`):
      the latest check run `checks` on HEAD succeeded, and a pull request merged into `master` whose `merge_commit_sha`
      is HEAD has a latest check run `sandbox` on its head that succeeded. No such pull request (a direct push) fails.
      A check still running fails with "wait for it and run again"; an API error fails, never passes.
6. **`push`**: guards (production), then `git -c credential.helper= push https://git.<domain>/ops/homelab.git
   HEAD:refs/heads/master` with `deploy`'s token through `GIT_ASKPASS` and an environment variable, as upstream does.
   Never forced: a rollback is a revert through a pull request on GitHub, and Forgejo refuses a non-fast-forward
   push. If Forgejo's `master` already is HEAD, it says so and pushes nothing. In the sandbox, `lab` dials the node
   (`docker inspect`, `http.curloptResolve` for git, a dialer for the API) and trusts `homelab-dev-ca`, so it works
   on GitHub Actions without the localhost proxy. `push` serves only `ops/homelab`: other repositories (blog) reach
   Forgejo by mirror or by hand, decided in step 10e. No `--wait`; the history of `ops/homelab` is the deploy log.
7. **Argo CD's credential** `argocd-repo-forgejo`: username `argocd`, password from `argocd/forgejo` `token`.
   `forgejo.admin` loses its destination `argocd/generated/forgejo-admin`: the admin password stays in namespace
   `forgejo`.
8. **`choose-values.sh`** switches from the seed when the Secret `argocd/argocd-repo-forgejo` exists and its token
   reads `/api/v1/repos/ops/homelab/branches/master` from inside the Forgejo pod (`kubectl exec`, the token on stdin,
   curl to `localhost:3000`). Forgejo down, no token yet, nothing pushed yet or a wrong credential keep the seed.
   Once the root ApplicationSet reads Forgejo it never goes back (as today). A kubectl failure other than "not found"
   fails the script, as today.
9. **`make infra env=production`** runs, stopping at the first failure:
   1. `lab infra forgejo verify` (skipped with `force=1`, which replaces today's `production-checkout`);
   2. `terragrunt run --all apply` in `infra/production`;
   3. `lab infra forgejo sync`;
   4. `lab infra forgejo push`;
   5. `terragrunt apply` in `infra/production/argocd` (the switch to Forgejo on the first install; afterwards a change
      to `system/argocd`, or nothing).

   The first install and every later deploy are the same command. `lab infra forgejo push` alone deploys without
   touching the infrastructure. The sandbox's `make dev` does not change.
10. **Out of this step, moved in the roadmap:** the staging guard goes to step 12 with `make staging` (nothing can mark
    a commit before then, and production does not exist before then either); the Woodpecker OAuth client goes to
    step 10e with Woodpecker.

## Failure handling

- A guard fails: nothing is pushed or applied; the message names the guard and, for a check, its state and link.
- `sync` fails halfway: running it again finishes the job (every step checks before it changes).
- A token in OpenBao that no longer authenticates (deleted in Forgejo, Forgejo's data lost): `sync` replaces it, and
  ESO refreshes `argocd-repo-forgejo` within its refresh interval (or force-sync it).
- `forgejo_admin` and Kanidm admins are Forgejo site admins. If a site admin can still push to the protected `master`
  (the sandbox test records which), the docs say so: like a GitHub admin, it is a deliberate bypass, not a guard.
- Forgejo down during a deploy: `sync` waits up to 30 minutes, then fails; production keeps syncing what it has.

## Tests

- `cli/`, unit tests (in `make -C cli test`, part of the basic checks):
  - guards against a fake GitHub API: both checks succeeded passes; a running check, a failed check, no pull request,
    an API error, HEAD not `origin/master` and an uncommitted change each fail;
  - `sync` against a fake Forgejo: a first run creates everything, a second run writes nothing, a valid token is
    kept, an invalid one is replaced, the branch protection matches decision 4;
  - `push`: the token is in neither git's arguments nor its output.
- `test/` against the sandbox (replacing `TestForgejoSeed` and `TestForgejoSeedIsIdempotent`):
  - `sync --env sandbox` twice: the second run changes nothing and keeps the tokens in OpenBao;
  - `push --env sandbox`: Forgejo's `master` is HEAD;
  - the protection: `argocd`'s token cannot push; `deploy` can neither force-push nor delete `master`;
    `forgejo_admin`'s push is recorded either way;
  - `argocd-repo-forgejo` reads `master` with the read-only token.
- Offline: `TestArgoCDChooseValues` gains Forgejo running without `master` (seed), `master` readable (switch) and a
  wrong credential (seed); a Makefile test shows `make infra env=production` runs `verify` before applying and skips it
  with `force=1`. `test/scopes.yaml` gets the new tests.

## Documentation

- `docs/installation/production/deployment.md`: the first install and later deploys (one command), rollback by
  revert, what `force=1` skips.
- Forgejo's docs: `sync` owns org `ops` (users `deploy` and `argocd`, the protection, the tokens); nothing declares
  other orgs or repositories.
- CLAUDE.md: 10c done; the staging guard in step 12; the Woodpecker OAuth client in 10e; the production checklist
  without the seed Job and with Argo CD's read-only token.

## Out of scope

- Running any of it against production (step 12), the staging guard (step 12), Woodpecker and its OAuth client (10e).
- Declaring Forgejo orgs, teams or repositories other than `ops` (for example `groupTeamMap`).
- Pushing repositories other than `ops/homelab`.
