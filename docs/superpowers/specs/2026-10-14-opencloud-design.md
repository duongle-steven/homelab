# Step 5c: OpenCloud with Kanidm OIDC

Status: design approved in chat 2026-10-14, written spec awaiting review. Branch: `feat/opencloud` (PR into `dev`).

## Goal

OpenCloud (file storage, `drive.<domain>`) runs as `apps/opencloud.yaml` and logs in with Kanidm directly
through its own client (the step 5a pattern). Only members of the access group `homelab-files` may use it;
members of `homelab-admins` get OpenCloud's Admin role, everyone else the User role. The sandbox runs it and
its tests prove login, the role mapping, the refusal of accounts outside `homelab-files`, and that files are
stored and read back.

## Context

- Upstream `nixos` (`52c32ab`) has `apps/opencloud.yaml`: app-template, one pod running every OpenCloud
  service from the single `opencloud` binary, image `docker.io/opencloudeu/opencloud-rolling:8.1.0`, an init
  container that runs `opencloud init` when `/etc/opencloud/opencloud.yaml` is missing, PVCs `config` (1Gi)
  and `data` (20Gi), a CSP file allowing `connect-src https://auth.<domain>/`, the built-in IdP disabled, and
  accounts provisioned from the OIDC claim `sub`. Its Kanidm client is public (`opencloud`, redirects `/`,
  `/oidc-callback.html`, `/oidc-silent-redirect.html`), created by its Makefile. It has no role mapping and
  routes through `istio-system/gateway` (Istio is step 6 here; this repo uses the Cilium Gateways
  `gateway/homelab` and `gateway/homelab-public`).
- OpenCloud has two public release channels: Rolling (`opencloudeu/opencloud-rolling`, latest 8.1.0, a
  full release; OpenCloud's docs call it "ideal for most users") and Production (`opencloudeu/opencloud`,
  latest 7.2.4, focused on stability). LTS needs an enterprise license.
- The web UI is a single-page app: the browser runs the OIDC code flow with PKCE and sends the access token
  to OpenCloud's proxy, which verifies it. There is nowhere to keep a client secret, so the client must be
  public; Kanidm cannot disable the consent prompt on public clients.
- Role assignment: `PROXY_ROLE_ASSIGNMENT_DRIVER=oidc` with `PROXY_ROLE_ASSIGNMENT_OIDC_CLAIM` picks the claim;
  the claim-value-to-role map exists only in the proxy config file
  (`role_assignment.oidc_role_mapper.role_mapping`, a list of `role_name`/`claim_value`; the first matching
  entry wins, unmapped values are ignored). OpenCloud requires the claim in the access token or the userinfo
  endpoint, independent of the requested scopes. Whether Kanidm's `claimMap` claims reach either is unproven.
- `test/identity_helpers_test.go` already accepts Kanidm's consent page in `appLogin` (zot is public) and has
  `browserDenied` for accounts Kanidm refuses.

## Decisions

| Topic | Decision |
|---|---|
| Packaging | `apps/opencloud.yaml` (app-template), adapted from upstream; not the official microservice chart |
| Image | `docker.io/opencloudeu/opencloud-rolling:8.1.0` (latest release, OpenCloud's recommended channel) |
| Client | Public `KanidmOAuth2Client` `opencloud` in `rawResources`, like upstream; consent shown once per person |
| Access | New access group `homelab-files` through `scopeMap`; `homelab-admins` → Admin, other members → User |
| Gateway | `gateway/homelab` (LAN), host `drive.<domain>` |
| Tests | Login, role, refusal, and a WebDAV upload/download with the browser session's token |
| Out of scope | Office editing (Collabora/WOPI), desktop and mobile clients, backups (step 10) |

## Design

### `apps/opencloud.yaml`

Upstream's file with these changes:

- Route `main`: `parentRefs` `gateway/homelab`, section `https`; host `drive.{{ .Values.global.domain }}`.
- Main container env, added to upstream's:
  - `PROXY_ROLE_ASSIGNMENT_DRIVER: oidc`, `PROXY_ROLE_ASSIGNMENT_OIDC_CLAIM: homelab_groups`
  - `SSL_CERT_DIR: '{{ if .Values.global.devCA }}/etc/ssl/certs:/etc/homelab-dev-ca{{ end }}'` (OpenCloud is
    Go; empty in production, where Go uses its default)
- ConfigMaps, each mounted with `subPath` into `/etc/opencloud/` over the `config` PVC:
  - `csp.yaml` as upstream
  - `proxy.yaml`:

    ```yaml
    role_assignment:
      driver: oidc
      oidc_role_mapper:
        role_claim: homelab_groups
        role_mapping:
          - role_name: admin
            claim_value: homelab-admins
          - role_name: user
            claim_value: homelab-files
    ```

- Persistence: `config` (1Gi) and `data` (20Gi) keep `suffix`, so the PVCs are `opencloud-config` and
  `opencloud-data`; a `dev-ca` volume from ConfigMap `homelab-dev-ca`, optional, mounted at
  `/etc/homelab-dev-ca` (as in `apps/paperless.yaml`). The sandbox uses the same sizes (local-path does not
  enforce them).
- Resources and probes as upstream (request 512Mi, limit 1536Mi; startup on TCP 9200, readiness `/readyz`
  on 9205).
- `rawResources.kanidm-client` (`forceRename: opencloud`):
  - `public: true`, `displayname: OpenCloud`, `origin: https://drive.<domain>`
  - `redirectUrl`: `https://drive.<domain>/`, `/oidc-callback.html`, `/oidc-silent-redirect.html`
  - `strictRedirectUrl: true`, `preferShortUsername: true`
  - `claimMap` `homelab_groups` (`joinStrategy: array`): `homelab-admins` → `homelab-admins`,
    `homelab-files` → `homelab-files`
  - `scopeMap`: only `homelab-files` → `[openid, profile, email, groups]`

### Wiring

- `platform/kanidm`: `homelab-files` joins the group list in `templates/groups.yaml`; `opencloud` joins
  `oauth2ClientNamespaces` in `values.yaml` and `values-dev.yaml`; the sandbox sets
  `homelab-files: [dev-admin, dev-editor]` in `values-dev.yaml`.
- `scripts/onboard-user`: access group `files` (`[budget] [documents] [files]`).
- `system/argocd/values-dev.yaml`: `apps/opencloud.yaml` in the `apps` allowlist.
- `system/argocd/values.yaml`: `p, role:editor, exec, create, default/opencloud, allow`.
- `apps/homepage.yaml`: an OpenCloud entry (`https://drive.<domain>`).

### Login flow

The browser opens `drive.<domain>`, goes to Kanidm, accepts the consent page once, and returns to
`/oidc-callback.html`. The web UI sends the access token to the proxy, which verifies it, provisions the
account in OpenCloud's internal LDAP on first login (`sub`), and sets the role from `homelab_groups` at each
login, so leaving `homelab-admins` demotes someone to User on their next login. People outside
`homelab-files` get no token from Kanidm ("Access Denied").

## Error handling

- `homelab_groups` not readable by OpenCloud (the open risk): the first plan task proves it against the
  sandbox before anything else is built on it. If it fails, stop and ask; the fallback is the `default`
  driver (everyone User, admins assigned by hand).
- `opencloud-config` holds the secrets `opencloud init` generated; losing the PVC loses them (and the
  internal LDAP's users and shares). Backups come in step 10.
- Kanidm unreachable: new logins fail. Whether the pod still starts is not known yet; the plan checks it
  in the sandbox and `CLAUDE.md` records the result (as for Forgejo and zot).

## Testing

Offline basic checks on the branch: `scripts/render-chart apps/opencloud.yaml` and `... dev`,
`scripts/render-chart platform/kanidm` and `... dev`, `go vet` and the offline test regex,
`mkdocs build --strict`. After the merge, `make dev`.

Sandbox tests:

- `apps_test.go`, `smoke_test.go`: `{"opencloud", "opencloud"}`.
- `sso_test.go`, `TestSSOOpenCloud` (host `drive.127-0-0-1.nip.io`):
  - `dev-admin` (Admin) and `dev-editor` (User): a helper `openCloudLogin` opens the web UI, logs in through
    Kanidm (accepting the consent page) and captures the `Authorization: Bearer` header the web UI sends to
    `/graph`. With it: `GET /graph/v1.0/me?$expand=appRoleAssignments` (the display name matches the Kanidm
    account; the username is the Kanidm `sub` UUID, as upstream; the role name is resolved through
    `/graph/v1.0/applications`); the personal drive's `webDavUrl` from `/graph/v1.0/me/drives`; PUT a
    file with random content, GET it back and compare, DELETE it.
  - `dev-viewer` and `dev-outsider`: `browserDenied` returns "Access Denied".
- The `make dev` filter already matches (`SSO`).

## Docs

- `docs/installation/sandbox.md`: the `drive` link; a `homelab-files` column in the test account table;
  "OpenCloud admits only `homelab-files` (members of `homelab-admins` become Admin)"; measured RAM with an
  `opencloud` row.
- `docs/getting-started/user-onboarding.md`: the `files` access group.
- README and `docs/index.md` app lists (where apps are listed).
- `CLAUDE.md`: 5c done; `homelab-files`, the public client and the `proxy.yaml` role mapping in the identity
  note; under "Before the first production deploy": OpenCloud only ran in the sandbox, `opencloud-config`
  holds generated secrets (backups in step 10), desktop and mobile clients need their own Kanidm clients.
