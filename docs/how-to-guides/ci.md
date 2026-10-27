# Continuous integration

Every pull request into `dev` or `master`, and every push to them, runs the basic checks on GitHub Actions
(`.github/workflows/checks.yaml`), in the same Nix dev shell as the workstation.

| Job | Command |
|---|---|
| `render`, `go`, `cli`, `infra`, `docs`, `mesh`, `backup-groups`, `rotation-catalog`, `env-config`, `secret-stores`, `lint`, `leaks`, `iac`, `images` | `scripts/ci-check <job>` |
| `checks` | succeeds only when every job above did: the one status the ruleset requires |

Run them all locally before a commit (the basic checks), in the dev shell:

```sh
scripts/ci-check all
```

`render` renders the charts the branch changes, uncommitted changes included (`cd cli && go run ./cmd/lab test charts` lists them).

## Detection (step 15a)

Three jobs look for what a change brings that should not reach `dev`. Kubernetes manifests (privileged, root, RBAC)
are Kyverno's (step 15b), not these jobs'.

| Job | Fails on | Scans |
|---|---|---|
| `leaks` | a secret gitleaks recognises (`.gitleaks.toml`: its default rules), printed redacted | the commits from the base (the pull request's, `origin/dev` locally) to HEAD |
| `iac` | a HIGH or CRITICAL Trivy finding | every Dockerfile and OpenTofu (`infra/`), from the root |
| `images` | a CRITICAL vulnerability that has a fix | the images new in the renders of the charts and app files the change touches |

GitHub's secret scanning and push protection are on as well (the repository is public): a push holding a known
provider's token is refused before any job runs.

What to do about a finding:

- **A leaked secret:** it is public once pushed. Rotate it (`lab secrets rotate <name>`, or the provider's console),
  then remove it from the branch. Rewriting history does not take it back.
- **An image:** bump it to a release that fixes the CVE (Renovate usually has one). If none exists and the risk is
  understood, add the CVE to `.trivyignore.yaml` under `vulnerabilities` with the image's path, a `statement` and an
  `expired_at` at most 90 days away.
- **A Dockerfile or OpenTofu finding:** fix it, or add it to `.trivyignore.yaml` under `misconfigurations` with the
  file's path from the root and a `statement`.

Trivy keeps its databases in `~/.cache/trivy`. GitHub Actions restores them from a cache keyed by day.

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
| `sandbox` | `workflow_dispatch` only (Actions tab, or `gh workflow run sandbox.yaml --ref <branch>`) | `scope` `full` or `auto`, or exactly `tests`; `revision` picks another commit |
| `sandbox-release` | A pull request into `master` (a release): opened, reopened or a new commit | The whole suite; its check `sandbox` is required by the ruleset `protect-master-sandbox` |
| `sandbox-retarget` | A pull request retargeted to `master` (its base changed) | The whole suite, reported as the check `sandbox` through the checks API |
| `sandbox-label` | The label `sandbox` on a pull request into anything but `master` | What `make dev` would run for the pull request (its base to its head) |

Labels never trigger `sandbox-release`: a label event would replace the running check with a skipped one, which
GitHub counts as passing. For the same reason no other job is named `sandbox` (`sandbox`'s is `sandbox-dev`): a run
that tests nothing would pass the required check on the same commit. A pull request retargeted to `master` from a fork
gets a read-only token, which cannot create the check: push to it (or close and reopen it).
Labelling needs Triage or more on the repository. A new commit on a labelled pull request does not rerun
`sandbox-label` (except Renovate's own commits on its labelled pull requests): remove the label and add it again (read the new code first: it runs on the runner); other labels never
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

A push to `dev` (a merge) runs only the basic checks: `make dev` already ran the branch's scope locally, and the
whole suite runs on every `dev` → `master` pull request (and locally with `make dev-fresh`). Run `sandbox` by hand to
test `dev` in between. If `dev` breaks after a merge, fix it with a new pull request right away. A `dev` → `master`
pull request runs the whole suite on its head and cannot merge until it passes.

GitHub disables the workflows of a fork now and then to save compute: no run starts, and a manual run answers
`Actions has been disabled for this repository`, while Settings → Actions still shows them allowed. The Actions tab
then shows "Workflows on this fork have been disabled": re-enable them there.
