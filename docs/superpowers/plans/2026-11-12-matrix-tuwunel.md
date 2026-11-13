# Matrix on tuwunel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the Dendrite and Element chart with one app-template file: tuwunel and FluffyChat, LAN only, signed in
through Kanidm, proven in the sandbox.

**Architecture:** `apps/matrix.yaml`, like `apps/actualbudget.yaml`:
- two controllers (server, client), routes on the LAN Gateway;
- a `KanidmOAuth2Client` `matrix` whose secret tuwunel reads from a file, and an AuthorizationPolicy;
- a `daily` volume.

**Tech Stack:** app-template (the version the `apps` ApplicationSet pins), tuwunel, FluffyChat, kaniop, Ginkgo.

**Spec:** `docs/superpowers/specs/2026-11-12-matrix-tuwunel-design.md`

## Global Constraints

- **Images:** the latest stable of `ghcr.io/matrix-construct/tuwunel` and `ghcr.io/krille-chan/fluffychat`, pinned exactly.
- **Hosts:** `matrix.<global.baseDomain>` (the server) and `chat.<global.baseDomain>` (the client), both on
  `istio-system/gateway` (LAN).
- **tuwunel's config:**
  - `server_name = "matrix.<domain>"`;
  - `login_with_password = false`, `allow_registration = false`, `allow_federation = false`;
  - one `[[global.identity_provider]]` for Kanidm (`client_id = "matrix"`, the secret from a file).
- **The Kanidm client:** `matrix` in namespace `matrix`; its scope map grants `homelab-users`, `homelab-editors` and
  `homelab-admins`; `matrix` is added to `oauth2ClientNamespaces`.
- **The volume:** `homelab/backup: daily`.
- **Public surface:**
  - `matrix` leaves `system/istio-system/values.yaml`'s `public`;
  - `chat` leaves `system/public-scan`'s targets;
  - `matrix` leaves `system/egress`'s internet table.
- **The sandbox:** `apps/matrix.yaml` in `values-dev.yaml`'s apps list. It trusts the sandbox CA the way the other apps
  do when `global.devCA` is set.

## Review Focus

1. **tuwunel trusting the sandbox CA for Kanidm's discovery:** without it, SSO fails only in the sandbox. Task 2's spec
   catches it.
2. **The old chart's leftovers:** `scripts/check-backup-groups`, `test/scopes.yaml`, the docs, `CLAUDE.md`.
   `scripts/ci-check all` catches most of them.
3. **Nothing of Matrix on Gateway `public`:** `TestPublicRoutes` and `TestGatewayPublicAllowList` pin it.

---

### Task 1: `apps/matrix.yaml`, and the old chart removed

**Files:**
- Create: `apps/matrix.yaml`.
- Delete: `apps/matrix/`.
- Modify:
  - `platform/kanidm/values.yaml` (`oauth2ClientNamespaces`);
  - `system/istio-system/values.yaml`;
  - `system/public-scan/values.yaml`;
  - `system/egress/values.yaml`;
  - `scripts/check-backup-groups`;
  - `test/scopes.yaml`;
  - `system/argocd/values-dev.yaml` (the apps list).
- Test: `scripts/ci-check render policy mesh backup-groups env-config`, and `TestPublicRoutes`.

- [ ] **Step 1:** Remove `apps/matrix/` and run the checks. Expected: FAIL. `check-backup-groups` still expects Matrix's
  claims, and the public map still lists `matrix`.
- [ ] **Step 2:** Write `apps/matrix.yaml` and the modifications. `check-backup-groups` expects `apps/matrix.yaml`'s
  data claim as `daily`.
- [ ] **Step 3:** Run the checks. Expected: PASS.
- [ ] **Step 4: Commit** `feat(matrix): tuwunel and FluffyChat on app-template, LAN only, signed in through Kanidm`.

### Task 2: The sandbox's proof

**Files:**
- Create: `test/e2e/matrix_test.go`.
  - `Describe("Matrix", Label("sandbox", "matrix"))`:
    - the client page at `chat.` answers;
    - `/_matrix/client/v3/login` offers only `m.login.sso`;
    - `dev-admin` signs in through Kanidm's form, the SSO helpers' way (identity tests), and gets an access token;
    - it creates a room and sends a message, which reads back.
- Modify: `test/scopes.yaml`: the label `matrix`, and `apps/matrix.yaml` mapped to it.

- [ ] **Step 1:** Write the spec, then `go vet` and `just cli check`.
- [ ] **Step 2:** `just dev` (from the main checkout, after 12b-2's runs). Expected: `Matrix` passes. A sign-in failure
  on the CA is fixed in the app file.
- [ ] **Step 3: Commit** `test(matrix): signs in through Kanidm and sends a message in the sandbox`.

### Task 3: Docs

- `CLAUDE.md`:
  - the Portability line (the domain in `apps/matrix`) goes;
  - `apps/matrix/` is no longer "the only app chart";
  - the before-production lines on Matrix (dendrite 0.13 → 0.14, its volumes and its PostgreSQL) go.
- `docs/how-to-guides/public-surface.md`: Matrix leaves the public list.
- `docs/superpowers/deferred-minors.md`: any minors.
- [ ] `mkdocs build --strict`, `scripts/ci-check all`, then commit.
