# App Delivery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Our apps' new images reach production without a homelab pull request: Kargo promotes a signed image by writing its tag to Forgejo's `ops/releases`, Argo CD syncs it, and Argo Rollouts canaries it behind the app's HTTPRoute, aborting on a bad analysis.

**Architecture:** Two shared components, `system/argo-rollouts` (controller, Gateway API traffic-router plugin, a shared `ClusterAnalysisTemplate`) and `platform/kargo` (UI and API on the LAN behind Kanidm). The `apps` ApplicationSet gives each app on the fast road a second values file from `ops/releases`, which only Forgejo user `kargo` writes. Pods and nodes pull our images from zot with a read-only account. A sandbox-only test app built from the Backstage template drives the whole road in one spec.

**Tech Stack:** Kargo 1.12.6 (chart `oci://ghcr.io/akuity/kargo-charts/kargo` 1.12.6), Argo Rollouts v1.10.2 (argo-helm chart `argo-rollouts` 2.43.12), `rollouts-plugin-trafficrouter-gatewayapi` v0.18.0, app-template 5.3.0, ESO, kaniop, Go (`cli/`), Ginkgo (`test/e2e`). Check each for a newer stable release when the task starts (`CLAUDE.md` Rules).

**Spec:** `docs/superpowers/specs/2026-11-18-app-delivery-design.md`

## Global Constraints

- Latest stable release of every chart, image and binary, pinned exactly; images by digest when they have no version tag.
- Kargo is LAN only (`kargo.<domain>` on `istio-system/gateway`); sign-in for `homelab-admins` (admin) and `homelab-editors` (viewer) only.
- Kargo never writes to `ops/homelab`; nothing but Kargo writes to `ops/releases`.
- Only zot user `ci` and `homelab-admins` push to zot; `homelab-editors` and `homelab-users` read.
- Pods pull our images with an `imagePullSecret` from a read-only zot account (ESO from OpenBao); no credential in containerd's config, no anonymous read.
- Promotion is automatic for the blog and for the sandbox's test app, manual for any app that asks; an aborted rollout and a failed promotion alert through Alertmanager.
- Every secret is in `cli/internal/rotate/catalog.yaml`; every workload sets its securityContext or carries a `PolicyException` with its reason; every meshed namespace has its `allow`; egress through `system/egress/values.yaml`; every component has its `test/scopes.yaml` entry.
- The threat model, `CLAUDE.md`, `docs/superpowers/deferred-minors.md` and the how-to guide are updated in this branch.

## Review Focus

- **An unsigned image pushed to zot by an admin.** Admins keep write, so a hand-pushed `blog:<tag>` exists. Expected: Kargo never promotes it to `production`, because the `verify` stage's cosign analysis fails. Pinned in Task 6's spec, which pushes an unsigned tag with the admin account.
- **A delivered app's first sync, before any promotion.** `ops/releases/<app>/production.yaml` does not exist yet. Expected: the app syncs from its base values (`ignoreMissingValueFiles`) and is Healthy. Pinned in Task 5's offline test and Task 6's spec (fresh sandbox).
- **A canary that serves 5xx.** Expected: the analysis fails, the Rollout aborts, 100% of traffic is back on stable, and `RolloutAborted` fires. Git still names the bad tag. Pinned in Task 6's spec.
- **Forgejo restarting while Kargo promotes.** Expected: the promotion fails and is retried by Kargo, not half-written. A failed promotion alerts. Pinned in Task 4 by the alert's offline test; the restart itself is not driven (noted in `deferred-minors.md`).
- **A sandbox whose Forgejo has no `ops/releases` yet** (the suite runs before any spec synced Forgejo). Expected: the suite's setup runs `lab infra forgejo sync` before waiting for Argo CD to settle. Pinned in Task 3.

---

### Task 1: Argo Rollouts (`system/argo-rollouts`)

**Files:**
- Create: `system/argo-rollouts/Chart.yaml` (dependency `argo-rollouts` 2.43.12 from `https://argoproj.github.io/argo-helm`; `homelab/*` annotations, group Development, no host), `values.yaml`, `templates/analysis-template.yaml`, `templates/prometheus-rule.yaml`, `templates/plugin-rbac.yaml`, `templates/authorization-policy.yaml`
- Modify: `system/argocd/values-dev.yaml` (the sandbox's `root` list), `system/egress/values.yaml` (namespace `argo-rollouts`: 443, for the plugin's download), `test/scopes.yaml` (component `system/argo-rollouts/**` → label `rollouts`), `cli` catalog regeneration (`lab catalog generate`)
- Test: `test/offline/argo_rollouts_test.go`

**Interfaces:**
- Produces: `ClusterAnalysisTemplate` `gateway-success-rate` with args `service` (the canary Service's name), `namespace`, `max-error-ratio` (default `"0.05"`) and `max-p95-ms` (default `"1000"`). It reads Istio's Gateway request metrics from Prometheus (`istio_requests_total` and `istio_request_duration_milliseconds_bucket`, filtered by `destination_service_name` and `destination_service_namespace`, reporter of the Gateway), over 1 minute, every 30 s, `failureLimit: 1`. It treats no traffic as success (`len(result) == 0 || result[0] <= max`).
- Produces: the plugin registered as `argoproj-labs/gatewayAPI` (the name an app's Rollout uses under `trafficRouting.plugins`), loaded from the v0.18.0 release URL with its `sha256`.
- Produces: alert `RolloutAborted` (`rollout_info{phase="Degraded"} == 1` or the controller's aborted phase, `for: 1m`, severity `warning`, label `source: argo-rollouts`).

- [ ] **Step 1: Write the failing test** `TestArgoRolloutsRender` (render with `renderChart`/`scripts/render-chart` as the other offline tests do), asserting:
  - the controller's ConfigMap lists the plugin `argoproj-labs/gatewayAPI` with a `location` ending `v0.18.0/gatewayapi-plugin-linux-amd64` and a non-empty `sha256`;
  - `ClusterAnalysisTemplate/gateway-success-rate` exists, with args `service`, `namespace`, `max-error-ratio` and `max-p95-ms`;
  - the PrometheusRule has `RolloutAborted`;
  - a ClusterRole grants the controller `get,list,watch,update,patch` on `httproutes.gateway.networking.k8s.io`;
  - no Deployment lacks `seccompProfile: RuntimeDefault` and `allowPrivilegeEscalation: false`;
  - the dashboard is off.
- [ ] **Step 2: Run** `cd test && go test ./offline -run TestArgoRolloutsRender`. Expected: FAIL (chart missing).
- [ ] **Step 3: Implement** the chart and templates above. The controller sits in the mesh: its webhooks are none, the metrics port is allowed for Prometheus (`allow`). Get the plugin's sha256 from the release's checksum file.
- [ ] **Step 4: Run** the test and `scripts/ci-check render policy mesh env-config catalog`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(argo-rollouts): the controller with the Gateway API plugin and a shared analysis`

### Task 2: Pulling our images from zot

**Files:**
- Modify: `platform/zot/values.yaml` (`accessControl`: `homelab-editors` → `[read]`; a user `puller` → `[read]`), `platform/zot/templates/auth-secret.yaml` (htpasswd gains `puller` from `zot/generated/puller`), `platform/generated-secrets/values.yaml` (a generated password `zot-puller` pushed to `zot/generated/puller` and to each pulling namespace's `<ns>/generated/zot-puller`: `kargo` and the delivered apps (`delivery-test` in Task 6, `blog` later)), `cli/internal/rotate/catalog.yaml` (entry `zot.puller`, method generated, class 1, readers zot's StatefulSet and each namespace's pull Secret)
- Create: `system/secret-store/templates/zot-pull.yaml`, an `ExternalSecret` `zot-pull` (type `kubernetes.io/dockerconfigjson` for host `registry.<domain>`, user `puller`). It is rendered only in namespaces listed in `system/secret-store/values.yaml` `zotPull: []`; the delivered apps' namespaces are added by their own tasks.
- Modify: `system/istio-system` (production too: the CoreDNS rewrite in `coredns-custom` sends `registry.<domain>` to `gateway-istio.istio-system`, so pods resolve zot before LAN DNS. The sandbox's nip.io rewrite stays.), `infra/_modules/metal/flake/profiles/network.nix` (`networking.hosts.<Gateway VIP> = [ "registry.<domain>" ]` from `cluster.json`'s domain and service VIP)
- Modify: `infra/_modules/k3d/main.tf` (the sandbox node's path to zot, from Step 1's spike)
- Test: `test/offline/zot_test.go` (extend), `test/offline/secret_store_test.go` (extend), `test/offline/coredns_test.go` (or the existing istio-system test)

**Interfaces:**
- Produces: in every namespace listed in `zotPull`, a Secret `zot-pull` of type `kubernetes.io/dockerconfigjson` valid for `registry.<domain>`. Apps name it in `defaultPodOptions.imagePullSecrets`, and Kargo's Warehouse in its credentials (Task 4).

- [ ] **Step 1: Spike the sandbox node's path to zot**, not committed. The k3d node resolves `registry.127-0-0-1.nip.io` to its own loopback, and containerd does not know the local CA. Try in order and keep the first that pulls `registry.127-0-0-1.nip.io/<image>` with `crictl pull --creds puller:<pw>` from inside the node:
  - (a) a `registries.yaml` mirror for that host whose endpoint is the node's own address on 443, with `configs.<host>.tls.insecure_skip_verify: true`;
  - (b) the same with the Gateway Service's ClusterIP, written after Istio creates it;
  - (c) a k3d `hostAliases` entry to the node's address.

  Record the result and why in `infra/_modules/k3d/main.tf`'s comment. `insecure_skip_verify` is sandbox only (its CA is made after the cluster).
- [ ] **Step 2: Write the failing tests:**
  - `TestZotWriters`: the rendered config grants `create`/`update` only to `homelab-admins` and user `ci`, and `puller` only `read`.
  - `TestZotPullSecret`: `secret-store` rendered for a listed namespace yields `ExternalSecret/zot-pull` with target type `kubernetes.io/dockerconfigjson` and a key under `<ns>/generated/zot-puller`; for an unlisted one, none.
  - `TestRegistryResolvesInPods`: production's `coredns-custom` rewrites `registry.<domain>`.
  - `lab secrets check` passes with the new catalog entry.
- [ ] **Step 3: Run** `cd test && go test ./offline -run 'TestZot|TestRegistryResolvesInPods'`. Expected: FAIL.
- [ ] **Step 4: Implement** the files above. In `network.nix`, read the Gateway's VIP and domain from `cluster.json` the way the profile reads other cluster values.
- [ ] **Step 5: Run** the tests, `scripts/ci-check render secret-stores rotation-catalog env-config` and `just infra check`. Expected: PASS.
- [ ] **Step 6: Commit** `feat(zot): a read-only pull account, editors read-only, the registry's name for pods and nodes`

### Task 3: `ops/releases` and user `kargo` in Forgejo

**Files:**
- Modify: `cli/internal/forgejo/sync.go`:
  - constants `ReleasesRepo = "releases"`, `ReleasesBranch = "main"`, `Kargo = "kargo"`, `KargoPath = "kargo/forgejo-token"`;
  - `ensureRepository` for `ops/releases` (private, default branch `main`, initialised with a README so the branch exists);
  - collaborators `kargo` (write) and `argocd` (read) on it;
  - branch protection on `main` pushable only by `kargo`, no force-push or deletion;
  - `kargo`'s token with scope `write:repository`, checked against `/api/v1/repos/ops/releases`.
- Modify: `cli/internal/forgejo/sync_test.go`, `cli/internal/rotate/catalog.yaml` (entry `forgejo.kargo-token`, method like `forgejo.backstage-token`'s, readers Kargo's controller), `system/argocd-config/templates/forgejo-repository.yaml` (a second ExternalSecret `argocd-repo-releases`: same `argocd/forgejo` token, url `http://forgejo-http.forgejo:3000/ops/releases`, carrying `SkipDryRunOnMissingResource` like the first), `test/e2e/e2e_suite_test.go` (sandbox `BeforeSuite`: wait at most 15 minutes for Forgejo's `/api/healthz`, run `lab infra forgejo sync --env sandbox` once, then the existing wait for Argo CD to settle), `CLAUDE.md` (the sandbox's Forgejo is synced by the suite's setup, not left empty)
- Test: `cli/internal/forgejo/sync_test.go`, `test/offline/argocd_config_test.go` (extend)

**Interfaces:**
- Produces: repository `http://forgejo-http.forgejo:3000/ops/releases`, branch `main`, layout `<app>/production.yaml`. OpenBao `secret/kargo/forgejo-token` holds `{username: kargo, token}`. Argo CD has the repository Secret `argocd-repo-releases`.

- [ ] **Step 1: Write the failing tests** in `sync_test.go` (with the fake Forgejo the other tests use):
  - `TestSyncCreatesReleases`: repository `ops/releases` exists with branch `main`.
  - `TestSyncReleasesProtection`: only `kargo` may push to `main`, no force push, no deletion.
  - `TestSyncKargoToken`: a token with `write:repository` written to `kargo/forgejo-token`.
  - `TestSyncReleasesReaders`: `argocd` reads; `deploy` has no access.
  - `TestSyncIdempotent` covers a second run changing nothing.
  - In `argocd_config_test.go`: `argocd-repo-releases` is rendered with the annotation.
- [ ] **Step 2: Run** `just cli::test` and `cd test && go test ./offline -run TestArgoCDConfig`. Expected: FAIL.
- [ ] **Step 3: Implement** the changes above, reusing `ensureUser`, `ensureCollaborator`, `ensureToken` and a protection helper generalised from `ensureProtection(c, repo, branch, pusher)`.
- [ ] **Step 4: Run** the tests, then `just cli::test cli::check` and `scripts/ci-check rotation-catalog render`. Expected: PASS.
- [ ] **Step 5: Run** `just dev --tests 'forgejo || ci'` from the main checkout. Expected: PASS, and `ops/releases` exists in the sandbox's Forgejo after the suite's setup.
- [ ] **Step 6: Commit** `feat(forgejo): ops/releases written only by kargo; the sandbox suite syncs Forgejo first`

### Task 4: Kargo (`platform/kargo`)

**Files:**
- Create: `platform/kargo/Chart.yaml` (dependency `kargo` 1.12.6 from `oci://ghcr.io/akuity/kargo-charts`; annotations: name Kargo, description "Promotes our apps' versions", group Development, host `kargo`), `values.yaml`, `templates/httproute.yaml`, `templates/kanidm-client.yaml` (public client `kargo`, PKCE, `scopeMap` admins and editors, `claimMap` `homelab_groups`), `templates/authorization-policy.yaml`, `templates/external-secrets.yaml` (Kargo's admin bootstrap disabled; the Forgejo token from `kargo/forgejo-token` as a shared git credential Secret labelled `kargo.akuity.io/cred-type: git` for `http://forgejo-http.forgejo:3000/ops/releases`), `templates/prometheus-rule.yaml`
- Modify: `platform/kanidm/values.yaml` (`oauth2ClientNamespaces` += `kargo`), `system/argocd/values-dev.yaml` (sandbox list), `test/scopes.yaml` (`platform/kargo/**` → label `kargo`), `system/secret-store/values.yaml` (`zotPull` += `kargo`), `platform/generated-secrets/values.yaml` (destination `kargo/generated/zot-puller`)
- Test: `test/offline/kargo_test.go`, `test/e2e/sso_test.go` (a `SSOKargo` Describe like `SSOArgoWorkflows`)

**Interfaces:**
- Consumes: Task 2's `zot-pull` (here re-labelled `kargo.akuity.io/cred-type: image` for `registry.<domain>`: a second ExternalSecret from the same key, since Kargo reads its own Secret format); Task 3's `kargo/forgejo-token`.
- Produces:
  - Kargo's API and UI at `https://kargo.<domain>`; OIDC admins `homelab-admins`, viewers `homelab-editors` (chart values `api.oidc.admins.claims.groups` and `api.oidc.viewers.claims.groups`, claim `homelab_groups`).
  - The controller's Argo CD integration on namespace `argocd`: it updates only Applications annotated `kargo.akuity.io/authorized-stage: <project>:<stage>`.
  - Alert `KargoPromotionFailed`, from the controller's promotion metric if Kargo 1.12 exposes one; otherwise a Loki ruler alert on the controller's failed-promotion log line. The test pins whichever is chosen.

- [ ] **Step 1: Write the failing tests:**
  - `TestKargoRender`: the HTTPRoute host is `kargo.<domain>` on `gateway` only; OIDC issuer `https://auth.<domain>/oauth2/openid/kargo` with client `kargo` and admin and viewer claims as above; the webhook pods are labelled `istio.io/dataplane-mode: none`; the git credential Secret targets only `ops/releases`; no Secret, ConfigMap or chart value holds a domain or IP (`scripts/check-env-config` passes); `KargoPromotionFailed` exists.
  - `SSOKargo`: `dev-admin` reaches the projects list, `dev-editor` reads it but a create returns 403, and `dev-viewer` and an outsider are refused at Kanidm.
- [ ] **Step 2: Run** the offline test. Expected: FAIL.
- [ ] **Step 3: Implement** the chart. Add the `allow` for the Gateway on the API port, Prometheus on metrics and the controller's calls to Forgejo and Argo CD's API. The Kargo controller needs the `argocd` namespace allowed to it in `platform/forgejo`'s allow (port 3000). Add a `PolicyException` only where the chart cannot set the securityContext.
- [ ] **Step 4: Run** the offline test and `scripts/ci-check render policy mesh env-config secret-stores rotation-catalog catalog`. Expected: PASS.
- [ ] **Step 5: Run** `just dev --tests 'kargo'`, plus a measurement: Kargo's and Argo Rollouts' memory in the sandbox (`kubectl top pods -n kargo -n argo-rollouts`), recorded in the how-to guide (Task 7). Expected: PASS.
- [ ] **Step 6: Commit** `feat(kargo): Kargo on the LAN behind Kanidm, writing ops/releases`

### Task 5: The `apps` ApplicationSet's second source; Renovate leaves our zot alone

**Files:**
- Modify: `system/argocd/values.yaml` and `values-dev.yaml` (restated) in the `apps` ApplicationSet. Its `templatePatch` gains, for the apps in a list `delivered` (`blog`, `delivery-test`):
  - a third source `{repoURL: http://forgejo-http.forgejo:3000/ops/releases, targetRevision: main, ref: releases}`;
  - the app-template source's `valueFiles` += `$releases/<app>/production.yaml` with `ignoreMissingValueFiles: true`;
  - the Application annotation `kargo.akuity.io/authorized-stage: <app>:production`;
  - the namespace label `kargo.akuity.io/project: "true"` in `managedNamespaceMetadata`.

  The app file's own path is taken from the generator's `.path.path`, so the sandbox's fixture outside `apps/` works.
- Modify: `platform/renovate/values.yaml` (a `packageRules` entry `matchPackageNames: ["registry.<domain>/**"]`, `enabled: false`, built from `global.baseDomain` in the wrapper's template, never a literal domain)
- Test: `test/offline/argocd_apps_test.go` (or the existing ApplicationSet test), `test/offline/renovate_test.go`

**Interfaces:**
- Consumes: Task 3's `ops/releases` and `argocd-repo-releases`; Task 4's authorized-stage contract.
- Produces: for a delivered app `X`, values precedence `apps/X.yaml` then `ops/releases/X/production.yaml`. The release file holds only `controllers.main.containers.main.image.{repository,tag}` (app-template's path), which Kargo's `yaml-update` step writes.

- [ ] **Step 1: Write the failing tests:**
  - `TestDeliveredAppsReadReleases`: render `system/argocd` for production and the sandbox, and evaluate the template for `blog` and for `homepage` with the ApplicationSet's Go template. `blog` has the `releases` source, the extra valueFile, `ignoreMissingValueFiles: true` and the annotation; `homepage` has none of them.
  - `TestRenovateSkipsOurRegistry`: the rendered Renovate config disables `registry.<domain>/**`.
- [ ] **Step 2: Run** them. Expected: FAIL.
- [ ] **Step 3: Implement** the changes above; `scripts/check-env-config` must still see the restated generators equal.
- [ ] **Step 4: Run** the tests and `scripts/ci-check render env-config`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(argocd): delivered apps read their version from ops/releases; Renovate skips our registry`

### Task 6: The road end to end in the sandbox (`AppDelivery`)

**Files:**
- Create: `test/fixtures/apps/delivery-test.yaml`, an app-template values file, sandbox only (listed in `values-dev.yaml`'s `apps` files; production's `apps/*.yaml` never sees it). It contains:
  - a Deployment `main` with `replicas: 0`, base image `docker.io/argoproj/rollouts-demo:blue` pinned by digest, and `imagePullSecrets: [zot-pull]`;
  - two Services `main` (stable) and `canary`;
  - an HTTPRoute `delivery-test.<domain>` on the LAN Gateway with backendRefs `main` (weight 100) and `canary` (weight 0);
  - `rawResources`:
    - a `Rollout` with `workloadRef` to the Deployment (`scaleDown: onsuccess`), canary steps `setWeight 20, pause 1m, setWeight 50, pause 1m`, `trafficRouting.plugins.argoproj-labs/gatewayAPI` naming the HTTPRoute, and `analysis` from step 1 on `gateway-success-rate` with `service: delivery-test-canary`;
    - Kargo resources in namespace `delivery-test`:
      - a `Project`;
      - a `Warehouse` on `registry.<domain>/delivery-test` that selects the newest build;
      - a `Stage` `verify`. Its `verification` is an `AnalysisTemplate` that runs `cosign verify` with `ci/ci-signing-public-key` as a Job. The image is cosign's, pinned. The Job is allowed by the namespace's `allow` and egress.
      - a `Stage` `production` that takes freight only from `verify` (`sources.stages: [verify]`), with `autoPromotionEnabled: true`. Its promotion steps are `git-clone` of `ops/releases`, `yaml-update` of `delivery-test/production.yaml`, `git-commit`, `git-push`, and `argocd-update` for Application `delivery-test`.
- Modify: `system/argocd/values-dev.yaml` (the fixture in the sandbox's `apps` files and in `delivered`), `system/secret-store/values.yaml` and `platform/generated-secrets/values.yaml` (`delivery-test` pulls), `system/egress/values.yaml` if the Job needs it, `test/scopes.yaml` (label `delivery`: `system/argo-rollouts/**`, `platform/kargo/**`, `test/fixtures/apps/delivery-test.yaml`, `platform/ci/**`)
- Create: `test/e2e/app_delivery_test.go`, `Describe("AppDelivery", Label("sandbox", "delivery"), Ordered)`

**Interfaces:**
- Consumes: Tasks 1 to 5; Backstage's template `ci-repository` (from the spec's Backstage-1 test helpers in `test/e2e/backstage_test.go`) and the CI's `pipeline` (from `test/e2e/ci_test.go` helpers).

- [ ] **Step 1: Write the spec**, `Ordered`, with `SpecTimeout`s:
  1. `"creates its repository and builds the first version"`: the Backstage template creates `ci/delivery-test` (reuse the BackstageTemplate helper). Its Containerfile serves `200 ok-<sha>` on port 8080. Wait for commit status `ci/pipeline` success.
  2. `"verifies and promotes it to production by itself"`: Freight with that digest reaches `verify` then `production`. `ops/releases` holds `delivery-test/production.yaml` with the tag. The Rollout is `Healthy`, and `https://delivery-test.127-0-0-1.nip.io` returns `ok-<sha>`.
  3. `"never promotes an image the CI did not sign"`: push an unsigned tag to zot with the admin account. Its Freight fails `verify`, and `production` keeps the previous tag for 3 minutes.
  4. `"aborts a version that serves errors and keeps users on the last good one"`: push a commit whose server answers 500 to every request. While the Rollout is at 20%, drive 10 requests per second through the Gateway. The analysis fails, and the Rollout is `Degraded` with the abort set. Requests return the previous `ok-<sha>` within 2 minutes of the abort. `RolloutAborted` fires in Alertmanager.
  5. `"is undone by promoting the previous freight"`: promote the previous Freight through Kargo's API as `dev-admin`. The Rollout is `Healthy` on it.

  Each spec cleans up after itself in `DeferCleanup`: the repository in Forgejo, the release file, and the zot tags.
- [ ] **Step 2: Run** `just dev --tests 'delivery'`. Expected: FAIL first on the missing fixture; fix only what the spec shows, by the tasks' interfaces.
- [ ] **Step 3: Run** `just dev --tests 'delivery'` until PASS, then `just dev` (the branch's whole selection). Expected: PASS.
- [ ] **Step 4: Commit** `test(delivery): the app road end to end in the sandbox`

### Task 7: Docs, threat model, CLAUDE.md

**Files:**
- Create: `docs/how-to-guides/app-delivery.md`, covering:
  - the two roads;
  - how an app joins the fast one: the `delivered` list, `zotPull`, its Kargo resources, its Rollout;
  - approving a promotion in Kargo's UI;
  - rolling back by promoting the previous Freight;
  - what to do with a `Degraded` Rollout;
  - the sandbox's measured memory.
- Modify:
  - `docs/how-to-guides/in-cluster-ci.md` (a link);
  - `docs/reference/architecture/overview.md`;
  - `mkdocs.yml` nav;
  - `docs/superpowers/specs/2026-11-14-threat-model-design.md`: Kargo as a git writer and UI, user `kargo`'s token, the pull account, editors losing zot write, `verify` as the signature gate while Kyverno is in Audit;
  - `CLAUDE.md`:
    - the Roadmap: this step done, before the blog;
    - a Sandbox bullet: Kargo and Argo Rollouts run there, and the suite syncs Forgejo first;
    - "Before the first production deploy": nodes pulling zot by hosts entry only until LAN DNS, the CoreDNS rewrite for `registry.`, Kargo and Rollouts only ever ran in the sandbox, and the `trivy-server` StatefulSet must be deleted once on a cluster that ran it before #103;
    - the zot note: pull credentials now exist;
  - `docs/superpowers/deferred-minors.md` (Forgejo restart mid-promotion not driven by a spec);
  - `cli/internal/rotate/catalog.yaml` docs if the guide lists entries.
- [ ] **Step 1: Write** the docs above.
- [ ] **Step 2: Run** `scripts/ci-check all`. Expected: PASS.
- [ ] **Step 3: Commit** `docs(app-delivery): the guide, the threat model and CLAUDE.md`
