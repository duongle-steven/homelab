# Sandbox tests the current commit before the merge

Status: design approved in chat 2026-10-15, written spec awaiting review. Branch: `feat/sandbox-branch` (PR into `dev`).

## Goal

`make dev` runs the sandbox on the commit you are on, so a feature branch passes the full sandbox tests before its
pull request is merged. Staging and production keep their fixed branches.

## Context

- The sandbox's Argo CD syncs `github.com/duongle-steven/homelab` branch `dev` (`system/argocd/values-dev.yaml`,
  the `root` and `apps` ApplicationSets share `revision: &revision dev`). A change reaches the sandbox only after
  its PR is merged, so breakage is found on `dev` and fixed forward (steps 6b and 6c each needed a follow-up PR).
- Argo CD manages itself: the `argocd` Application renders `system/argocd` with `values-dev.yaml` from git, so a
  revision passed only at bootstrap would be reverted to what git says.
- The ApplicationSets come from the `argocd-apps` chart, which takes literal values (no templating) and whose
  revisions sit inside lists, so a Helm `--set` cannot override them without replacing the lists.

## Decision

A fixed branch `sandbox` on GitHub, which `make dev` force-pushes to the current commit:

- `system/argocd/values-dev.yaml`: `revision: &revision sandbox`.
- `make dev` refuses to run with uncommitted changes (only commits reach GitHub), then runs
  `git push --force origin HEAD:sandbox`, then builds or updates the sandbox. On an existing sandbox it asks Argo CD
  to refresh every Application (annotation `argocd.argoproj.io/refresh=normal`) so the new commit is picked up
  without waiting for the polling interval.
- The branch `sandbox` belongs to the sandbox only: it is overwritten on every `make dev`; nothing merges from it.

Unchanged: production syncs Forgejo's `master` (seed: GitHub `master`), staging likewise; the bootstrap still
renders `values-dev.yaml`.

## Workflow

1. Create a feature branch from `dev`, commit.
2. `make dev`: the full sandbox tests run on that commit.
3. Push the branch and open a PR into `dev`.
4. After the merge, `make dev` on `dev` once more (several PRs may land together).

## Error handling

- Uncommitted changes: `make dev` stops with a message listing them.
- Push rejected or offline: `make dev` stops before touching the cluster.
- A sandbox running an older commit: the refresh makes Argo CD sync the new one; the tests wait for sync as today.

## Testing

- Offline: `scripts/render-chart system/argocd dev` renders `revision: sandbox` for the git generators and
  sources of both ApplicationSets; `mkdocs build --strict`.
- On this branch: `make dev` pushes this commit to `sandbox` and the sandbox passes every test, proving the
  mechanism with the change itself.

## Docs

- `CLAUDE.md`: the sandbox syncs branch `sandbox`, which `make dev` force-pushes to the current commit; the basic
  checks are followed by `make dev` on the feature branch before the PR, and once more on `dev` after the merge.
- `docs/installation/sandbox.md`: the same workflow.
