# Step 5b: Forgejo replaces Gitea

Status: design approved in chat 2026-10-14, written spec awaiting review. Branch: `feat/forgejo` (PR into `dev`).

## Goal

Forgejo replaces Gitea and is the source of truth for the GitOps repo: the first install seeds it from
GitHub, then production Argo CD syncs from Forgejo. People log in with Kanidm through a per-app client
(the step 5a pattern); `homelab-admins` are Forgejo site admins. Production stores Forgejo's data in a
PostgreSQL cluster run by CloudNativePG; the sandbox uses SQLite and proves login, admin mapping, SSH
and the seed.

Nothing called Gitea is kept: every name this repo controls becomes `forgejo`. The deployment starts from
scratch; there is no data migration.

## Context

- `platform/gitea`: Gitea chart 12.7.0 with Bitnami `postgresql-ha` and `valkey-cluster` in production
  (one Gitea pod), SQLite in the sandbox; a `TCPRoute` for SSH; a config job (unfinished Go program that
  runs `go get` at every start) that migrates upstream's repositories. The config job is disabled in the
  sandbox, so the sandbox Gitea is empty and the seed-then-switch path has only ever been rendered.
- Production Argo CD syncs `http://gitea-http.gitea:3000/ops/homelab`; `values-seed.yaml` syncs GitHub
  on the first install; `system/bootstrap.yml` picks between them by looking for a running Gitea pod.
- Forgejo chart 17.3.0 (`oci://code.forgejo.org/forgejo-helm/forgejo`) defaults to Forgejo 15.0.10 (LTS);
  the latest stable Forgejo is 16.0.6. The chart bundles no database or cache.
- The chart and image keep Gitea's names for their own interface; they cannot change without forking
  the chart or the image (Helm's dependency `alias` only renames the outer key `forgejo:`):
  - chart: the values key `gitea:` (`forgejo.gitea.config`, `forgejo.gitea.oauth`,
    `forgejo.gitea.admin` in the wrapper), the init container `configure-gitea`, the template names
    `gitea.*`, the env variables `GITEA_*` (including `GITEA__<section>__<key>`), `/data/gitea/conf/app.ini`
  - image: `/var/lib/gitea`, `/etc/gitea`
- The chart's `gitea.oauth[]` entries become flags of `forgejo admin auth add-oauth`/`update-oauth`,
  run by `configure-gitea` at every pod start; `existingSecret` must have the keys `key` and `secret`,
  and `deployment.env` reaches the init container too.
- Forgejo 16.0.6 sends PKCE (S256) to `openidConnect` login sources (`routers/web/auth/oauth.go`,
  `generateCodeChallenge`). If discovery fails when the provider is registered, the next login hits
  "no provider", calls `ResetOAuth2` and registers it again.
- Upstream `nixos` runs Forgejo with SQLite, a confidential Kanidm client, no SSH, and seeds repositories
  with its `toolbox` (which reads secrets from OpenBao, not available until step 8).
- Bitnami no longer publishes free images, so `postgresql-ha` is not an option.

## Decisions

| Topic | Decision |
|---|---|
| Packaging | Wrapper chart `platform/forgejo` (like `platform/zot`), namespace `forgejo`, `fullnameOverride: forgejo` |
| Versions | Forgejo chart 17.3.0, image `code.forgejo.org/forgejo/forgejo:16.0.6` (overrides the chart's LTS default); CloudNativePG chart 0.29.1 (operator 1.30.1); PostgreSQL `ghcr.io/cloudnative-pg/postgresql:18.6-standard-trixie` |
| Database | Production: CloudNativePG `Cluster` `forgejo-db`, 3 instances, in namespace `forgejo`. Sandbox: SQLite |
| High availability | Database only: Forgejo stays one pod (`Recreate`), cache and session in memory |
| Login | Kanidm, confidential client `forgejo` with PKCE; the three role groups may log in; `homelab-admins` become site admins |
| Local admin | `forgejo_admin` from `global-secrets` `forgejo.admin`, kept as break-glass access |
| SSH | Kept: Service `forgejo-ssh`, `TCPRoute` on the `ssh` listener of `gateway/homelab` |
| Source of truth | Forgejo from step 5b: a seed Job copies the GitHub repo once into `ops/homelab`; production Argo CD syncs from Forgejo |
| Naming | Every name in this repo becomes `forgejo`; only chart-imposed names and history (past specs and plans, changelog, decision records) keep `gitea` |
| Left for step 9 | Push mirror to GitHub, Woodpecker and Renovate actually running, Forgejo credentials in OpenBao |
| Left for step 10 | Backups of the CloudNativePG cluster and the Forgejo volume |

## Design

### `platform/cloudnative-pg` (production only)

- Wrapper chart depending on `cloudnative-pg` 0.29.1 from `https://cloudnative-pg.github.io/charts`,
  namespace `cnpg-system`, CRDs installed by the chart, monitoring with the Prometheus Operator
  (`PodMonitor`).
- Not in the sandbox allowlist: the sandbox runs no PostgreSQL. Production picks it up through the
  `platform/*` generator.

### `platform/forgejo`

`Chart.yaml` depends on `forgejo` 17.3.0. Templates:

- `admin-secret.yaml`: ExternalSecret `forgejo-admin` (`username: forgejo_admin`, `password` from
  `forgejo.admin`); the chart uses it with `passwordMode: keepUpdated`.
- `kanidm-client.yaml`: `KanidmOAuth2Client` `forgejo`:
  - `origin: https://git.<domain>`, `redirectUrl: https://git.<domain>/user/oauth2/Kanidm/callback`,
    `strictRedirectUrl`, `preferShortUsername`, `disableConsentPrompt` (confidential client)
  - `scopeMap` for `homelab-admins`, `homelab-editors`, `homelab-users` with
    `openid profile email groups`
  - `claimMap` `homelab_groups` with short names, as zot
- `tcproute.yaml`: `TCPRoute` `forgejo-ssh` (`gateway.networking.k8s.io/v1`, like the Gitea one) to
  Service `forgejo-ssh` port 22, parent `gateway/homelab` section `ssh`.
- `database.yaml` (when `database.cnpg.enabled`): CloudNativePG `Cluster` `forgejo-db`, 3 instances,
  image pinned as above, bootstrap `initdb` with database and owner `forgejo`, storage on the default
  Ceph block storage class. CloudNativePG generates the Secret `forgejo-db-app`.
- `seed-job.yaml` and its script (see below).

Values (`values.yaml`, production). The dependency's values sit under `forgejo:`; `forgejo.gitea:` is
the Forgejo chart's own key for the app's settings, and the file says so in a comment where it starts:

- `forgejo.image`: registry `code.forgejo.org`, repository `forgejo/forgejo`, tag `16.0.6`; rootless.
- `forgejo.gitea.config`:
  - `server`: `ROOT_URL https://git.<domain>`, `SSH_DOMAIN git.<domain>`, `LANDING_PAGE explore`,
    `OFFLINE_MODE true`
  - `service`: `DISABLE_REGISTRATION true`, `ALLOW_ONLY_EXTERNAL_REGISTRATION true`
  - `oauth2_client`: `ENABLE_AUTO_REGISTRATION true`, `USERNAME preferred_username`,
    `ACCOUNT_LINKING disabled`
  - `repository`, `service.explore`, `webhook`: as Gitea today; `actions.ENABLED false`
  - `database`: `DB_TYPE postgres`, `HOST forgejo-db-rw:5432`, `NAME forgejo`, `USER forgejo`; the
    password comes from `forgejo-db-app` through `forgejo.gitea.additionalConfigFromEnvs`
    (`GITEA__DATABASE__PASSWD`)
  - `cache` `memory`, `session` `memory`, `queue` `level`, `indexer` default (bleve on the volume)
- `forgejo.gitea.oauth`: one entry `Kanidm`, provider `openidConnect`, `key: forgejo`,
  `secret: ${CLIENT_SECRET}` (expanded by the init script's shell),
  `autoDiscoverUrl https://auth.<domain>/oauth2/openid/forgejo/.well-known/openid-configuration`,
  `scopes openid profile email groups`, `groupClaimName homelab_groups`, `adminGroup homelab-admins`.
- `forgejo.deployment.env`: `CLIENT_SECRET` from `forgejo-kanidm-oauth2-credentials`.
- `forgejo.strategy: Recreate`, `forgejo.persistence` claim `forgejo-data`.
- `forgejo.httpRoute` on `gateway/homelab` section `https`, host `git.<domain>`.
- `forgejo.service.ssh` port 22.
- Wrapper keys: `database.cnpg.enabled: true`; `seed.repository: https://github.com/khuedoan/homelab` (replaced by
  `make configure`).

`values-dev.yaml` (sandbox):

- host `git.127-0-0-1.nip.io`, `ROOT_URL`, `SSH_DOMAIN`
- `database`: `DB_TYPE sqlite3`; `database.cnpg.enabled: false`
- the optional `homelab-dev-ca` volume and `SSL_CERT_DIR`, as Grafana and zot
- `seed.repository: https://github.com/duongle-steven/homelab`

A comment marks each chart-imposed `gitea` name where it appears.

### Seed Job

- `forgejo-seed-<hash of the script>`, Argo CD sync wave after the Deployment, `backoffLimit: 10`,
  image Forgejo 16.0.6 (already pulled; `curl` is checked in the plan, with a pinned curl image as the
  fallback).
- Shell script, authenticated as `forgejo_admin` from `forgejo-admin`, against
  `http://forgejo-http:3000`:
  1. Wait until `/api/healthz` answers.
  2. `GET /api/v1/orgs/ops`; on 404 `POST /api/v1/orgs` (`username: ops`).
  3. `GET /api/v1/repos/ops/homelab`; on 404 `POST /api/v1/repos/migrate` with
     `clone_addr: <seed.repository>`, `repo_owner: ops`, `repo_name: homelab`, `service: git`,
     `mirror: false`, `private: true`.
  4. Any other status fails the Job.
- Never overwrites an existing org or repository: after the seed, Forgejo is the source of truth.
- The seed repository is public, so no credential is needed; a private GitHub repo would need one from
  step 9.

### Argo CD source switch (production)

- `system/argocd/values.yaml`: the `root` and `apps` generators and sources use
  `http://forgejo-http.forgejo:3000/ops/homelab`.
- Argo CD reads the private repository with a repository credential: ExternalSecret
  `argocd-repo-forgejo` in `argocd` (label `argocd.argoproj.io/secret-type: repository`, `url` of the
  repository, `username forgejo_admin`, `password` from `forgejo.admin`). Step 9 replaces it with a
  read-only token in OpenBao.
- `values-seed.yaml` keeps syncing GitHub for the first install.
- `system/bootstrap.yml` detects a running pod with `app.kubernetes.io/instance=forgejo`.
- The sandbox keeps syncing GitHub branch `dev`, so the switch itself is only rendered.

### Renames and removals

- Delete `platform/gitea` (config job and `files/config` included).
- `global-secrets`: `gitea.*` become `forgejo.admin`, `forgejo.woodpecker`, `forgejo.renovate`.
- `platform/kanidm/values.yaml` (and `values-dev.yaml`): `forgejo` in `oauth2ClientNamespaces`.
- `system/gateway/templates/gateway.yaml`: the `ssh` listener admits namespace `forgejo`.
- `system/argocd/values-dev.yaml`: `platform/forgejo` replaces `platform/gitea`.
- `platform/woodpecker`: `WOODPECKER_FORGEJO`, `WOODPECKER_FORGEJO_URL`, `WOODPECKER_FORGEJO_CLIENT`
  and `_SECRET` from `forgejo.woodpecker`, `WOODPECKER_ADMIN forgejo_admin`.
- `platform/renovate`: `"platform": "forgejo"`, endpoint `http://forgejo-http.forgejo:3000/api/v1`,
  token from `forgejo.renovate`. Woodpecker and Renovate stay undeployed until step 9.
- `apps/homepage.yaml` (icon `forgejo.svg`, link), `scripts/take-screenshots`.

## Error handling

- Kanidm unreachable when Forgejo starts: Forgejo registers the provider again at the next login
  (`ResetOAuth2`), so no startup probe is needed; the SSO test covers a fresh install, where Kanidm and
  Forgejo start together.
- The login source follows git: `update-oauth` at every start overwrites changes made in the UI.
- Admin rights follow `homelab-admins` at every login.
- Seed failures (GitHub or Forgejo unreachable) retry up to 10 times, then Argo CD shows the Job as
  failed; changing the script renames the Job and runs it again.
- PostgreSQL: CloudNativePG fails over to a replica; Forgejo reconnects through `forgejo-db-rw`.
- Offboarding: removing someone from Kanidm keeps their Forgejo access tokens and SSH keys; the
  onboarding docs say to disable the user in Forgejo too.

## Testing

Sandbox (`make dev`; the basic-checks regex does not change because the new tests need a cluster):

- `smoke_test.go`, `apps_test.go`: `{"forgejo", "forgejo"}` replaces Gitea.
- `TestGatewayForgejoSSH` (renamed): an `SSH-2.0-` banner on port 22.
- `TestSSOForgejo`: `dev-admin`, `dev-editor` and `dev-viewer` log in through Kanidm (`appLogin` from
  `/user/oauth2/Kanidm`); `GET /api/v1/user` with the session cookie returns their
  `preferred_username`, and `is_admin` is true only for `dev-admin`. `dev-outsider` gets Kanidm's
  "Access Denied".
- `TestForgejoSeed`: as `forgejo_admin` (password from Secret `forgejo-admin`),
  `GET /api/v1/repos/ops/homelab` returns 200 and the repository is private; `git ls-remote` over HTTPS
  lists `master`.
- Rename check: `grep -rni gitea --exclude-dir=charts` outside history (past specs and plans in
  `docs/superpowers/`, `docs/reference/changelog.md`, `docs/reference/architecture/decision-records.md`)
  finds only the chart- and image-imposed names listed in Context, plus this spec and its plan.

Basic checks on the branch: `scripts/render-chart` for `platform/forgejo`, `platform/cloudnative-pg`,
`system/argocd`, `system/gateway`, `platform/global-secrets`, `platform/kanidm`, `platform/woodpecker`,
`platform/renovate` and `apps/homepage.yaml`, each with and without `dev`; `go vet`; the basic tests;
`mkdocs build --strict`.

Not proven in the sandbox (recorded in `CLAUDE.md` under "Before the first production deploy"):
PostgreSQL through CloudNativePG, the Argo CD switch to Forgejo with its repository credential, and
`bootstrap.yml`'s detection.

## Docs

- Rename `docs/how-to-guides/use-both-github-and-gitea.md` to `use-both-github-and-forgejo.md`
  (Forgejo is the source of truth; push to GitHub when needed; the push mirror comes in step 9) and
  update `mkdocs.yml`.
- Update `README.md`, `docs/reference/architecture/overview.md`, `networking.md`,
  `docs/installation/post-installation.md`, `docs/concepts/secrets-management.md`,
  `docs/reference/roadmap.md`.
- `docs/installation/sandbox.md`: Forgejo in the identity table; RAM measured after `make dev`.
- `docs/getting-started/user-onboarding.md`: role groups also set Forgejo access (admins are site
  admins); offboarding disables the Forgejo user.
- `CLAUDE.md`: roadmap 5b done; sandbox notes (Forgejo, seed Job, SSH `TCPRoute`); production notes
  (CloudNativePG and the Argo CD switch only rendered, Argo CD reads with `forgejo_admin` until step 9,
  backups in step 10); the `scripts/hacks` note rewritten for Forgejo.
