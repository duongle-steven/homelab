# Sub-project 1c: SSO for Grafana, ArgoCD and zot

Status: design approved in chat 2026-10-13, written spec awaiting review. Branch: `dev`.

## Goal

People log in to Grafana, ArgoCD and zot through Dex → Kanidm, and their Kanidm group decides
what they may do. Each app keeps a local break-glass account. The sandbox proves every role
with automated browser and API tests.

## Context

- Builds on 1a (kind sandbox, `values-dev.yaml` overlays, dev CA) and 1b (ESO generators,
  Kanidm via kaniop, Dex with PKCE, identity tests in `test/`).
- Specs: `2026-10-12-kind-cilium-sandbox-design.md`, `2026-10-12-sandbox-identity-design.md`.
- **Groups.** `homelab-admins`, `homelab-editors` and `homelab-users` exist in Kanidm. Today the
  `groups` claim from Dex carries SPNs such as `homelab-admins@auth.127-0-0-1.nip.io`, plus
  UUIDs and Kanidm's built-in `idm_*` groups.
- **Kanidm login.** It asks for the TOTP code before the password.
- **Grafana** (`platform/grafana`):
  - not in the sandbox allowlist
  - `generic_oauth` already points at Dex but maps no roles
  - its chart moved to `grafana-community` (13.5.0); the old repo stops at 10.5.15
- **ArgoCD:** no SSO (`dex.enabled: false`, no `oidc.config`).
- **zot:** no auth (`# TODO enable auth`); chart 0.1.52, latest 0.1.128 (zot v2.1.22).
- **Test plumbing.** The identity tests dial the ingress LB IP directly because the localhost
  socat proxy stalls about 1% of connects. Credentials and tokens are kept out of test logs.

## Decisions

| Topic | Decision |
|---|---|
| Group format | Normalised in Kanidm: the Dex client's `claimMap` emits `homelab_groups` with short names; the Dex connector maps it to `groups`. Same values in sandbox and production |
| Grafana roles | admins → Admin + Grafana server admin, editors → Editor, users → Viewer; no group → refused (`role_attribute_strict`) |
| Grafana local admin | Kept, password from global-secrets `grafana.admin`; login form stays next to "Sign in with Dex" |
| ArgoCD roles | admins → `role:admin`; editors → `role:editor` (get everything, sync, actions, logs, exec; no create/update/delete); users → `role:readonly`; others → nothing |
| ArgoCD local admin | Kept |
| zot auth | OIDC for the UI, per-person API keys for `docker`/`crane`, htpasswd machine account `ci` |
| zot permissions | admins read/create/update/delete, editors read/create/update, users read, `ci` read/create/update, anonymous none |
| Dev CA trust in Go apps | Mount the app's own ingress certificate `ca.crt` into `/etc/homelab-dev-ca` and set `SSL_CERT_DIR=/etc/ssl/certs:/etc/homelab-dev-ca` (dev only) |
| Versions | Grafana chart 13.5.0 from `https://grafana-community.github.io/helm-charts`, zot chart 0.1.128; re-check and pin exactly while planning |

## Design

### 1. Shared groundwork

- **Normalised groups:**
  - `platform/dex/templates/kanidm-client.yaml` adds a `claimMap` named `homelab_groups`. Each
    of the three groups maps to its own short name, with `joinStrategy: array`.
  - The Dex Kanidm connector (both values files) adds `claimMapping.groups: homelab_groups` and
    `overrideClaimMapping: true`.
  - The `groups` claim every app receives becomes exactly `["homelab-<x>"]`.
  - `TestIdentityLogin` asserts equality with that list instead of a prefix match.
- **Generated secrets:** `platform/global-secrets/values.yaml` adds:
  - `dex.argocd` and `dex.zot` (`client_secret`)
  - `grafana.admin` (`password`)
  - `registry.ci` (`password`)
- **Dex static clients:** add `argocd` (redirect `https://<argocd host>/auth/callback`) and `zot`
  (redirect `https://<registry host>/zot/auth/callback/oidc`) next to `grafana-sso`, in both
  values files (lists are replaced). `dex-secrets` gains `ARGOCD_CLIENT_SECRET` and
  `ZOT_CLIENT_SECRET`.
- **Dev CA trust:** each Go app mounts the `ca.crt` of its own ingress certificate and sets
  `SSL_CERT_DIR` in its `values-dev.yaml` only.
- **Allowlist:** `system/argocd/values-dev.yaml` adds `platform/grafana` and `platform/zot`.
- **Test helper.** `browserAuthorize` generalises to an app login helper:
  1. open the app's login page and click its SSO button
  2. follow Dex → Kanidm (TOTP, then password)
  3. return to the app and hand back the browser's cookies for API calls

  It reuses the stored credentials, `totpClock`, the direct-to-LB dialling and the quiet
  logging from 1b.

### 2. Grafana (`platform/grafana`)

- **Chart:** dependency `grafana` 13.5.0 from the `grafana-community` repo.
- **`grafana.ini` → `auth.generic_oauth`:**
  - keep the Dex client `grafana-sso` with `scopes: openid profile email groups`
  - `role_attribute_path: contains(groups[*], 'homelab-admins') && 'Admin' || contains(groups[*], 'homelab-editors') && 'Editor' || contains(groups[*], 'homelab-users') && 'Viewer'`
  - `role_attribute_strict: true`
  - `allow_assign_grafana_admin: true` (admins also get Grafana server admin)
- **Local admin:**
  - `admin.existingSecret` points at a Secret built by ExternalSecret from `grafana.admin`
  - the login form stays enabled
- **Data:** datasources and dashboards are unchanged; sidecars read namespace
  `monitoring-system`.
- **`values-dev.yaml`:**
  - host `grafana.127-0-0-1.nip.io`, with `root_url`, `auth_url`, `token_url` and `api_url` on
    the sandbox domains
  - dev CA mount and `SSL_CERT_DIR`

### 3. ArgoCD (`system/argocd`)

- **`configs.cm`:**
  - `oidc.config` with issuer Dex, `clientID: argocd`,
    `clientSecret: $argocd-oidc:clientSecret` and
    `requestedScopes: [openid, profile, email, groups]`
  - `exec.enabled: "true"`, needed for the editor role. This also takes effect in production
    (admins can exec too).
- **Secret `argocd-oidc`:** an ExternalSecret in namespace `argocd` from `dex.argocd`, with label
  `app.kubernetes.io/part-of: argocd` so ArgoCD reads it.
- **`configs.rbac`:**
  - `policy.default: ''`
  - `scopes: '[groups]'`
  - `policy.csv`:

```csv
p, role:editor, applications, get, */*, allow
p, role:editor, applications, sync, */*, allow
p, role:editor, applications, action/*, */*, allow
p, role:editor, logs, get, */*, allow
p, role:editor, exec, create, */*, allow
p, role:editor, projects, get, *, allow
p, role:editor, clusters, get, *, allow
p, role:editor, repositories, get, *, allow
g, homelab-admins, role:admin
g, homelab-editors, role:editor
g, homelab-users, role:readonly
```

- **`values-dev.yaml`:**
  - restate `oidc.config` with the sandbox Dex issuer (it is one string)
  - dev CA mount and `SSL_CERT_DIR` on `argocd-server`
- **Unchanged:** the local `admin` account, and `dex.enabled: false` (the platform Dex is used).

### 4. zot (`platform/zot`)

- **Chart:** 0.1.128 (zot v2.1.22), with the `search` and `ui` extensions enabled.
- **`http` config:**
  - `externalUrl` set to the registry host
  - `auth.openid.providers.oidc`:
    - issuer Dex, client `zot`
    - scopes `openid profile email groups`
    - `claimMapping.username: preferred_username`; groups come from `groups`
    - the client credentials file is mounted from an ExternalSecret built from `dex.zot`
  - `auth.apikey: true`
  - `auth.htpasswd`: user `ci`; the file is rendered by an ExternalSecret template using
    `htpasswd` (bcrypt) from `registry.ci`
  - `registry-admin-secret` stays as it is
- **`accessControl`:**
  - repositories `**` with the group and user policies in the table above
  - `defaultPolicy: []` and no anonymous policy
- **`values-dev.yaml`:** host `registry.127-0-0-1.nip.io`, sandbox Dex issuer, dev CA mount and
  `SSL_CERT_DIR`.

### 5. Tests, workflow, docs

- **`make dev`** runs the smoke test with filter `Smoke|Identity|SSO`. Each `TestSSO*` calls
  `waitForIdentityStack` and waits for its app's ingress before logging in.
- **`TestSSOGrafana`:**
  - for `dev-admin`, `dev-editor` and `dev-viewer`, log in through "Sign in with Dex"
  - `/api/user` must return `orgRole` Admin, Editor and Viewer respectively, and
    `isGrafanaAdmin` must be true only for `dev-admin`
  - the local `admin` password logs in through `/api/user` basic auth
- **`TestSSOArgoCD`:**
  - log in via Dex and use the `argocd.token` cookie with `/api/v1/account/can-i`
  - admin: sync, delete and exec are `yes`
  - editor: sync and exec `yes`, delete `no`
  - viewer: all three `no`
  - the local `admin` still gets a session from `/api/v1/session`
- **`TestSSOZot`:**
  - log in to the UI via OIDC and create an API key through `/zot/auth/apikey`
  - with basic auth `<user>:<key>`, `POST /v2/sso-test/blobs/uploads/` returns 202 for admin
    and editor and 403 for viewer; `GET /v2/_catalog` returns 200 for all
  - `ci` with its htpasswd password gets 202
  - a request without credentials gets 401
- **Smoke tests:** in dev, `TestSmoke` adds the `grafana` and `zot` ingresses.
- **Docs:**
  - `docs/installation/sandbox.md`: Grafana and zot links, the group → permission table per
    app, how to create a zot API key and `docker login`, and an updated RAM table
  - `docs/getting-started/user-onboarding.md`: what each group gets in each app
  - `CLAUDE.md`:
    - the `homelab_groups` convention and the `SSL_CERT_DIR` dev-CA pattern
    - roadmap: 1c done
    - production checklist: ArgoCD `exec.enabled` is on, and the Grafana chart moved repos

## Acceptance criteria (on branch `dev`)

1. `make dev` succeeds from scratch, including the smoke, identity and all SSO tests, and
   succeeds again when run a second time.
2. All 11 allowlisted Applications are Synced and Healthy.
3. The `groups` claim is exactly `["homelab-<x>"]` for each test account.
4. Grafana roles match the groups, and the local admin logs in.
5. ArgoCD permissions match the table in section 5, and the local admin logs in.
6. zot: admin and editor push, viewer pulls only, `ci` pushes, and anonymous requests get 401.
7. Production and dev renders pass for every changed chart, and Prometheus has no down
   targets.
8. The sandbox RAM table includes Grafana and zot.

## Risks

- **zot identity and chart layout.** The username zot uses after claim mapping (needed for
  API-key basic auth), and how the 0.1.128 chart mounts `config.json`, `htpasswd` and the
  credentials file, are verified first while planning.
- **Grafana 13 and chart 13.5.0** may rename `grafana.ini` keys or chart values. Render prod
  and dev, and fix any differences in `values.yaml`.
- **Kanidm `claimMap` join strategy.** The claim must arrive as a JSON array for Dex's
  `claimMapping`; the identity test asserts the exact list.
- **Exec in production** becomes possible for admins and editors; noted in the production
  checklist.

## Out of scope

- SSO for Gitea, Woodpecker, Renovate and the apps in `apps/`
- A CI pipeline using `ci`
- Automatic secret rotation
- The socat proxy stall from 1a
