# Continuous integration

Every pull request into `dev` or `master`, and every push to them, runs the basic checks on GitHub Actions
(`.github/workflows/checks.yaml`), in the same Nix dev shell as the workstation.

| Job | Command |
|---|---|
| `render`, `go`, `cli`, `infra`, `docs`, `mesh`, `env-config`, `secret-stores`, `lint` | `scripts/ci-check <job>` |
| `checks` | succeeds only when every job above did: the one status the ruleset requires |

Run them all locally before a commit (the basic checks), in the dev shell:

```sh
scripts/ci-check all
```

`render` renders the charts the branch changes, uncommitted changes included (`cd cli && go run ./cmd/lab test charts` lists them).

## A failed run

Open the failed job in the pull request's checks: its last step is the `scripts/ci-check` command, which reproduces
locally. A newer push cancels the older run of a pull request: its jobs show as cancelled and its `checks` as failed.

## Protected branches

The ruleset `protect-dev-master` (`.github/rulesets/protect-dev-master.json`): changes reach `dev` and `master` only
through pull requests whose `checks` passed (no approval needed), never by a direct push, a force-push or a deletion.
Repository admins may merge a pull request whose checks failed (`gh pr merge --admin`), for a GitHub Actions outage
only: run `scripts/ci-check all` first. Branch `sandbox` is not protected (`make dev` force-pushes it).

```sh
# Create it (once)
gh api --method POST 'repos/{owner}/{repo}/rulesets' --input .github/rulesets/protect-dev-master.json
# Its ID
id=$(gh api 'repos/{owner}/{repo}/rulesets' --jq '.[] | select(.name == "protect-dev-master") | .id')
# Update it after editing the file
gh api --method PUT "repos/{owner}/{repo}/rulesets/$id" --input .github/rulesets/protect-dev-master.json
# Show it, and the rules a branch gets
gh api "repos/{owner}/{repo}/rulesets/$id"
gh api 'repos/{owner}/{repo}/rules/branches/dev'
# Remove it
gh api --method DELETE "repos/{owner}/{repo}/rulesets/$id"
```

The ruleset `protect-master-sandbox` (`.github/rulesets/protect-master-sandbox.json`) adds the check `sandbox` to
`master`; the same commands apply with its file and name.

`gh` fills `{owner}/{repo}` from the repository you are in.

## Adding a check

Add a case to `scripts/ci-check` and its name to the `checks` list there and to the workflow's matrix.

## The sandbox

`.github/workflows/sandbox.yaml` builds a fresh sandbox on a GitHub-hosted runner and runs the suite with
`make dev revision=<sha>`: Argo CD syncs that commit (the bootstrap rewrites the homelab repository's revisions,
`infra/_modules/argocd/set-revision.sh`), so nothing is pushed to branch `sandbox` and a local sandbox is not
disturbed. A run takes about 45 minutes; it needs no secret (the state passphrase is random).

| Trigger | Runs |
|---|---|
| A push to `dev` (a merge) | The whole suite, unless every change since the previous push is skipped (`test/scopes.yaml`) |
| A pull request into `master` (a release): opened, or a new commit | The whole suite, required by the ruleset `protect-master-sandbox` |
| The label `sandbox` on another pull request | What `make dev` would run for the pull request (its base to its head) |
| `workflow_dispatch` (Actions tab, or `gh workflow run sandbox.yaml --ref <branch>`) | `scope` `full` or `auto`, or exactly `tests`; `revision` picks another commit |

The check is required only on pull requests into `master`; elsewhere it runs after the merge, or when a maintainer labels a pull request. Only
people with write access can label. A new commit on the pull request does not rerun it: remove the label and add it
again (read the new code first: it runs on the runner). Pull requests from forks get a read-only token, which is all
the run needs.

A failed run uploads the artifact `sandbox-state`: the `make dev` output, the pods, the events, the Applications and
the logs of the pods that are not ready.

```sh
gh run download <run-id> --name sandbox-state
```

If `dev` breaks after a merge, fix it with a new pull request right away. A `dev` → `master` pull request runs the whole
suite on its head and cannot merge until it passes.
