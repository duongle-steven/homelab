# App delivery: Kargo promotes our apps, Argo Rollouts releases them

Status: designed in chat 2026-11-17/18. Decisions marked *(user)* were made in chat; *(Claude's call)* marks the
builder's own, open to review. Builds on 10f (the in-cluster CI and its pipeline), Backstage-1 (the template that
creates repositories in Forgejo's org `ci`), 10c (production deploys by an explicit push) and the threat model
(`docs/superpowers/specs/2026-11-14-threat-model-design.md`). The blog, the next step, is the first app to use it.

## Intent

*(user)* Changes to the homelab come in two kinds, which should not share one road:

- **The platform** (charts, components, infrastructure, third-party apps and their upgrades): reviewed, tested in the
  sandbox, released `dev` → `master`, deployed by `just infra deploy production`. It stays as it is.
- **Our apps' versions** (the images our own CI builds, signs and attests, from repositories in org `ci`: the blog, the
  teaching app later, anything the Backstage template creates): a new version is only a new image tag, so it takes a
  fast road of its own, from a new image to production without a homelab pull request.

Today the second kind has no road at all: the in-cluster CI (10f) pushes `registry.<domain>/<repo>:<commit>` to zot
and stops there, and `apps/blog.yaml` still names upstream's `:latest`, which never reaches git's history and which
the pipeline never pushes. The CI's "golden path" is only half a path; this step is the other half.

*(user)* Kargo (GitOps promotion, the Argo ecosystem) promotes; Argo Rollouts (canary, traffic split by the Gateway
API) releases. Both are learned here and both serve the teaching app later.

## Goals

1. Argo Rollouts runs as a shared component (`system/argo-rollouts`) with the Gateway API traffic-router plugin, so an
   app's `Rollout` shifts weight between its stable and canary Services on its own `HTTPRoute`.
2. An app's canary is judged by an `AnalysisTemplate` reading Prometheus (the Istio Gateways' request metrics: the
   canary's share of 5xx and its latency); a failed analysis aborts the rollout and returns all traffic to stable.
3. Kargo (`platform/kargo`) watches our apps' images in zot and promotes a new one to production by writing its tag
   into Forgejo's repository `ops/releases`, which only Kargo writes *(user: approach A)*.
4. Per app *(user)*: the blog promotes itself; the teaching app (and any app that asks) waits for a person to promote
   it in Kargo's UI.
5. Production's Applications of our apps read their configuration from `ops/homelab` (`apps/<name>.yaml`, the platform
   road) and their image tag from `ops/releases` (the app road).
6. Renovate leaves images in our zot alone; it keeps third-party images and charts.
7. A blocked release says so: an aborted rollout and a failed promotion alert through Alertmanager (ntfy, Telegram).
8. Everything runs in the sandbox too, where a spec drives the whole road with a repository the Backstage template
   creates: CI builds it, Kargo promotes it, Rollouts releases it, and a broken version is rolled back.

## Non-goals

- **Several stages** (staging before production): the homelab has no environment that is always up (the sandbox and
  the testbed live on the workstation and are often off). One stage, production; the canary and its analysis are the
  check before everyone sees a version.
- **Our platform-level images** (lab's, Backstage's): they are built from this repository and released with it; they
  stay on the platform road.
- **Semantic versions** for the blog *(user)*: it ships every commit (its tag is the commit, chosen by build time).
  Release tooling for semantic versions (release-please on GitHub, or a Forgejo equivalent) comes with the teaching
  app.
- **Progressive delivery for third-party apps** (homepage, Jellyfin): possible later, not here.
- **Argo CD Image Updater**: Kargo covers its job and adds approval and verification.

## Anti-goals

- Two writers of one file: Kargo never writes to `ops/homelab`, and the platform road never writes to `ops/releases`.
- A promotion that skips the CI's signature: only the pipeline (zot user `ci`) and `homelab-admins` push to our apps'
  repositories in zot *(user, 2026-11-18: editors lose write there)*, and the promotion runs `cosign verify` against
  the pipeline's key before it writes the tag. Kyverno's `verify-ci-images` stays in Audit until 15b-2, so it is a
  report, not this gate.
- A rollout that keeps sending users to a failing canary: the analysis aborts on its own, no person needed.
- Kargo's UI or API reachable from the internet, or by anyone outside `homelab-admins` (and `homelab-editors` read-only).
- A sandbox that pays for this when it is not being tested: both components are small (to be measured), and the
  spec that drives the whole road runs only when its paths change (`test/scopes.yaml`).

## Constraints

- **The sandbox is often off** *(user)*: nothing depends on it being up; it exists to test.
- **Production's Argo CD reads Forgejo** (`ops/homelab`, pushed by `deploy` only). `ops/releases` is a second
  repository in org `ops`, created by `lab infra forgejo sync` like `ops/homelab`, writable only by a Forgejo user
  `kargo`, readable by `argocd`.
- **The sandbox's Argo CD reads GitHub** (branch `sandbox`) for configuration; for our apps' tags it reads the
  sandbox's own Forgejo `ops/releases`, as production does.
- **Rules of this repository** (`CLAUDE.md`): latest stable, pinned; every secret in the rotation catalog; every PVC
  labelled; every workload's securityContext; the mesh's `allow`; egress by table; `test/scopes.yaml`; the threat model
  updated in the same PR (Kargo is a new entry point and a new git writer).

## Approaches

Where an app's promoted version lives:

- **A. A repository of its own, `ops/releases`** *(user, chosen)*: one small values file per app and stage
  (`<app>/production.yaml`: the image tag), written only by Kargo. Clear ownership, permissions by repository, its
  history is the log of app deployments, Forgejo's daily backup covers it. The cost: one more repository, and the
  `apps` ApplicationSet learns which apps read a second source.
- B. A branch of `ops/homelab` (`releases`). Fewer repositories, but per-branch permissions and two writers in one
  repository's history.
- C. Each app's own repository (`ci/<app>/deploy/`). Self-contained apps, but deployment state mixed with source, every
  promotion triggering the app's CI, and Kargo needing write access to every app repository.

How a canary splits traffic:

- **Argo Rollouts' Gateway API plugin** *(user, chosen)*: weights on the app's own `HTTPRoute`, matching the homelab's
  Gateway API everywhere (no `VirtualService`, ambient mesh).
- Istio `VirtualService` traffic routing: a second routing model beside the HTTPRoutes. Rejected.
- A replica-count canary without traffic routing: simplest, but no weighted split to learn and not enough for the
  teaching app. Rejected.

How promotion is gated: per app *(user)*: automatic for the blog, manual for the teaching app.

## Design

### Argo Rollouts (`system/argo-rollouts`)

- The upstream chart at its latest version, with the Gateway API traffic-router plugin (its release pinned, loaded the
  way the chart documents), the controller only (the dashboard is optional: Kargo's UI and Argo CD show rollouts).
- An app opts in by rendering a `Rollout` instead of a `Deployment` (app-template can emit the Rollout with
  `workloadRef` to its Deployment scaled to zero, or the app renders the Rollout itself: left to the builder, with the
  blog step as its first user), two Services (stable, canary) and its `HTTPRoute` naming both.
- A shared `ClusterAnalysisTemplate` (`gateway-success-rate`) reading Prometheus: the canary Service's share of 5xx
  through the Gateway over the step's window, and its p95 latency, both against thresholds the app can override.
- Alerts: a rollout `Degraded` or aborted (`RolloutAborted`, from the controller's metrics) through Alertmanager.

### Kargo (`platform/kargo`)

- The upstream chart at its latest version (1.12 or later at build), LAN only at `kargo.<domain>`, signing in with
  Kanidm OIDC (`homelab-admins` admin, `homelab-editors` read-only; PKCE), in the mesh with its `allow`; its webhooks
  outside the mesh as every webhook pod.
- One Kargo `Project` per app, each with the resources below. *(Claude's call)* They live in git beside the app
  (`apps/<name>.yaml` and the app's Kargo resources, rendered by the same Application), so creating an app or changing
  how it is promoted takes the platform road; only its versions take the app road. The Project's namespace is the
  app's own (one namespace per app already carries its mesh `allow`, egress and RBAC):
  - a `Warehouse` on `registry.<domain>/<app>` (zot, read through the read-only pull account), image selection by newest
    build for commit-tagged apps and by semantic version for the others;
  - a `Stage` `production` whose promotion template verifies the image's signature (`cosign verify` with the
    pipeline's public key), writes the tag into `ops/releases/<app>/production.yaml`, commits
    as `kargo`, pushes, and asks Argo CD to sync the app's Application (`argocd-update`);
  - auto-promotion on for the blog, off for an app that needs approval;
  - optional verification after promotion: the same analysis or a smoke check (the app decides).
- Kargo's credentials: its Forgejo token (`ops/releases` write) from `lab infra forgejo sync` into OpenBao, zot's
  read-only account, Argo CD access through Kargo's own RBAC to Argo CD's API or its ServiceAccount (left to the
  builder; the least privilege that syncs only our apps' Applications).
- A failed promotion alerts (Kargo's metrics or events into Alertmanager).

### Pulling our images

No node has pulled from zot yet: its name does not resolve on the nodes (LAN DNS comes later), the sandbox's node does
not trust the local CA, and zot refuses anonymous pulls.

- **Credentials** *(user, 2026-11-18)*: an `imagePullSecret` in each delivered app's namespace, from a read-only zot
  account (htpasswd, generated in OpenBao, in the rotation catalog) through an `ExternalSecret`; no credential in the
  nodes' containerd config, no anonymous read. Kargo's Warehouse reads with the same account.
- **The name** *(Claude's call)*: the nodes resolve `registry.<domain>` to the LAN Gateway themselves (a hosts entry
  from `config.json`: the Gateway's VIP in production, the node's own address in the sandbox), so pulls do not wait for
  the LAN DNS step; Kargo reads zot by the same name as the pods, so its image references match.
- **Trust**: production's certificate is Let's Encrypt's; the sandbox's containerd trusts the local CA for that host
  (`registries.yaml` in `infra/_modules/k3d`).
- **Writes** *(user, 2026-11-18)*: zot's policy narrows `homelab-editors` from create/update on `**` to read on our
  apps' repositories (they keep write where they had it otherwise); `ci` and admins write.

### The versions repository (`ops/releases`)

- Created and kept by `lab infra forgejo sync` (private, org `ops`, branch `main` protected: only `kargo` pushes, no
  force-push or deletion; `argocd` reads). In the sandbox the same, on the sandbox's Forgejo.
- Layout: `<app>/production.yaml`, values merged by the app's Application after `apps/<app>.yaml` (for app-template:
  the controller's image tag; for a Rollout, its pod template's image).
- The `apps` ApplicationSet adds a second values source for apps that declare it (a marker in the app file, e.g. the
  `homelab:` description's `delivery: kargo`, read by the generator or a label), with `ignoreMissingValueFiles` so a new
  app syncs before its first promotion.
- *(Claude's call)* The repository exists before any Application reads it: in production `just infra deploy production`
  already runs `lab infra forgejo sync` before the push; in the sandbox the specs that need it run `sync` first, as
  `TestForgejoDeploy` does, and only an app that declares Kargo delivery reads it (the sandbox's test app, below). An
  Application whose second source is unreachable shows an error until then, which the seed phase accepts.

### Renovate

- A package rule disables `registry.<domain>/*` (our zot; the rule built from the environment's domain in Renovate's
  in-cluster config, never in git).

### The sandbox

- Both components run; the sandbox's Forgejo holds `ops/releases`; Kargo watches the sandbox's zot.
- A spec (`AppDelivery`, label `delivery`) drives the road end to end: the Backstage template creates
  `ci/delivery-test` (a tiny web image), the CI builds it, its Kargo Project (a sandbox-only app file in git,
  `values-dev` switched, never in production: *(Claude's call)*) auto-promotes, the Rollout
  canaries it to 100%; a second commit that serves 5xx is built, promoted, and aborted by the analysis with traffic
  back on the stable version; the alert fires.
- The blog in the sandbox (next step) runs the Rollouts demo images (`argoproj/rollouts-demo`) when no blog repository
  exists.

*(Claude's call)* The plan is written against Backstage-1 as merged (#104): its template creates the test app's
repository.

## Testing

- Offline: the charts render under policy, mesh, egress and backup checks; the `apps` ApplicationSet adds
  `ops/releases` only for apps that declare it; Renovate's rule; `lab infra forgejo sync`'s repository, protection and
  `kargo` user (cli tests beside `ci-bot`'s and `backstage`'s).
- Sandbox: `AppDelivery` above; Kargo's sign-in for an admin and an editor (read-only) and refusal of others.

## Documentation

`docs/how-to-guides/app-delivery.md` (the two roads, how an app joins the fast one, approving a promotion, rolling
back by promoting the previous freight), the in-cluster CI guide's link to it, the architecture overview, the threat
model (Kargo: a new git writer and UI; Rollouts), `CLAUDE.md` (the roadmap: this step before the blog; the platform
road vs the app road), the rotation catalog.

## Left to the builder

The charts' values, the plugin's installation, how an app-template app renders its Rollout and two Services, the
analysis queries and thresholds, Kargo's RBAC towards Argo CD, the marker that tells the ApplicationSet an app is
delivered by Kargo, the test app's image, and the order of the plan's tasks.
