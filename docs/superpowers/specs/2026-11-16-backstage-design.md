# Backstage-1: a developer portal for the homelab

Status: designed in chat 2026-11-15/16; the builder check's three questions are settled below (who signs in, homepage's
list, the blog's repository). Decisions marked *(user)* were made in chat; *(Claude's call)* marks the
builder's own, open to review. Builds on 10f (the in-cluster CI and its golden path), 15e (lab's signed image), the
OpenTelemetry step (traces) and the threat model (`docs/superpowers/specs/2026-11-14-threat-model-design.md`).

## Intent

*(user)* Backstage is here to be learned by running it: the user does not yet know what it is for, the CNCF's
Certified Backstage Associate (part of the Golden Kubestronaut goal) tests it, and a homelab with a catalog, its own
docs and repositories made from templates is a real case to learn on. The order agreed on 2026-11-13 stands: the blog's
repository is the first one a Backstage template creates.

The step is split *(user)*: **Backstage-1** (this spec) is the platform: the app, sign-in, the catalog, TechDocs, the
template and the plugins that come ready-made. **Backstage-2**, a step of its own after the blog and before G2, writes a
plugin of our own (the CBA tests it; for example Kyverno's PolicyReports per component) and scorecards from this
repository's rules if they fit.

## Goals

1. Backstage runs at `backstage.<domain>` on the LAN Gateway only, every page behind Kanidm sign-in.
2. The catalog describes the homelab: every app in `apps/*.yaml` and every component in `system/` and `platform/`, each
   with an owner, a lifecycle, the namespace it runs in, its links (its page, Argo CD, Grafana), and the relations
   between them where they are known (an app's database, the components it needs).
3. Who signs in comes from Kanidm's token: its name and its groups `homelab-admins` or `homelab-editors` (the client's
   claim `homelab_groups`), so ownership and permissions name real groups *(user, 2026-11-17: option B; people stay out
   of the catalog)*. Only `homelab-admins` and `homelab-editors` sign in *(user, 2026-11-16)*: Backstage is a tool for
   running the homelab, and the family's apps never need it.
4. TechDocs serves this repository's MkDocs site (`docs/`) inside Backstage.
5. A template creates a repository in Forgejo org `ci` that the golden path (10f) builds, signs and attests, and
   registers it in the catalog. This step delivers the template with a generic skeleton, proven in the sandbox; the
   blog's repository is created with it in the blog's own step *(Claude's call)*.
6. The permission framework allows templates only to `homelab-admins` *(user, the threat model's scaffolder concern)*;
   editors read everything else (catalog, docs, the plugins' tabs) and may refresh entities, but neither execute
   templates nor register or unregister entities *(user, 2026-11-16)*.
7. A component's page shows its Kubernetes workloads (the Kubernetes plugin), its Argo CD Application's sync and health,
   and its open alerts from Alertmanager (if a ready-made plugin fits Backstage 1.55; otherwise that view moves to
   Backstage-2's own plugin).
8. A home page for operators: starred components, recent docs, links to Argo CD and Grafana (the CBA's "customise the
   home page").
9. Backstage exports traces to the OpenTelemetry gateway (`system/opentelemetry`), the first app instrumented with an
   SDK (the OpenTelemetry step left auto-instrumentation to "Backstage, the next step").

## Non-goals

- **Replacing homepage** *(user)*: homepage stays the family's front door (light, no sign-in, links only); Backstage is
  for whoever runs the homelab. Both are generated from the same per-app description (below), so neither keeps a list
  of the homelab's apps by hand.
- **PagerDuty or any on-call service** *(user)*: its Backstage plugin needs a paid plan; ntfy and Telegram stay the
  alert paths. An on-call schedule (a file in git, Alertmanager routing to the person on call, a plugin showing who)
  waits for a second person on call (Ideas).
- **A template that changes this repository** (an "add an app" pull request on GitHub): it needs write access to
  GitHub from the cluster; an idea, weighed against the threat model when wanted.
- **API entities, Tech Radar, cost insights**: nothing in the homelab has an API of its own yet.
- **Search beyond the built-in PostgreSQL engine**: no Elasticsearch or OpenSearch.

## Anti-goals

- Backstage reachable from the internet, or any page readable without signing in.
- A scaffolder anyone can run, or one that can run arbitrary actions: only the actions the templates need are installed,
  and only `homelab-admins` may execute templates (`fetch:*`, `publish:gitea`, `catalog:register`, and `debug:log` at
  most; no `fs:delete` outside the workspace, no shell action).
- A Backstage credential that can write to the cluster or to Argo CD: the Kubernetes and Argo CD plugins read only.
- Daily `just dev` paying Backstage's memory: in the sandbox it is an optional component *(user)*.
- Two lists of apps kept by hand (homepage's and the catalog's) that drift apart.

## Constraints

- **The sandbox is often off** *(user, 2026-11-16)*: it runs while a feature is built or a bug fixed. Nothing may assume
  it stays up; the specs pass on a fresh install.
- **Resources**: the sandbox (13 GB WSL) is tight; Backstage (Node, a few hundred MB) plus its PostgreSQL joins only
  when asked, like paperless and OpenCloud (14e).
- **Kanidm**: confidential client, PKCE required (Backstage's OIDC provider sends it: `openid-client` 5's strategy
  defaults to S256 when the issuer lists it, as Kanidm does), ID tokens ES256 (`tokenSignedResponseAlg: ES256`; Kiali's
  RS256 exception is not needed).
- **Rules of this repository** (`CLAUDE.md`): latest stable everything pinned (Backstage 1.55 or later at build time),
  every secret in the rotation catalog, every PVC labelled for backups, every workload's securityContext, the mesh's
  `allow` policy, egress by table, `test/scopes.yaml` entry, the threat model updated in the same PR (Backstage is a new
  entry point on E2 and widens E6).

## Approaches

Where the app's source lives and who builds its image (Backstage has no image to pull: an app is generated with
`@backstage/create-app`, plugins added in code, then built):

- **A. Source in this repository (`backstage/`), built the way lab's image is** *(user, chosen)*: the sandbox builds it
  from the checkout and imports it into k3d (like `scripts/build-lab-image`); production runs the image GitHub Actions
  builds from `master`, signed keyless with its SPDX SBOM attested and checked by Kyverno (like `lab-image.yaml` and
  `verify-lab-image`). Renovate bumps its dependencies; changes go through pull requests like everything else. The
  in-cluster CI (10f) does not build Backstage; it builds what Backstage's template creates, starting with the blog.
- B. Source here, built by the in-cluster CI: `lab infra forgejo push` would split `backstage/` into `ci/backstage`
  for the golden path. Rejected: every sandbox with Backstage would build it inside the cluster (Buildah, vfs, 10–20
  minutes and several GB of memory), and Argo CD would wait for an image that may not exist yet.
- C. A repository of its own in Forgejo org `ci`. Rejected: the sandbox's Forgejo is empty, Renovate cannot see it, and
  review happens outside GitHub.

This changes 2026-11-13's "its own app image built by 10f's CI": the golden path is exercised by the apps Backstage
creates, not by Backstage itself.

## Design

### The app

- `backstage/`: an app made by `@backstage/create-app` at the latest stable release, with the backend system and the
  frontend system it generates by default *(Claude's call: the CBA follows upstream's current defaults)*; its
  `Dockerfile` multi-stage (build, then a slim Node runtime, a numeric non-root user), Python with `mkdocs-techdocs-core`
  and the MkDocs plugins `docs/` uses, for TechDocs' local builder.
- Config in `app-config.yaml` and `app-config.production.yaml`: nothing environment-specific (domain, repository) in
  the image; the chart passes them as environment variables from `global.*` and the cluster Secret.
- `scripts/build-backstage-image` (the sandbox) mirrors `scripts/build-lab-image`; `.github/workflows/backstage-image.yaml`
  mirrors `lab-image.yaml` (from `master` when `backstage/` changes); Kyverno's `verify-backstage-image` (production)
  mirrors `verify-lab-image`. The image's digest is pinned in the chart's production values, set after the release
  that builds it, as lab's is.
- The yarn workspace's tests (`yarn tsc`, `yarn lint`, `yarn test`) join the basic checks (`scripts/ci-check`) when
  `backstage/` changes.

### The deployment: `platform/backstage`

- A wrapper chart on the upstream `backstage` Helm chart (backstage/charts) *(Claude's call: the chart CBA material
  uses; its bundled PostgreSQL off)*, namespace `backstage`, in the mesh with its `allow` policy (its Gateway route,
  Prometheus).
- PostgreSQL: a CloudNativePG `Cluster` `backstage-db`, one instance in both environments *(Claude's call: the catalog
  and search rebuild from their sources; scaffolder task history and user settings are the only state, not worth a
  second instance)*, its PVC `homelab/backup: none`.
- The route: `backstage.<domain>` on `istio-system/gateway` (LAN) only.
- Sign-in: a `KanidmOAuth2Client` `backstage` (confidential, `homelab_groups` in its claim map, a `scopeMap` admitting
  only `homelab-admins` and `homelab-editors`, as Actual Budget and paperless admit their access groups), namespace added
  to `oauth2ClientNamespaces`; Backstage's OIDC provider with a sign-in resolver matching the Kanidm user's name to a
  the identity from the token, refusing anyone in neither group (Kanidm already refuses the rest; the resolver is the second gate).
- Secrets in OpenBao through ESO, each with its rotation-catalog entry: the auth session secret, the backend's service
  token keys, the Forgejo token, the Argo CD token (the Kanidm client's secret is kaniop's, the database's
  CloudNativePG's).
- In the sandbox an optional component: synced only when the cluster Secret carries `homelab/app-backstage`, which
  `just dev --apps backstage|all` or a change under `backstage/` or `platform/backstage/` sets (the justfile's
  `optional_apps` learns paths outside `apps/`); Actions' release, retarget and on-demand sandboxes run `--apps all`.
- Traces: the OpenTelemetry Node SDK in the backend, exporting OTLP to `gateway-collector.opentelemetry:4318`;
  `backstage` joins the gateway's `senders`.
- Egress: in the sandbox, GitHub (443) for the catalog and TechDocs (below); production needs none (everything it reads
  is in the cluster).

### Catalog

- What Backstage reads is the repository Argo CD reads (the cluster Secret's `repository` and `revision`, step 14g):
  GitHub's `sandbox` branch in the sandbox, Forgejo's `ops/homelab` in production, so the catalog matches what runs. In
  production through the Gitea integration with a read-only Forgejo token *(Claude's call: a Forgejo user `backstage`,
  created by `lab infra forgejo sync` like `ci-bot`)*.
- One description per app *(user, 2026-11-15)*: each app and component describes itself once, in its own files (name,
  description, group, icon, owner, lifecycle, its host if it has one): an app in its `apps/<name>.yaml` (one per route
  for files with several, as `apps/jellyfin.yaml`), a component in its directory. One script generates both the
  catalog's `catalog-info.yaml` entities and homepage's `services.yaml` from them, both committed, and a basic check
  fails when either differs from what the script generates *(Claude's call)*. Links to things outside the homelab
  (GitHub, Cloudflare, the cloud consoles) stay a hand-written section of homepage's config. Not homepage's Kubernetes
  discovery *(Claude's call, from the builder check)*: it would give the family's unauthenticated front door read
  access to every HTTPRoute in the cluster, and lose every entry without a route.
- People are not in the catalog *(user, 2026-11-17)*: Kanidm lets a service account read only itself over LDAP or its
  API, and a built-in read group would need Kanidm's admin and expose everyone's PII. The sign-in resolver
  (`backstage/packages/backend/src/auth/identity.ts`) issues `user:default/<name>` with ownership of
  `group:default/homelab-admins` or `-editors` from the token; `lab catalog generate` writes those two Group entities.
- Systems: `homelab` (the whole), with one system per layer (`system/`, `platform/`, apps); domains only if they earn
  their keep.

### TechDocs

- `builder: local`, `publisher: local` *(Claude's call: no object storage to publish to; the site is built on first view
  and cached on the pod's disk)*; the catalog's root entity carries `backstage.io/techdocs-ref: dir:.`, so `mkdocs.yml`
  and `docs/` render as they do in `mkdocs build --strict`. A change the TechDocs build needs in `mkdocs.yml` (the
  `techdocs-core` plugin) must keep the strict build passing.

### Templates

- One template, "a new repository in `ci`": asks for a name and a description, fetches a skeleton (a `Containerfile`,
  `.ci.yaml`, `catalog-info.yaml`, a README) from this repository, publishes it with `publish:gitea` to Forgejo org `ci`
  as user `backstage` (a member of team `developers` with repository creation only), and registers it in the catalog.
  The golden path then runs on its first push.
- Only `homelab-admins` may execute it (the permission framework's policy, in code, tested).

### Plugins

- Kubernetes: a ServiceAccount with one read-only ClusterRole on workloads and their status (no Secrets, no exec, and
  no pod logs anywhere: logs stay in Grafana, which has its own access control, so paperless's and OpenCloud's never
  show here) *(user, 2026-11-16)*; components matched by the label `backstage.io/kubernetes-id`.
- Argo CD (the community plugin): an Argo CD local account `backstage` with read-only RBAC, its token in OpenBao.
- Alertmanager's open alerts per component: a ready-made plugin if one works with Backstage 1.55 (Roadie's
  `backstage-plugin-prometheus` is the candidate), through Backstage's proxy to Alertmanager inside the cluster.

## Testing

Specs labelled `backstage` (in `test/scopes.yaml` with `backstage/**` and `platform/backstage/**`), skipped when the
optional component is absent and failing when the list names it:

- signing in through Kanidm (the browser tests' path) lands on the home page for an admin and for an editor; a user
  only in `homelab-users` is refused;
- the catalog lists the components the generator produced, with a Kanidm group as owner;
- a TechDocs page of `docs/` renders;
- the template, run as an admin, creates a repository in Forgejo org `ci` whose golden-path run succeeds, and it appears
  in the catalog; run as an editor, it is refused;
- homepage's generated list still holds every app it held, and each host it links answers;
- a component's Kubernetes and Argo CD tabs show data;
- the backend's spans reach Tempo.

Offline: the generated catalog matches the committed one; the chart's render passes policy, mesh and backup checks; the
permission policy's unit tests in `backstage/`.

## Documentation

`docs/how-to-guides/backstage.md` (signing in, adding an app's description, running the template, reading TechDocs,
building the image locally); the architecture overview; the threat model (E2 gains Backstage; E6 widens: the template
creates repositories whose Containerfiles the CI runs); `CLAUDE.md`'s roadmap (Backstage-1 in progress, Backstage-2
after the blog), its Ideas (PagerDuty or another on-call service only for phone calls that wake, with a dead man's
switch; an on-call schedule once a second person is on call) and its "before the first production deploy" list.

## Left to the builder

The exact plugin packages and versions, the chart's values, the skeleton's contents, how the
generator script reads the descriptions, the home page's layout, the image's base, and the order of the plan's tasks.
