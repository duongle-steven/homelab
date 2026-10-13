# Step 4: compact `apps/*.yaml` layout

Status: design approved in chat 2026-10-14, written spec awaiting review. Branch: `feat/apps-layout` (PR into `dev`).

## Goal

Every app-template app becomes one values file `apps/<name>.yaml`, deployed by an ApplicationSet that pins
app-template once and injects the environment (domain, public Gateway). The rendered resources stay the
same, so nothing else in the cluster notices the move. This follows upstream's `nixos` rewrite (PR #197).

## Context

- Roadmap step 4 in `CLAUDE.md`. Steps 0–3 are done (sandbox, Gateway API, latest images).
- `apps/` has 12 apps, each a wrapper chart (`Chart.yaml`, `values.yaml`):
  - 11 use app-template 5.3.0 only. Five have a `values-dev.yaml` for sandbox hosts (excalidraw also
    switches to the LAN Gateway, homepage sets `HOMEPAGE_ALLOWED_HOSTS`).
  - paperless has `templates/secret.yaml` (`ExternalSecret`); tailscale has `templates/` with an
    `ExternalSecret`, `Role`, `RoleBinding` and `ServiceAccount`.
  - matrix uses the `elementweb` and `dendrite` charts with its own `templates/httproute.yaml`.
- ArgoCD: the ApplicationSet `root` (chart `argocd-apps` in `system/argocd`) generates one Application per
  directory under `system/*`, `platform/*`, `apps/*`; the sandbox restates it with an allowlist in
  `system/argocd/values-dev.yaml`. `values-seed.yaml` is the first-install variant.
- Upstream `nixos`: ApplicationSet `apps` with a git files generator over `apps/*.yaml`, multi-source
  (app-template chart + `$values` ref), Helm parameter `global.domain`; app files template hosts as
  `'<host>.{{ .Values.global.domain }}'`. Upstream removed matrix ("will add more later").
- app-template 5.3.0 renders Helm templates in route hostnames, `parentRefs`, annotations and env values
  (checked with a render), and in `configMaps` data when `renderFileContent: true`. It has native
  `externalSecrets`, `roles`, `roleBindings`, `serviceAccount` and `rawResources`.

## Decisions

| Topic | Decision |
|---|---|
| Layout | `apps/<name>.yaml` = app-template values (no wrapper chart, no `app-template:` level) |
| matrix | Stays a directory, `apps/matrix/`, synced by `root`; it may be removed or rewritten later |
| Environment differences | Only Helm parameters from the ApplicationSet: `global.domain` and `global.publicGateway`; no per-app dev files. Add an overlay mechanism only when an app needs one |
| app-template version | Pinned once in the ApplicationSet `apps` |
| Extra resources | app-template's native `externalSecrets`, `serviceAccount`, `roles`, `roleBindings` (else `rawResources`), keeping object names |
| opencloud | Not in this step: it moves to step 5 (Kanidm OIDC directly, in the sandbox) |

## Design

### 1. ApplicationSet `apps`

- In `system/argocd/values.yaml` (and `values-seed.yaml`), under `argocd-apps.applicationsets`:
  - generator: git files `apps/*.yaml` from the same repository and revision as `root`
  - Application name, Helm release and namespace: the file name without `.yaml`
  - sources: chart `app-template` 5.3.0 from `https://bjw-s-labs.github.io/helm-charts` with
    `valueFiles: [$values/apps/<name>.yaml]` and parameters `global.domain` (`khuedoan.com`) and
    `global.publicGateway` (`homelab-public`); the git repository as `ref: values`
  - `syncPolicy` like `root`: automated prune and self-heal, retries, `CreateNamespace=true`,
    `ApplyOutOfSyncOnly=true`
- `root` keeps scanning `apps/*` directories, which now only matches `apps/matrix/`.
- Sandbox (`system/argocd/values-dev.yaml`): restate `apps` in full (Helm replaces lists) with:
  - the GitHub repository and branch `dev`, like `root`
  - a files generator limited to `apps/{actualbudget,excalidraw,homepage,pairdrop,speedtest}.yaml`
  - parameters `global.domain: 127-0-0-1.nip.io`, `global.publicGateway: homelab`
  - the five `apps/...` entries removed from `root`'s allowlist

### 2. App files

- `apps/<name>/values.yaml` → `apps/<name>.yaml` without the `app-template:` level; delete the
  directory (`Chart.yaml`, `values-dev.yaml`, `templates/`).
- Hosts and other domain strings use `{{ .Values.global.domain }}`: route hostnames, env values (e.g.
  `HOMEPAGE_ALLOWED_HOSTS`, `PAPERLESS_URL`), homepage's `services.yaml` links (with
  `renderFileContent: true`).
- Public apps (excalidraw, blog): `parentRefs[].name: '{{ .Values.global.publicGateway }}'`.
- paperless and tailscale: extra objects through app-template's native keys, same names as today.
- Kept from earlier steps: `suffix: data` PVC names, `forceRename: ollama`, homepage
  `identifier: config`, the Seerr note.
- Template references to `.Release.Name`/`.Release.Namespace` in the old `templates/` become the literal
  app name (release name = namespace = file name).

### 3. Rendering and tests

- `scripts/render-chart` also accepts a file `apps/<name>.yaml`: it renders app-template 5.3.0 with that
  file and the `global.*` parameters read from `system/argocd/values.yaml` or `values-dev.yaml` (the same
  source ArgoCD uses).
- Equivalence check: for every converted app and both environments, the new render matches the render of
  the old wrapper chart, except chart labels (`helm.sh/chart` and similar). Any other difference is a bug.
- Go tests: `smokeApps` is unchanged (route names stay).

### 4. Docs and rollout

- Docs: `docs/installation/sandbox.md` (how to add an app to the sandbox, how to render), the how-to
  pages that show app values, `CLAUDE.md` Sandbox section (layout, `global.*` parameters, the matrix
  exception).
- Running sandbox: on merge, `root` drops the five apps and deletes their resources; `apps` creates them
  again (it may report "already exists" until `root` finishes; ArgoCD retries). Sandbox data of those
  apps is lost, which is acceptable. Then `make dev`, then one fresh install.

## Acceptance criteria (on branch `dev`)

1. `apps/` contains only `*.yaml` files plus `apps/matrix/`.
2. For every app and environment, the new render equals the old one except chart labels.
3. `make dev` passes on the upgraded sandbox and on a fresh one (50 tests).
4. The app-template version appears in exactly one place.

## Risks

- **Templating coverage:** a field app-template does not template would keep a literal
  `{{ .Values.global.domain }}`. The equivalence check catches it.
- **ApplicationSet handover on the running sandbox:** both ApplicationSets briefly claim the same
  Application names. Fresh installs are unaffected.
- **Production-only apps** (blog, jellyfin, ollama, paperless, tailscale, wireguard) are only checked by
  render equivalence.

## Out of scope

- opencloud (step 5), Dex removal and Forgejo (step 5).
- Moving the ApplicationSet into OpenTofu (step 7).
- Rewriting or removing matrix.
