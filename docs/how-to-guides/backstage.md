# Backstage

Backstage (`backstage.<domain>`, LAN only) is the homelab's developer portal: a catalog of every app and component, this
repository's docs (TechDocs), and a template that starts a repository in Forgejo's org `ci`
(`docs/superpowers/specs/2026-11-16-backstage-design.md`). Homepage stays the family's start page; Backstage is for
whoever runs the homelab.

## Who signs in

Only `homelab-admins` and `homelab-editors` (Kanidm's client `backstage` admits no one else). Backstage takes who you
are from Kanidm's token: your name and those groups (`backstage/packages/backend/src/auth/identity.ts`); people are not
in the catalog. Admins may do everything; editors read and refresh, but neither run templates nor add or remove what
the catalog reads (`permissions/policy.ts`).

Someone removed from those groups keeps their access until Backstage refreshes their token (about an hour): each
refresh trades Kanidm's refresh token for a new one and runs the sign-in again (`createOAuthRouteHandlers` in
`@backstage/plugin-auth-node`), which then refuses them. Read from the code, not measured. Even an admin's template runs only `fetch:*`, `publish:gitea`,
`catalog:register` and `debug:log`.

## What the catalog shows

Every app and component describes itself once, in its own files: a `homelab:` list in `apps/<name>.yaml` (one item per
route) or `homelab/*` annotations in a chart's `Chart.yaml`. `lab catalog generate` writes the catalog (`catalog/`,
`catalog-info.yaml`) and Homepage's services from them; the basic check `catalog` fails when either is stale.

```yaml
homelab:
  - route: main            # the app-template route whose host Homepage links (omit: no page)
    name: Jellyfin
    description: Media system (movies, music, etc.)
    group: Popular         # Homepage's group (omit: not on Homepage)
    icon: jellyfin.svg
```

Backstage reads the catalog from the repository Argo CD syncs (`global.gitRepository` at `global.gitRevision`): GitHub's
branch `sandbox` in the sandbox, Forgejo's `ops/homelab` in production. A component's page shows its pods (the
Kubernetes plugin: no Secrets, ConfigMaps, logs or exec) and its Argo CD Application (account `backstage`, read-only).

## A new repository

**Create** → "A repository in org ci" (admins): a name and a description make a repository in Forgejo's org `ci` with a
`Containerfile`, a `README.md` and its `catalog-info.yaml`, registered in the catalog. Its first push runs the golden
path (`in-cluster-ci.md`). Backstage publishes as Forgejo's user `backstage` (team `templates`: creates repositories),
whose token `lab infra forgejo sync` writes; Forgejo makes it an admin collaborator of each repository it creates (the
threat model's G7).

A template's registration lives only in Backstage's database (`backstage-db`, never backed up). After a restore or a
lost database, the generated catalog comes back by itself but repositories the template made do not: register each
again (**Create** → **Register Existing Component**, its `catalog-info.yaml` on Forgejo). A lost volume does not heal a
one-instance CloudNativePG cluster: `kubectl -n backstage delete cluster backstage-db`, and Argo CD recreates it.

### Production's first deploy

Backstage reads its Forgejo token only at start, and the token exists only once `lab infra forgejo sync` has run. After
the first `just infra deploy production`, refresh its secret and restart it, or its catalog (read from `ops/homelab`)
and the template stay broken:

```sh
kubectl -n backstage annotate externalsecret backstage-forgejo force-sync="$(date +%s)" --overwrite
kubectl -n backstage rollout restart deployment/backstage
```

## Docs

TechDocs builds this repository's MkDocs site on first view, inside Backstage's pod, from the Domain `homelab`.

## The image

Backstage's app is `backstage/` (Backstage 1.56, made by `@backstage/create-app`). The sandbox builds it from the
checkout (`scripts/build-backstage-image`, run by every `just dev`); production runs
the image `.github/workflows/backstage-image.yaml` builds from `master`, signed keyless with its SBOM attested, which
Kyverno's `verify-backstage-image` checks. After the first release builds it, pin its digest in
`platform/backstage/values.yaml` and drop `platform/backstage`'s exclusion from `system/argocd/values.yaml`.

The sandbox always runs it (`just dev` builds its image each time; Docker reuses the layers that did not change). Its
specs are labelled `backstage` (`just dev --tests backstage`).
