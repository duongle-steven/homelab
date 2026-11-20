# App delivery

Our own apps (images the in-cluster CI builds from repositories in Forgejo's org `ci`: the blog, the teaching app
later, anything Backstage's template creates) reach production by a road of their own. The rest of the homelab keeps
the platform road. Design: `docs/superpowers/specs/2026-11-18-app-delivery-design.md`.

## The two roads

| | The platform road | The app road |
| --- | --- | --- |
| What | charts, components, third-party apps and their upgrades, an app's configuration | a new version of one of our apps (only its image tag) |
| Who changes it | a pull request into `dev`, Renovate's included | the app's CI, then Kargo |
| Tested by | the basic checks and the sandbox | the app's CI, then the canary's analysis in production |
| Deployed by | `just infra deploy production` (Forgejo's `ops/homelab`) | Kargo writing Forgejo's `ops/releases` |

Renovate leaves our images alone (`renovate.json5`: names starting `registry.{{`).

## How a version travels

1. A push to the app's default branch: the CI builds, pushes `registry.<domain>/<app>:<commit>` to zot, and signs it
   ([in-cluster CI](in-cluster-ci.md)). Only the CI (zot user `ci`) and `homelab-admins` push to zot.
2. Kargo's `Warehouse` for the app sees the newest build and makes Freight of it.
3. Stage `verify` runs `ClusterAnalysisTemplate` `ci-signature` (`platform/ci`): a Job in the app's namespace checks
   the image's signature against `ci-signing`'s public key. An unsigned image stops here.
4. Stage `production` (automatic for the blog; an app may leave it to a person) writes the whole file
   `ops/releases/<app>.yaml` (the image's repository and `tag@digest`), pushes it as Forgejo user `kargo`, and asks Argo
   CD to sync the app's Application.
5. The app's `Rollout` (Argo Rollouts, `system/argo-rollouts`) sends 20%, then 50% of the traffic to the new version
   through its `HTTPRoute` (the Gateway API plugin), while `gateway-success-rate` reads its share of 5xx and its p95
   latency at the Gateway, from 45 s after the canary takes traffic, four times 20 s apart. A failed analysis aborts it:
   every request is back on the stable version, `RolloutAborted` fires (ntfy, Telegram), and `ops/releases` still
   names the failed version. A canary that never becomes healthy is aborted at its progress deadline
   (`progressDeadlineAbort`) and alerts the same way. With no traffic there is nothing to judge, so the analysis
   passes: a version of a quiet app (the blog at night) goes out on its health checks alone.

## When a version is aborted

Fix it and push: the next good build goes through the same road and replaces it. To go back without a new build,
promote the previous Freight to `production` in Kargo (`https://kargo.<domain>`, `homelab-admins` only: editors see
Kargo but change nothing); the Rollout returns to Healthy on it. Promoting an older Freight by hand puts `production`
on hold (Kargo's auto-promotion hold, shown on the Stage): later versions still pass `verify`, but production stays on
the one you chose until someone promotes a newer Freight by hand, which releases the hold. So after a rollback, the
fix goes out when you promote it.

`KargoPromotionFailed` means a Promotion ended Failed or Errored (Forgejo or Argo CD unreachable, a step failing): open
it in Kargo's UI for the failed step. Nothing changed in production.

## Adding an app to the road

In the app's file (`apps/<app>.yaml`, the platform road):

- the image as `registry.{{ .Values.global.baseDomain }}/<app>` with a base tag the app runs before its first
  promotion, and `defaultPodOptions.imagePullSecrets: [{name: zot-pull}]`;
- two Services (stable and canary), its `HTTPRoute` naming both, a `Rollout` with `workloadRef` to the Deployment
  (which runs no pods), `gateway-success-rate`'s analysis and `progressDeadlineAbort: true`, a ServiceAccount
  `ci-signature`;
- its Kargo `ProjectConfig` (which stages promote themselves), `Warehouse` (`allowTags` on commit hashes) and Stages
  `verify` and `production`, as `test/fixtures/apps/delivery-test.yaml` has them.

Then:

- its Kargo `Project` in `platform/kargo/values.yaml`'s `projects` (a Project has no spec, which an app file cannot
  render);
- the app in the list of `system/argocd/values.yaml`'s `apps` ApplicationSet (`releases` source and `templatePatch`,
  both `(list "blog" "delivery-test")`): it reads `ops/releases`, syncs under AppProject `delivered`, Kargo may sync it,
  its namespace is a Kargo project's, and Argo CD leaves the canary's weights and selectors alone; and in
  `system/argocd-config/values.yaml`'s `deliveredApps`;
- its namespace in `system/secret-store/values.yaml`'s `zotPull`, a destination `<app>/generated/zot-puller` of
  `registry.puller` (`platform/generated-secrets`), its paths in the rotation catalog, OpenBao's policy `rotator` and
  `system/secret-store`'s `rotationReaders`;
- its namespace in `platform/ci/values.yaml`'s `publicKeyReaders`.

## In the sandbox

Kargo and Argo Rollouts run there; the suite's setup runs `lab infra forgejo sync`, so `ops/releases` exists before
anything reads it. The spec `AppDelivery` (`just dev --tests delivery`) drives the whole road with `delivery-test`,
which runs only there: Backstage's template creates `ci/delivery-test`, and the spec checks a promotion, an unsigned
image stopped at `verify`, an aborted canary, a rollback by Freight, and the hold that follows it until the fix is
promoted by hand.
