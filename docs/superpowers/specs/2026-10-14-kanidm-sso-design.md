# Step 5a: SSO directly with Kanidm (drop Dex)

Status: design approved in chat 2026-10-14, written spec awaiting review. Branch: `feat/kanidm-sso` (PR into `dev`).

## Goal

Every app logs in directly against Kanidm with its own OAuth2 client declared in git; Dex is removed.
Grafana, Argo CD and zot keep their role mapping from the `homelab-admins/editors/users` groups.
Actual Budget and paperless get SSO, each limited to its own access group. The sandbox proves every case.

Step 5 is split: **5a** (this spec), **5b** Forgejo replaces Gitea, **5c** opencloud. 5b and 5c reuse the
client pattern defined here.

## Context

- Today: one `KanidmOAuth2Client` `dex` (in `platform/dex`) with a `claimMap` `homelab_groups` (short
  group names); Dex maps it to `groups` and serves Grafana, Argo CD, zot and the sandbox-only test client
  `smoke`. Client secrets come from `global-secrets` (`dex.grafana`, `dex.argocd`, `dex.zot`, `dex.smoke`).
- Kanidm's own `groups` claim carries SPNs (`homelab-admins@auth.<domain>`) and UUIDs; the custom claim
  `homelab_groups` gives short names. Kanidm requires PKCE (S256) for every client unless disabled.
- kaniop 0.19 `KanidmOAuth2Client`: `public`, `scopeMap`, `claimMap`, `preferShortUsername`,
  `disableConsentPrompt`, `strictRedirectUrl`, `jwtLegacyCryptoEnable` (RS256), `secretTemplate`,
  `secretKeyAliases`. Confidential clients get a Secret `<name>-kanidm-oauth2-credentials`
  (`CLIENT_ID`, `CLIENT_SECRET`) in their namespace. The `Kanidm` CR's `oauth2ClientNamespaceSelector`
  limits where clients may live (today: namespace `dex`).
- App capabilities (checked in source or docs):
  - Grafana `generic_oauth`: JMESPath role mapping on any claim, `use_pkce`.
  - Argo CD: OIDC with `enablePKCEAuthentication`; RBAC `scopes` selects the group claim.
  - zot v2.1.22: `claimMapping.groups` and `claimMapping.username`; without a client secret it uses PKCE
    (public client).
  - Actual Budget: `ACTUAL_OPENID_*` env, `ACTUAL_OPENID_ENFORCE`, `ACTUAL_USER_CREATION_MODE`; no group
    roles; the first OpenID user becomes the server owner; upstream notes it needs RS256.
  - paperless 3.3: allauth OpenID Connect, `PAPERLESS_SOCIAL_ACCOUNT_SYNC_GROUPS_CLAIM`,
    `PAPERLESS_SOCIAL_ACCOUNT_SYNC_SUPERUSER_GROUP`.
- Upstream `nixos` creates one client and one access group `<client>_users` per app imperatively
  (`toolbox sso client ensure`), with no role mapping; zot is a public client without API keys or htpasswd.

## Decisions

| Topic | Decision |
|---|---|
| Scope | 5a: drop Dex; Grafana, Argo CD, zot move to Kanidm; SSO for Actual Budget and paperless |
| Clients | One `KanidmOAuth2Client` per app, declared with kaniop in the app's chart or app file, in the app's namespace |
| Admin tools | Grafana, Argo CD, zot: `scopeMap` for the three role groups, `claimMap` `homelab_groups`, roles as today |
| Personal-data apps | New groups `homelab-budget` and `homelab-documents` grant access (`scopeMap`); paperless also maps `homelab-admins` to superuser |
| zot | Public client with PKCE; per-person API keys and the htpasswd `ci` account stay |
| Actual Budget | RS256 (`jwtLegacyCryptoEnable`), OpenID enforced, users created on first login |
| Client secrets | The kaniop-generated Secret, read directly by the app; `dex.*` entries leave `global-secrets` |
| paperless in the sandbox | Yes (tests its SSO and the v3 image) |

## Design

### 1. Kanidm (`platform/kanidm`)

- Groups: `homelab-admins`, `homelab-editors`, `homelab-users`, `homelab-budget`, `homelab-documents`.
  Production groups set no `members` (people are added with `scripts/onboard-user`). Sandbox members:
  - `homelab-budget`: `dev-admin`, `dev-viewer`
  - `homelab-documents`: `dev-admin`, `dev-editor`
  - role groups unchanged; `dev-outsider` stays in no group.
- `oauth2ClientNamespaceSelector` admits the namespaces of the SSO apps, through a namespace label set
  by the ApplicationSets (`managedNamespaceMetadata`); the plan fixes the exact mechanism.
- Sandbox-only client `smoke` (redirect `https://smoke.127-0-0-1.nip.io/callback`, `claimMap`
  `homelab_groups`, `scopeMap` for the three role groups) moves from Dex to Kanidm, in `platform/kanidm`
  dev values.
- Common client settings: `disableConsentPrompt: true`, `preferShortUsername: true`,
  `strictRedirectUrl: true`.

### 2. Apps

Issuer per app: `https://auth.<domain>/oauth2/openid/<client>`.

| App | Client | App settings |
|---|---|---|
| Grafana | confidential, role groups, `homelab_groups` | `generic_oauth` with Kanidm's `/ui/oauth2`, `/oauth2/token`, `/oauth2/openid/grafana/userinfo`; `use_pkce: true`; `role_attribute_path` on `homelab_groups`; client secret from the kaniop Secret; local admin kept |
| Argo CD | confidential, role groups, `homelab_groups`; `secretTemplate` label `app.kubernetes.io/part-of: argocd` | `oidc.config` issuer, `enablePKCEAuthentication: true`, `clientSecret: $argocd-kanidm-oauth2-credentials:CLIENT_SECRET`; RBAC `scopes: '[homelab_groups]'`; local admin kept |
| zot | public, role groups, `homelab_groups` | openid provider without `clientsecret`, `claimMapping.groups: homelab_groups`, `claimMapping.username: preferred_username`; API keys, htpasswd `ci` and access control unchanged |
| Actual Budget | confidential, `homelab-budget`, RS256 | `ACTUAL_OPENID_DISCOVERY_URL`, `ACTUAL_OPENID_CLIENT_ID`/`_SECRET`, `ACTUAL_OPENID_SERVER_HOSTNAME`, `ACTUAL_OPENID_ENFORCE=true`, `ACTUAL_USER_CREATION_MODE=login` |
| paperless | confidential, `homelab-documents`, `homelab_groups` | allauth OpenID Connect with PKCE in `PAPERLESS_SOCIALACCOUNT_PROVIDERS` (client secret through `$(CLIENT_SECRET)` env expansion), `PAPERLESS_SOCIAL_AUTO_SIGNUP=true`, `SYNC_GROUPS`, `SYNC_GROUPS_CLAIM=homelab_groups`, `SYNC_SUPERUSER_GROUP=homelab-admins`; local admin kept |

- Sandbox CA: Actual Budget (`NODE_EXTRA_CA_CERTS`) and paperless (`REQUESTS_CA_BUNDLE`/`SSL_CERT_FILE`)
  trust the ConfigMap `homelab-dev-ca` in the sandbox only; the plan picks the mechanism that works with
  one app file per app (a `global.*` parameter or an optional mount).
- Remove `platform/dex`, its `global-secrets` entries and every Dex reference.

### 3. Tests, rollout, docs

- **Identity tests:** `browserAuthorize` uses Kanidm's authorize endpoint with the `smoke` client and PKCE
  (`code_verifier`/`code_challenge` S256); `TestIdentityLogin` checks `homelab_groups`, `email`,
  `preferred_username`; `TestIdentityDenied` expects Kanidm's "Access Denied".
- **SSO tests:** Grafana, Argo CD and zot keep their role checks (the browser goes straight to Kanidm).
  New `TestSSOActualBudget` (`dev-admin`, `dev-viewer` get in; `dev-editor` is refused) and
  `TestSSOPaperless` (`dev-admin` is superuser, `dev-editor` a regular user, `dev-viewer` refused).
- **Smoke:** add `paperless`, drop `dex`. Sandbox allowlists add `apps/paperless.yaml`, drop
  `platform/dex`.
- **Rollout:** one PR into `dev`; basic checks on the branch; after the merge `make dev` on the running
  sandbox, then a fresh install, then measure memory (report if above about 8 GiB).
- **Docs:** `docs/getting-started/user-onboarding.md` (groups, how to grant budget/documents access),
  `docs/installation/sandbox.md` (test accounts and their groups), `CLAUDE.md` (drop the Dex notes,
  describe per-app Kanidm clients and access groups; production checklist: the first Actual Budget OpenID
  user becomes the owner, so add yourself to `homelab-budget` and log in first).

## Acceptance criteria (on branch `dev`)

1. No Dex component, client, secret or reference remains.
2. `make dev` passes on the upgraded sandbox and on a fresh one, including the new SSO tests.
3. Grafana, Argo CD and zot roles behave as before; Actual Budget and paperless admit only their groups.
4. Sandbox memory is measured and reported.

## Risks

- **Clients in app namespaces:** kaniop must be allowed to watch them; a wrong selector leaves clients
  unreconciled. The SSO tests catch it.
- **RS256 for Actual Budget** is needed per upstream; if Actual Budget accepts ES256 by now, the flag is
  harmless.
- **Actual Budget owner:** in production the first OpenID login wins ownership.
- **paperless memory** in the sandbox.

## Out of scope

- Forgejo (5b), opencloud (5c).
- Role mapping inside Actual Budget; paperless permissions beyond superuser/regular.
- Moving client creation or secrets to OpenBao (step 8).
