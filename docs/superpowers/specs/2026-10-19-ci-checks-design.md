# Step 10a: GitHub Actions basic checks and protected branches

Status: approved 2026-10-19. Branch: `feat/ci-checks` from `dev` (the trimmed dev shell of PR #41 is merged).

## Goal

Every pull request into `dev` or `master` runs the basic checks on GitHub Actions, in the same Nix dev shell as the
workstation, and reports one status, `checks`. A repository ruleset makes `dev` and `master` accept changes only through
pull requests whose `checks` passed. The user also learns GitHub Actions on the real thing (workflows, matrix jobs,
caching, concurrency, pinned actions, least-privilege tokens, rulesets).

## Context

- Until production exists (step 12), work stays on GitHub: branches, pull requests, `gh`. Forgejo, Woodpecker and
  Renovate come in steps 10c–10e; 10b runs the sandbox on GitHub Actions.
- The repository is public: GitHub-hosted runners are free and have 4 vCPUs and 16 GB of memory.
- The basic checks (CLAUDE.md): `scripts/render-chart <dir>` and `<dir> dev` for every changed chart or app file,
  `go vet` and the offline tests in `test/` (the `TestState*` tests run `terragrunt` and `tofu`), `mkdocs build
  --strict`, `scripts/check-mesh-policies`, `scripts/check-env-config`, `scripts/check-secret-stores`,
  `make -C infra check`, `make -C cli test check`. Locally `check-env-config` takes about ten minutes.
- The dev shell lost pre-commit in the cleanup before this step; its useful hooks were yamllint, shellcheck and
  `tofu fmt` (`make -C infra check` already runs `tofu fmt -check`). Today `yamllint -s .` reports two warnings
  (`.woodpecker/`) and shellcheck three notes (`infra/_modules/argocd/bootstrap.sh`,
  `system/istio-system/files/cni-watchdog.sh`).
- Latest releases on 2026-10-19: `actions/checkout` v7.0.1, `cachix/install-nix-action` v31.11.2,
  `nix-community/cache-nix-action` v7.

## Decisions

| Topic | Decision |
|---|---|
| Workflow | `.github/workflows/checks.yaml`, on `pull_request` (into `dev`, `master`) and `push` (to `dev`, `master`) |
| Token | `permissions: contents: read` at the workflow level; no job asks for more |
| Concurrency | Group by workflow and ref, `cancel-in-progress` for pull requests only (a push to `dev` always finishes) |
| Actions | Pinned by full commit SHA with the version in a comment (`uses: actions/checkout@<sha> # v7.0.1`); Renovate bumps them in 10d |
| Tools | `cachix/install-nix-action` installs Nix; `nix-community/cache-nix-action` keeps the store in the Actions cache (key: `flake.lock` and `flake.nix`); every check runs `nix develop --command …` in the default dev shell. No versions are pinned twice. If a cold run takes more than five minutes to enter the shell, a `devShells.ci` built from the same package list is the follow-up (measured, not assumed) |
| Jobs | A matrix, one job per check: `render`, `go`, `cli`, `infra`, `docs`, `mesh`, `env-config`, `secret-stores`, `lint`; `fail-fast: false` so every failure shows |
| One status | Job `checks` `needs` the matrix, runs `if: always()` and fails unless every matrix job succeeded. It is the only required check, so adding or removing jobs never touches the ruleset |
| Changed charts | New `lab test charts --base <ref>` in `cli/` (reuses `scope.Changed`): prints each chart directory (nearest `Chart.yaml` under `system/`, `platform/`, `apps/`) and `apps/<name>.yaml` the diff touches, existing ones only; a change to `scripts/render-chart`, `scripts/env-values`, `infra/*/cluster/config.json` or `system/argocd/values*.yaml` prints all of them. `render` renders each, with and without `dev`. Locally it tells which charts the basic checks must render |
| Base ref | Pull requests: `github.event.pull_request.base.sha`; pushes: `github.event.before` (all charts when it is the zero SHA, a new branch); `actions/checkout` with `fetch-depth: 0` |
| Offline tests | `go` runs `go vet ./...` and the CLAUDE.md offline list in `test/`; the state tests make their own passphrases, nothing secret is needed |
| Lint | New `scripts/lint`: `yamllint -s .`, `shellcheck` on every tracked file whose first line is a `sh`/`bash` shebang, `actionlint` (added to the dev shell). The existing findings are fixed in this step. It joins the basic checks |
| Ruleset | `.github/rulesets/protect-dev-master.json`: branches `dev` and `master`; pull request required (0 approvals); required status check `checks` (integration GitHub Actions); no force-push, no deletion; bypass: repository admins (for a GitHub Actions outage). Branch `sandbox` stays unprotected (`make dev` force-pushes it) |
| Applying it | Only after the workflow has passed once on this step's pull request (GitHub offers a check as required only once it has seen it). The `gh api` commands to create, update, show and delete it are in `docs/how-to-guides/ci.md`; running them changes GitHub settings, so the user approves each run |
| Test scopes | `.github/**` and `scripts/lint` join `test/scopes.yaml`'s `skip` (no sandbox effect) |
| Local checks | CLAUDE.md: the basic checks still run before each commit (CI is the second line, not a replacement); `scripts/lint` is added to them; `lab test charts --base origin/dev` names the charts to render |

## Failure handling

- A failing check fails its matrix job and then `checks`; the pull request shows which job failed. `fail-fast: false`
  lets every job finish.
- A cancelled job (a newer push) leaves `checks` failed for that run only; the newer run reports.
- Cache miss: the run is slower (the shell downloads from cache.nixos.org), never wrong.
- A GitHub Actions outage: admins bypass the ruleset; the docs say to run the basic checks locally first.

## Tests

- `cli/internal/scope` (or a new package) unit tests for `lab test charts`: a chart file, a `values-dev.yaml`, an app
  file, a file outside any chart, a deleted chart, a global trigger (`scripts/render-chart`), the zero base.
- `scripts/lint` passes on the repository; `actionlint` passes on the workflow.
- The real proof is this step's pull request: the workflow runs, every job passes, `checks` reports. Timing is recorded
  (cold and warm cache) in the PR.
- After the ruleset is applied: a throwaway draft pull request with a deliberately failing check shows
  `mergeStateStatus: BLOCKED` (`gh pr view --json mergeStateStatus`), then is closed and its branch deleted.

## Documentation

- `docs/how-to-guides/ci.md` (in the mkdocs nav): what runs and when, how to read a failure, the ruleset and its `gh api`
  commands, the bypass, how to add a check (a matrix entry plus its local command).
- CLAUDE.md: the Rules bullet for branches and checks mentions CI and the ruleset; `scripts/lint` and `lab test charts`
  join the basic checks.

## Out of scope

- The sandbox on GitHub Actions (10b).
- Woodpecker, Renovate, Forgejo as the source of truth (10c–10e). `.woodpecker/` stays until 10c.
