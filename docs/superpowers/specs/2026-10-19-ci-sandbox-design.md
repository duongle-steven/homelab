# Step 10b: the sandbox on GitHub Actions

Status: design approved in chat 2026-10-19, written spec awaiting review. Branch: `feat/ci-sandbox` from `dev`.

## Goal

GitHub Actions builds a fresh sandbox and runs the suite on every push to `dev` (full unless every change is skipped),
on pull requests labelled `sandbox` (scoped like `make dev`), and on demand. Contributors' pull requests are then tested
in the sandbox after their merge, or before it when a maintainer labels them, without anyone running `make dev` by hand.

## Context

- The sandbox's Argo CD syncs branch `sandbox`, which `make dev` force-pushes; the revision is written in
  `system/argocd/values-dev.yaml` (YAML anchor `&revision sandbox`, used by both ApplicationSets).
- Argo CD manages itself: `system/argocd` is a directory of the `root` ApplicationSet, so it syncs its own chart from
  git and would undo any revision set from outside.
- Upstream `nixos` does not: OpenTofu installs Argo CD (`helm_release`, chart `argo-cd`) and creates the platform's
  Applications, and the `apps` ApplicationSet's revision is the variable `var.repository.revision`.
- `system/argocd` holds two templates rendered only once their CRDs exist: `kanidm-client.yaml` (kaniop) and
  `forgejo-repository.yaml` (ESO's `ExternalSecret` `argocd-repo-forgejo`, read by `test/forgejo_test.go`). Today they
  reach the cluster through Argo CD's own sync, because the bootstrap (`infra/_modules/argocd/bootstrap.sh`) runs
  before those CRDs exist.
- Production's first install syncs from the seed repository on GitHub (`values-seed.yaml`, chosen by `bootstrap.sh`
  while Forgejo is not running); Argo CD's own sync then switches it to `values.yaml` (`ops/homelab` on Forgejo).
  `infra/production/argocd` bootstraps once (`prevent_destroy`, `ignore_changes`).
- Step 10a: `checks.yaml` runs the basic checks in `nix develop` with `cache-nix-action`; the ruleset requires
  `checks`. The repository is public (GitHub-hosted runners: 4 vCPUs, 16 GB, free).
- The sandbox needs no secret from outside: the state passphrase is generated, OpenTofu and the cluster generate the
  rest, the repository is public.
- Measured locally: a fresh sandbox syncs in about 20 minutes, the main run takes about 13, `TestNodeRestart` 5 to 10.

## Decisions

| Topic | Decision |
|---|---|
| Argo CD | No longer manages itself (upstream `nixos`'s model): `system/argocd` leaves the `root` generator; only `infra/_modules/argocd` applies it |
| Core chart | `system/argocd` keeps Argo CD, `allow-hbone`, the HTTPRoute, the OIDC Secret and both ApplicationSets |
| CRD-gated templates | Move to a new component `system/argocd-config` (in the `root` generator, synced by Argo CD like every other): the `KanidmOAuth2Client` and the `ExternalSecret` `argocd-repo-forgejo`, names unchanged |
| Re-apply | The bootstrap re-applies whenever the chart's files, the chosen values or the revision change, in every environment, as upstream's `helm_release` updates in place. Production drops "once": a guard against an accidental `destroy` stays (the unit's resource has no destroy-time action; the plan checks how `prevent_destroy` can stay without `ignore_changes`) |
| Revision | Module variable `revision`; `bootstrap.sh` rewrites every `revision`/`targetRevision` of a source or generator whose `repoURL` is the homelab repository in the rendered manifests (`yq`), so other repositories (app-template, the seed placeholder's replacement) are untouched. The sandbox unit takes it from `HOMELAB_SANDBOX_REVISION` (default `sandbox`); production keeps the values file's |
| Seed switch | Production switches from the seed to Forgejo at the first `make infra env=production` after Forgejo runs (`bootstrap.sh` already detects it). Documented in the installation docs and CLAUDE.md's production checklist |
| `make dev` | New parameter `revision=<sha>`: no push to branch `sandbox`; passes `HOMELAB_SANDBOX_REVISION`; everything else (scope selection, nothing-to-test exit, the alone run) unchanged |
| Workflow | `.github/workflows/sandbox.yaml`: `push` to `dev` → `make dev scope=full revision=<sha>`, skipped when `lab test scope --base <before>` selects nothing; `pull_request` `labeled` with `sandbox` → `make dev base=<base sha> revision=<head sha>`; `workflow_dispatch` (inputs `scope`, `tests`) on any branch |
| Concurrency | Pushes: one group per commit (none cancelled). Labelled pull requests: one group per pull request, a newer run cancels the older. Dispatch: one group per run |
| Runner | `ubuntu-24.04`, `timeout-minutes: 90`; frees disk (removes preinstalled dotnet, android, ghc); raises `fs.inotify.max_user_instances` and `max_user_watches`; Nix and its cache as in `checks.yaml` (saved on pushes only); a random state passphrase; `permissions: contents: read` |
| Failure output | On failure, an artifact with the test output, `kubectl get pods -A`, the Applications' sync and health, and the logs of pods that are not Ready |
| Forks | The `pull_request` event gives forks a read-only token, which is all the job needs. Only users with write access can label. New commits do not rerun it: remove and re-add the label. Whether Argo CD fetches a fork's head SHA (reachable through `refs/pull/<n>/head`) is checked early in the plan; if not, the label path covers same-repository pull requests and the docs say so |
| Docker Hub | First runs pull anonymously and are measured; if rate limits hit, the CI sandbox gets a k3s `registries.yaml` mirroring `docker.io` through `mirror.gcr.io` (no credential), CI only |
| Status | The workflow's job is a check on the commit or pull request; it is not required by the ruleset (it runs after the merge, or on request) |
| Rules | CLAUDE.md: after a merge, CI runs the full suite on `dev` (no manual `make dev scope=full`; check that run); before a `dev` → `master` PR the latest sandbox run on `dev` must be green (it is a fresh sandbox, as `make dev-fresh`), rerun with `workflow_dispatch`; `make dev` stays the test before a pull request |
| Test scopes | `system/argocd-config/**` joins the groups that cover Argo CD (the Forgejo and SSO tests); `.github/**` is already skipped |

## Failure handling

- A failing test fails the job; the artifact holds the cluster's state.
- Nothing to test (every change skipped): the job succeeds in about a minute without building the sandbox.
- Docker Hub rate limit: pods stay in `ImagePullBackOff`, visible in the artifact; the mirror is the fix.
- A cancelled labelled run (newer label): its check shows cancelled; the newer run reports.
- A broken `dev` after a merge: the push run fails and GitHub notifies the author; fix it with a new pull request.

## Tests

- Offline (in the `go` check): render `system/argocd` with the revision `abc123` through the bootstrap's rewrite;
  every `revision`/`targetRevision` of the homelab repository becomes `abc123`, `app-template`'s chart version and the
  seed repository do not; without a revision the output equals today's.
- The sandbox suite after the restructure: `make dev` (the whole suite) proves Argo CD, its Kanidm client and
  `argocd-repo-forgejo` still come up with `system/argocd-config`.
- On GitHub: this step's pull request labelled `sandbox` passes the suite; after the merge the push run on `dev` is
  green; a fork's labelled pull request is tried (or the limit documented).

## Documentation

- `docs/how-to-guides/ci.md`: the sandbox workflow, its triggers, the label, the artifact, forks, dispatch.
- `docs/how-to-guides/sandbox-tests.md`: `make dev revision=`.
- Installation docs: Argo CD's changes need `make infra env=production`; the seed switch.
- CLAUDE.md: Argo CD no longer manages itself (Sandbox bullets, the bootstrap, the production checklist's seed
  sentence), the new rules above, roadmap 10b done.

## Out of scope

- Porting `system/` and `platform/` to OpenTofu-managed Applications as upstream does.
- Woodpecker, Renovate, Forgejo as the source of truth (10c–10e).
