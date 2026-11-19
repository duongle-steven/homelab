# Backstage-1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Backstage runs on the LAN behind Kanidm, with the homelab's catalog, TechDocs, a template creating repositories in Forgejo org `ci`, and the Kubernetes, Argo CD and alert views; homepage and the catalog are generated from one description per app.

**Architecture:** The app's source lives in `backstage/` (made by `@backstage/create-app`), built like lab's image: from the checkout in the sandbox, by GitHub Actions (signed keyless, SBOM attested, checked by Kyverno) for production. `platform/backstage` wraps the upstream `backstage` chart with a CloudNativePG database, a Kanidm client, ESO secrets and read-only RBAC; in the sandbox it is an optional component. `lab catalog` generates the catalog and homepage's list from descriptions kept in each app's and component's own files.

**Tech Stack:** Backstage (latest stable, 1.55 or later), Node (the version create-app pins), the `backstage` Helm chart (backstage/charts), CloudNativePG, kaniop, ESO, Go (`cli/`), Ginkgo (`test/e2e`).

**Spec:** `docs/superpowers/specs/2026-11-16-backstage-design.md`

## Global Constraints

- Latest stable release of every package, chart and image, pinned exactly (`CLAUDE.md` Rules); no `latest` tags.
- Host `backstage.<domain>` on `istio-system/gateway` only; never on Gateway `public`.
- Sign-in only for `homelab-admins` and `homelab-editors` (Kanidm `scopeMap` and the sign-in resolver).
- Templates executable only by `homelab-admins`; editors may refresh entities, never register, unregister or execute.
- Kubernetes reader: one read-only ClusterRole, no Secrets, no `pods/log`, no `pods/exec`.
- Scaffolder actions installed: `fetch:*`, `publish:gitea`, `catalog:register`, `debug:log` only.
- Every secret has an entry in `cli/internal/rotate/catalog.yaml`; every PVC its `homelab/backup` label (`backstage-db`: `none`).
- In the sandbox Backstage syncs only with the cluster Secret label `homelab/app-backstage`.
- Kanidm client: confidential, PKCE (sent by default), `tokenSignedResponseAlg: ES256`.
- The threat model, `CLAUDE.md` and `docs/superpowers/deferred-minors.md` are updated in the same PR.

## Review Focus

- A Kanidm user only in `homelab-users` reaches `backstage.<domain>`: Kanidm refuses at its consent step, and if a token were obtained the resolver refuses (no catalog `User`). Pinned in Task 6's spec.
- An editor calls the scaffolder or catalog API directly (not through the UI): the permission policy refuses `scaffolder.task.create` and `catalog.location.create`/`delete` server-side. Pinned in Task 2's policy tests and Task 6's spec.
- A description added to an app file but the generated files not regenerated: `lab catalog check` fails the basic checks. Pinned in Task 1.
- An app file with several routes (`apps/jellyfin.yaml`: Jellyfin, Transmission, Prowlarr, Radarr, Sonarr, Jellyseerr): each route keeps its own homepage entry and catalog component. Pinned in Task 1's tests.
- Backstage restarted with the database gone (sandbox fresh, or `backstage-db` recreated): the catalog rebuilds from its locations and LDAP without manual steps. Pinned in Task 6's spec (fresh install).

---

### Task 1: One description per app; `lab catalog` generates the catalog and homepage's list

**Files:**
- Create: `cli/internal/catalog/catalog.go`, `cli/internal/catalog/catalog_test.go`, `cli/cmd/lab/catalog.go` (follow how existing commands register in `cli/cmd/lab`)
- Create: `catalog/` (generated: `catalog/all.yaml`, a `Location` listing one file per system; `catalog/systems.yaml`; `catalog/components/*.yaml`)
- Modify: every `apps/*.yaml` (a top-level `homelab:` key; app-template's schema allows unknown keys), every `system/*/Chart.yaml` and `platform/*/Chart.yaml` (`annotations:`), `apps/homepage.yaml` (its `services.yaml` generated; external links under its own `homelab.links`)
- Modify: `scripts/ci-check` (a check `catalog` running `lab catalog check`), `test/scopes.yaml` (`catalog/**` skipped: read only by Backstage, which Task 6's specs cover through `platform/backstage/**`)

**Interfaces:**
- Produces: `lab catalog generate` (writes `catalog/` and the `services.yaml` block of `apps/homepage.yaml`), `lab catalog check` (exit 1 and a diff when either differs). Entity names: an app's route `<file>-<route>` collapsed to `<file>` when a file has one route; components `<dir>`; owner `group:default/homelab-admins`; systems `homelab-apps`, `homelab-platform`, `homelab-system` under domain-less system `homelab`.
- Description schema, in an app file (one item per route; `route` names the app-template route key):

```yaml
homelab:
  - route: main
    name: Jellyfin
    description: Films and series for the family
    group: Popular          # homepage's group
    icon: jellyfin.svg      # homepage's icon
    lifecycle: production
```

  and in a `Chart.yaml`: `annotations: {homelab/name, homelab/description, homelab/group, homelab/icon, homelab/host (optional)}`. A component without a host gets a catalog entity and no homepage entry.

- [ ] **Step 1: Write the failing tests** in `catalog_test.go`: `TestGenerateFromApps` (a fixture app with two routes yields two components and two homepage entries with hosts `<host>.{{ .Values.global.baseDomain }}` taken from the route's `hostnames`), `TestGenerateFromCharts` (a fixture `Chart.yaml` without `homelab/host` yields a component and no homepage entry), `TestCheckReportsDrift` (a changed description makes `Check` return an error naming the file), `TestHomepageKeepsLinks` (entries under `apps/homepage.yaml`'s `homelab.links` are copied after the generated groups, unchanged).
- [ ] **Step 2: Run** `just cli test` — Expected: FAIL (package `catalog` does not exist).
- [ ] **Step 3: Implement** `Generate(root string) (map[string][]byte, error)` (path → content) and `Check(root string) error` in `catalog.go`; the command writes or compares. Output sorted and stable (the check diffs it).
- [ ] **Step 4: Run** `just cli test` — Expected: PASS.
- [ ] **Step 5: Describe every app and component**: move every entry of homepage's hand-written list into its app's `homelab:` key (same name, description, group, icon), external links into `homelab.links`; annotate every `system/*` and `platform/*` `Chart.yaml`. Run `go run ./cmd/lab catalog generate` from `cli/`.
- [ ] **Step 6: Verify homepage lost nothing**: an offline test `TestHomepageServicesKept` in `test/offline` asserting the rendered `services.yaml` holds every `href` the list held before this task (the list frozen in the test).
- [ ] **Step 7: Run** `scripts/ci-check catalog render` and `go test ./offline -run 'TestHomepage'` from `test/` — Expected: PASS.
- [ ] **Step 8: Commit** `feat(catalog): one description per app; lab catalog generates the catalog and homepage's list`

### Task 2: The Backstage app (`backstage/`)

**Files:**
- Create: `backstage/` from `npx @backstage/create-app@latest` (default backend and frontend systems), then: `packages/backend/src/index.ts` (modules below), `packages/backend/src/permissions/policy.ts` and `policy.test.ts`, `packages/backend/src/instrumentation.js` (OpenTelemetry Node SDK, OTLP HTTP to `OTEL_EXPORTER_OTLP_ENDPOINT`), `app-config.yaml`, `app-config.production.yaml`, the home page in `packages/app`
- Modify: `scripts/ci-check` (a check `backstage`: `yarn install --immutable && yarn tsc && yarn lint:all && yarn test:all` in `backstage/`, run only when `backstage/` changed), `.gitignore` (`backstage/node_modules`, `backstage/dist-types`, `backstage/packages/*/dist`), `flake.nix` dev shell (Node and yarn at the versions create-app pins, if not already there)

**Interfaces:**
- Produces: backend modules installed: `@backstage/plugin-auth-backend-module-oidc-provider`, `@backstage/plugin-permission-backend` with the policy below, `@backstage/plugin-catalog-backend-module-ldap`, `@backstage/plugin-scaffolder-backend-module-gitea`, `@backstage/plugin-kubernetes-backend`, `@backstage/plugin-techdocs-backend`, the community Argo CD backend if its frontend needs one, the search backend with the PostgreSQL engine. Frontend: Kubernetes, Argo CD (community), TechDocs, home page; Alertmanager alerts through Roadie's `backstage-plugin-prometheus` if it builds against this Backstage, otherwise left out and noted in `deferred-minors.md` for Backstage-2.
- Config read from the environment (names fixed for Task 5): `BACKSTAGE_BASE_URL`, `KANIDM_ISSUER`, `AUTH_OIDC_CLIENT_ID`, `AUTH_OIDC_CLIENT_SECRET`, `AUTH_SESSION_SECRET`, `BACKEND_SECRET`, `POSTGRES_HOST`, `POSTGRES_PORT`, `POSTGRES_USER`, `POSTGRES_PASSWORD`, `CATALOG_REPOSITORY` (the repository URL Argo CD reads), `CATALOG_REVISION`, `FORGEJO_HOST`, `FORGEJO_TOKEN`, `ARGOCD_URL`, `ARGOCD_USERNAME`, `ARGOCD_PASSWORD`, `LDAP_URL`, `LDAP_BIND_DN`, `LDAP_BIND_SECRET`, `ALERTMANAGER_URL`, `OTEL_EXPORTER_OTLP_ENDPOINT`.
- Sign-in resolver: `emailLocalPartMatchingUserEntityName` or the Kanidm `preferred_username` matched to a `User` entity name; no `dangerouslyAllowSignInWithoutUserInCatalog`.

- [ ] **Step 1: Generate the app** with create-app into `backstage/` (name `homelab`); commit the untouched scaffold first (`chore(backstage): the app as create-app makes it`), so later diffs show only our changes.
- [ ] **Step 2: Write the failing policy tests** in `policy.test.ts`: `admins may execute templates` (`scaffolder.task.create` → ALLOW for a user whose ownership refs include `group:default/homelab-admins`); `editors may not execute templates` (→ DENY for `group:default/homelab-editors`); `editors may refresh, not register or unregister` (`catalog.entity.refresh` ALLOW, `catalog.location.create` and `catalog.location.delete` DENY); `everyone signed in reads the catalog` (`catalog.entity.read` ALLOW for an editor).
- [ ] **Step 3: Run** `yarn test packages/backend` in `backstage/` — Expected: FAIL.
- [ ] **Step 4: Implement** `HomelabPermissionPolicy implements PermissionPolicy` in `policy.ts`, registered as the permission backend's policy; the scaffolder's action allow-list in `app-config.yaml` (Global Constraints).
- [ ] **Step 5: Run** `yarn test packages/backend` — Expected: PASS.
- [ ] **Step 6: Wire the modules and config** (Interfaces), the home page (starred entities, recently visited docs, links to `https://argocd.${domain}` and `https://grafana.${domain}` from config), TechDocs `builder: local`, `publisher: local`, catalog locations: `${CATALOG_REPOSITORY}` at `${CATALOG_REVISION}` path `catalog/all.yaml`, LDAP: groups `homelab-admins` and `homelab-editors` and their members only.
- [ ] **Step 7: Run** `scripts/ci-check backstage` — Expected: PASS (`tsc`, lint, tests).
- [ ] **Step 8: Commit** `feat(backstage): the homelab's app: sign-in, permissions, catalog, TechDocs, the plugins`

### Task 3: The image, built like lab's

**Files:**
- Create: `backstage/Dockerfile` (multi-stage: build with yarn; runtime on the slim Node image create-app's docs use, Python with `mkdocs-techdocs-core` and every MkDocs plugin and extension `mkdocs.yml` uses, numeric non-root user), `scripts/build-backstage-image` (builds `homelab-backstage:dev` and `k3d image import`s it, like `scripts/build-lab-image`), `.github/workflows/backstage-image.yaml` (from `lab-image.yaml`: on `master` when `backstage/**` changes, `ghcr.io/<owner>/homelab-backstage:<sha>`, keyless signature, SPDX SBOM attested), `system/kyverno/templates/backstage-image-policy.yaml` (from `lab-image-policy.yaml`: production only, subject the new workflow)
- Modify: `justfile` (`just dev` runs `scripts/build-backstage-image` when Backstage is among the sandbox's optional apps), `test/offline` (a `TestBackstageImagePolicy` beside `TestLabImagePolicy`), `test/scopes.yaml`

- [ ] **Step 1: Write the failing test** `TestBackstageImagePolicy`: the production render of `system/kyverno` holds `verify-backstage-image` matching `ghcr.io/*/homelab-backstage*` with the workflow's identity `.github/workflows/backstage-image.yaml@refs/heads/master`; the sandbox render does not.
- [ ] **Step 2: Run** it — Expected: FAIL.
- [ ] **Step 3: Implement** the policy, the workflow, the Dockerfile and the script.
- [ ] **Step 4: Run** `TestBackstageImagePolicy`, `scripts/ci-check iac workflows lint`, and `scripts/build-backstage-image` against the running sandbox — Expected: PASS; `docker run --rm homelab-backstage:dev node --version` and `mkdocs --version` answer; the image's user is numeric and not 0.
- [ ] **Step 5: Commit** `feat(backstage): its image, built from the checkout in the sandbox and signed by GitHub Actions for production`

### Task 4: Kanidm, Forgejo and Argo CD give Backstage its identities

**Files:**
- Create: `platform/backstage/templates/kanidm-client.yaml` (as `platform/forgejo/templates/kanidm-client.yaml`, `scopeMap` only `homelab-admins` and `homelab-editors`, redirect `https://backstage.<domain>/api/auth/oidc/handler/frame`)
- Modify: `platform/kanidm` (LDAPS enabled through kaniop's `Kanidm` resource, reachable only from namespace `backstage`: its `allow` policy; a service account `backstage-ldap` able to read the two groups and their members) — if kaniop cannot enable LDAP, a backend module of our own reading Kanidm's REST API with that service account's API token replaces the LDAP module (record the choice in the plan's notes and the spec's design)
- Modify: `platform/kanidm/values.yaml` (`oauth2ClientNamespaces` gains `backstage`), `cli/internal/forgejo/sync.go` (a user `backstage` in a team `templates` of org `ci` with "create repositories" only, and read access to org `ops`; its token written to OpenBao `backstage/forgejo-token`), `system/argocd/values.yaml` (a local account `backstage` with `login`, RBAC `role:readonly`; its password generated in OpenBao and bcrypted into `argocd-secret` by an ExternalSecret template)
- Modify: `cli/internal/rotate/catalog.yaml` (entries `kanidm-client.backstage`, `forgejo.backstage-token` (workstation, like `forgejo.ci-bot-token`), `argocd.backstage-password`, `kanidm.backstage-ldap`, `backstage.session-secret`, `backstage.backend-secret`), `platform/generated-secrets/values.yaml`
- Test: `cli/internal/forgejo` (`TestSyncCreatesBackstageUser`, beside `ci-bot`'s tests), `lab secrets check`

- [ ] **Step 1: Write the failing test** `TestSyncCreatesBackstageUser`: after sync, user `backstage` exists, is in team `templates` of `ci` whose units allow creating repositories and nothing on existing ones, can read `ops/homelab`, and a token is written to `secret/backstage/forgejo-token`; a second sync changes nothing.
- [ ] **Step 2: Run** `just cli test` — Expected: FAIL.
- [ ] **Step 3: Implement** the Forgejo user and team in `sync.go`.
- [ ] **Step 4: Run** `just cli test` and `scripts/ci-check rotation-catalog secret-stores render` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(backstage): its Kanidm client and directory reader, Forgejo user and Argo CD account`

### Task 5: `platform/backstage`, an optional component in the sandbox

**Files:**
- Create: `platform/backstage/Chart.yaml` (the upstream `backstage` chart at its latest version; the component's `homelab/*` annotations from Task 1), `values.yaml` (image from Task 3 by digest in production, the environment of Task 2's Interfaces, bundled PostgreSQL off, securityContext per the policy rules, `backstage.io/kubernetes-id: backstage`), `values-dev.yaml` (`homelab-backstage:dev`, `imagePullPolicy: Never`, smaller resources, `NODE_EXTRA_CA_CERTS` from `homelab-dev-ca`), `templates/database.yaml` (CloudNativePG `backstage-db`, one instance, `homelab/backup: none`, its `PeerAuthentication` for the operator as `forgejo-db`'s), `templates/httproute.yaml`, `templates/external-secrets.yaml`, `templates/authorization-policy.yaml`, `templates/rbac.yaml` (the read-only ClusterRole: Global Constraints)
- Modify: `system/argocd/values-dev.yaml` (a matrix generator on `homelab/app-backstage: "true"` for `platform/backstage`, as paperless's), `system/argocd/values.yaml` (production's list), `justfile` (`optional_apps` gains `backstage`, its paths `backstage/**` and `platform/backstage/**`), `system/opentelemetry/values.yaml` (`senders` gains `backstage`), `system/egress/values.yaml` (`backstage`: 443 to GitHub in the sandbox only), `scripts/check-backup-groups` (`EXPECTED`), `test/scopes.yaml` (`[backstage/**, platform/backstage/**] → backstage`)

- [ ] **Step 1: Write the failing offline test** `TestBackstageRender` in `test/offline`: the route's only parent is `istio-system/gateway`; the ClusterRole has no `secrets`, `pods/log` or `pods/exec`; `backstage-db` has one instance and `homelab/backup: none`; the sandbox's root ApplicationSet selects `platform/backstage` only under label `homelab/app-backstage`.
- [ ] **Step 2: Run** it — Expected: FAIL.
- [ ] **Step 3: Implement** the chart and the wiring.
- [ ] **Step 4: Run** `scripts/ci-check all` — Expected: PASS (render, policy, mesh, backup groups, env config, secret stores, rotation catalog, scope check).
- [ ] **Step 5: Run** `just dev --apps backstage --tests 'sso || mesh || policy'` — Expected: PASS; `https://backstage.127-0-0-1.nip.io` signs in as the smoke admin.
- [ ] **Step 6: Commit** `feat(backstage): platform/backstage, optional in the sandbox`

### Task 6: Specs, docs, threat model

**Files:**
- Create: `test/e2e/backstage_test.go` (label `backstage`; skipped when the component is absent, failing when `HOMELAB_SANDBOX_APPS` names it, as paperless's specs), `docs/how-to-guides/backstage.md`, `backstage/templates/ci-repository/` (`template.yaml` and its `skeleton/`: `Containerfile`, `.ci.yaml`, `catalog-info.yaml`, `README.md`)
- Modify: `docs/superpowers/specs/2026-11-14-threat-model-design.md` (E2 gains Backstage; E6 widened by the template), `docs/reference/architecture/overview.md`, `mkdocs.yml` (nav), `CLAUDE.md` (roadmap: Backstage-1 in progress, Backstage-2 after the blog; Ideas: PagerDuty only for phone calls that wake, with a dead man's switch; an on-call schedule once a second person is on call; "before the first production deploy": the image digest pinned after the first release, Forgejo and Kanidm LDAP in production unproven), `docs/superpowers/deferred-minors.md`

- [ ] **Step 1: Write the specs** in `backstage_test.go`: `BackstageSignIn` (admin and editor land on the home page; a `homelab-users`-only person is refused), `BackstageCatalog` (every component `lab catalog` generated is listed with owner `group:default/homelab-admins`; `User` entities exist for the smoke admin and editor), `BackstageTechDocs` (the root entity's docs page renders the title of `docs/index.md`), `BackstageTemplate` (as admin, the template creates `ci/bs-test-<n>`, its golden-path Workflow ends `Succeeded`, the entity appears; as editor, `POST /api/scaffolder/v2/tasks` answers 403; the repository is deleted after), `BackstagePlugins` (the `backstage` component's Kubernetes and Argo CD endpoints answer with its Deployment and Application), `BackstageTraces` (a span with `service.name` `backstage` reaches Tempo through Grafana's data source proxy, as `TracesFromGateway`).
- [ ] **Step 2: Run** `just dev --apps backstage --tests backstage` — Expected: PASS. Then `just dev-fresh` once on the branch before the PR (the Review Focus's fresh-install case).
- [ ] **Step 3: Write the docs and the threat model's and `CLAUDE.md`'s changes**; run `scripts/ci-check all` — Expected: PASS (mkdocs strict included).
- [ ] **Step 4: Commit** `test(backstage): sign-in, catalog, TechDocs, the template, the plugins and traces; docs and threat model`
- [ ] **Step 5: Open the PR** into `dev`, labelled `sandbox` (it changes the first install of an optional component and a GitHub Actions workflow).
