# Step 10e: Woodpecker for other repositories on Forgejo

Status: design approved in chat 2026-10-21. Branch: `feat/woodpecker` from `dev`.

## Goal

Repositories on Forgejo other than `ops/homelab` get CI in the cluster: a push runs the repository's
`.woodpecker.yaml` and can build an image into zot. Only the people allowed to run code in the cluster can sign in.
The sandbox proves it end to end with a test repository; the blog's own repository comes later.

## Context

- `platform/woodpecker` (chart 3.7.5, app 3.19.0, production only): two agents on the Kubernetes backend,
  `WOODPECKER_OPEN: true`, `WOODPECKER_ADMIN: forgejo_admin`, an HTTPRoute `ci.<domain>`, an AuthorizationPolicy
  (Gateway and Forgejo's webhooks on 8000), egress 80/443. Its ExternalSecret `woodpecker-secret` reads
  `woodpecker/forgejo` (`client_id`, `client_secret`), which nothing writes, and `woodpecker/generated/agent`. The
  server does not read that Secret (CLAUDE.md's production checklist), and the agents never get the agent secret.
  `WOODPECKER_WEBHOOK_HOST` is Woodpecker 2's name; Woodpecker 3 reads `WOODPECKER_EXPERT_WEBHOOK_HOST`.
- Upstream `nixos` (`infra/_modules/platform/woodpecker.tf`, `cli/internal/forgejo`): `lab infra forgejo sync`
  creates a confidential Forgejo OAuth2 application `woodpecker` (redirect `https://ci.<domain>/authorize`) and
  writes `client_id`, `client_secret`, `redirect_uri` to `woodpecker/forgejo`; Forgejo maps a Kanidm group to an org
  team (`groupTeamMap`, `groupTeamMapRemoval: true`); server and agents read the agent secret from one Secret
  (`createAgentSecret: false`, `mapAgentSecret: false`, `extraSecretNamesForEnvFrom`).
- Our `lab infra forgejo sync` owns org `ops` and refuses any team grant on `ops/homelab`, so the CI org must be
  another org.
- Forgejo's accounts come only from Kanidm; its login source sets `homelab-admins` as Forgejo admins.
- zot lets user `ci` (htpasswd, `zot/generated/ci`) read, create and update every repository.
- The sandbox today: the k3d node uses 7.3 GiB of the workstation's 11.7 GiB, its pods 5.3 GiB, about 4 GiB is
  free. Woodpecker adds an estimated 100 to 150 MiB at rest (server and one agent) and up to 0.5 GiB while a build
  runs.
- `.woodpecker/` in this repository (from upstream `master`) runs removed checks; this repository's CI is GitHub
  Actions, and production's Forgejo holds it only to deploy (deferred minor "dev shell cleanup").
- Deferred minor #31: a change to Woodpecker's or zot's settings does not restart their pods.

## Decisions

1. **Sign-in only for members of Forgejo org `ci`:** `WOODPECKER_OPEN: true` with `WOODPECKER_ORGS: ci` (a user
   outside the org cannot register). `lab infra forgejo sync` creates the private org `ci` and its team `developers`
   (write on code and pull requests, may create org repositories, not all repositories); Forgejo's login source maps
   the Kanidm groups `homelab-admins` and `homelab-editors` to `ci/developers` (`groupTeamMap`,
   `groupTeamMapRemoval: true`, so leaving the group in Kanidm drops the membership at the next login).
   `homelab-users` and outsiders cannot sign in. `WOODPECKER_ADMIN: forgejo_admin` stays.
2. **The OAuth client from `lab infra forgejo sync`**, ported from upstream: the Forgejo OAuth2 application
   `woodpecker` (confidential, redirect `https://ci.<domain>/authorize`), its credentials written to
   `woodpecker/forgejo` (`client_id`, `client_secret`, `redirect_uri`) with the admin role, kept while they match
   and replaced otherwise; two applications named `woodpecker` fail the sync. `make infra env=production` already
   runs `sync`.
3. **Server and agents wired:** the server reads `woodpecker-secret` (Forgejo client and agent secret) and
   `woodpecker-environment`; the agents read only the agent secret (a second ExternalSecret `woodpecker-agent`), as
   upstream. `WOODPECKER_EXPERT_WEBHOOK_HOST` replaces `WOODPECKER_WEBHOOK_HOST`. No checksum annotation: the chart passes
   `podAnnotations` without `tpl`, so the wrapper cannot hash `woodpecker-environment` into them; settings in
   `server.env`/`agent.env` already roll the pods, and Secrets ESO writes from OpenBao never would. #31 stays
   deferred; 11b chooses between a controller that watches Secrets (Stakater Reloader) and explicit restarts.
4. **Step pods unprivileged:** no privileged plugins (`WOODPECKER_PLUGINS_PRIVILEGED` empty), every
   `*_ALLOW_FROM_STEP` option off (service account, node selector, labels, annotations, runtime class), steps in
   user namespaces (`WOODPECKER_BACKEND_K8S_USER_NAMESPACES`) so a step's root is not the node's root. The step
   pods' service account has no API access. The plan's first task proves it on the k3d node (Kubernetes 1.37, kernel
   6.18); if user namespaces do not work there, steps run without them, still unprivileged, and the spec's review
   decides.
5. **Images built with Buildah** (`quay.io/buildah/stable`, pinned): `--isolation chroot`, `--storage-driver vfs`,
   no privilege, pushed to `registry.<domain>/<repository>` as zot's `ci`. Kaniko is archived; BuildKit rootless
   needs seccomp and AppArmor unconfined.
6. **zot's credentials for pipelines:** an org secret `registry_password` in Woodpecker's org `ci` (with
   `registry_username: ci`), set with the admin role by a documented command (the value read from OpenBao, never
   printed). Pipelines use `from_secret`. Rotation (11b) changes it there too.
7. **The sandbox runs Woodpecker** (`system/argocd/values-dev.yaml`, `values-dev.yaml`: one agent, the dev CA for
   the server; Forgejo's clone and zot's push skip TLS verification only in the sandbox's test pipeline, since step
   pods cannot mount the CA). Its `woodpecker/forgejo` appears when `lab infra forgejo sync --env sandbox` runs;
   until then the server waits for its Secret.
8. **`.woodpecker/` removed** from this repository (fixes the "dev shell cleanup" minor).
9. **Versions:** the latest chart and server, agent and Buildah images, pinned.

## Failure handling

- `woodpecker/forgejo` missing (sync not run yet): the ExternalSecret errors and the server waits; nothing else
  depends on Woodpecker.
- A Forgejo OAuth application changed by hand: the next `sync` restores it and writes the new credentials; ESO
  refreshes `woodpecker-secret` within its refresh interval, then the server is restarted by hand (`kubectl rollout
  restart statefulset/woodpecker-server -n woodpecker`, documented).
- A build that needs privilege fails; it is never granted per repository.

## Tests

- Offline: `lab` unit tests for the org, team and OAuth client (fake Forgejo, as `sync_test.go`): created, kept when
  right, fixed when wrong, refused when duplicated; no secret in errors. Renders: server and agent env, no privileged
  plugins, the ExternalSecrets; `scripts/check-*` pass.
- Sandbox (new group `Woodpecker`, `test/scopes.yaml`): `lab infra forgejo sync --env sandbox`, then
  `dev-editor` signs in to Woodpecker through Forgejo and Kanidm, `dev-viewer` is refused (not in `ci`); as
  `dev-editor`, a test repository in org `ci` (a `Dockerfile` and a `.woodpecker.yaml` that builds with Buildah and
  pushes to zot as `ci`), activated through Woodpecker's API, pushed; the pipeline succeeds within 10 minutes and zot
  serves the tag; the step pods ran without privilege and in a user namespace; cleanup removes the repository, the
  Woodpecker repository and the image.
- `TestForgejoDeploy` also checks the OAuth client in OpenBao and that a second sync keeps it.

## Documentation

- `docs/how-to-guides/woodpecker.md`: signing in (groups), activating a repository, the org secret command, a
  `.woodpecker.yaml` example with Buildah, mirroring a repository into Forgejo.
- `docs/concepts/secrets-management.md`: `woodpecker/forgejo` written by `sync`; `docs/how-to-guides/deploy-to-forgejo.md`:
  what `sync` creates; `networking.md` if a rule changes.
- CLAUDE.md: 10e done; the production checklist's Woodpecker lines (server not reading its Secret, `woodpecker/forgejo`
  unwritten) replaced by what production still does not prove.
- `deferred-minors.md`: "dev shell cleanup" removed; #31 kept, its step changed to 11b (Reloader or explicit restarts).

## Out of scope

- The blog's repository and its deployment (after 10e, when its source exists), pull credentials for `apps/blog`.
- Forgejo Actions, Woodpecker for `ops/homelab`, a Nix cache, per-org namespaces.
