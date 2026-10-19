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
locally. A newer push cancels the older run of a pull request, which then shows as cancelled.

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

`gh` fills `{owner}/{repo}` from the repository you are in.

## Adding a check

Add a case to `scripts/ci-check` and its name to the `checks` list there and to the workflow's matrix.
