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

Four workflows build a fresh sandbox on a GitHub-hosted runner and run the suite with `make dev revision=<sha>`
(their steps are the composite action `.github/actions/sandbox`): Argo CD syncs that commit (the bootstrap rewrites
the homelab repository's revisions, `infra/_modules/argocd/set-revision.sh`), so nothing is pushed to branch `sandbox`
and a local sandbox is not disturbed. A run takes about 30 minutes; it needs no secret (the state passphrase is
random).

| Workflow | Trigger | Runs |
|---|---|---|
| `sandbox` | A push to `dev` (a merge) | The whole suite, unless every change since the last push whose run passed is skipped (`scripts/sandbox-last-passed`, `scripts/sandbox-scope`, `test/scopes.yaml`); a newer push cancels the run |
| `sandbox` | `workflow_dispatch` (Actions tab, or `gh workflow run sandbox.yaml --ref <branch>`) | `scope` `full` or `auto`, or exactly `tests`; `revision` picks another commit |
| `sandbox-release` | A pull request into `master` (a release): opened, reopened or a new commit | The whole suite; its check `sandbox` is required by the ruleset `protect-master-sandbox` |
| `sandbox-retarget` | A pull request retargeted to `master` (its base changed) | The whole suite, reported as the check `sandbox` through the checks API |
| `sandbox-label` | The label `sandbox` on a pull request into anything but `master` | What `make dev` would run for the pull request (its base to its head) |

Labels never trigger `sandbox-release`: a label event would replace the running check with a skipped one, which
GitHub counts as passing. For the same reason no other job is named `sandbox` (`sandbox`'s is `sandbox-dev`): a run
that tests nothing would pass the required check on the same commit. A pull request retargeted to `master` from a fork
gets a read-only token, which cannot create the check: push to it (or close and reopen it).
Labelling needs Triage or more on the repository. A new commit on a labelled pull request does not rerun
`sandbox-label`: remove the label and add it again (read the new code first: it runs on the runner); other labels never
cancel it. Pull requests from forks get a read-only token, which is all the run needs.

Label your own pull requests only when they change what the local sandbox cannot prove (the first install, CI):
`make dev` already ran their scope. Renovate labels its pull requests itself (`renovate.json5`; its App has Issues and
Pull requests write, and a label added by an App triggers workflows), because nobody runs `make dev` for them.

A failed run uploads the artifact `sandbox-state`: the `make dev` output, the pods, the events, the Applications and
the logs and descriptions of the pods that are not ready, and why each unhealthy Application is unhealthy
(`applications-unhealthy.yaml`).

```sh
gh run download <run-id> --name sandbox-state
```

If `dev` breaks after a merge, fix it with a new pull request right away. A `dev` → `master` pull request runs the whole
suite on its head and cannot merge until it passes.
