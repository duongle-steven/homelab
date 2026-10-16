# Sub-project 1b: sandbox identity (secrets, Kanidm, Dex)

Status: design approved in chat 2026-10-12, written spec awaiting review. Branch: `dev`.

## Goal

The sandbox gets a working identity layer, declared in git and tested automatically:
generated secrets, Kanidm with groups and test accounts, and Dex in front of Kanidm.
Applications that log in through Dex (Grafana, ArgoCD, zot) are sub-project 1c.

## Context

- Builds on 1a (`docs/superpowers/specs/2026-10-12-kind-cilium-sandbox-design.md`):
  kind + Cilium, ingress-nginx at `172.30.255.200`, cert-manager issuing from the dev CA
  under the name `letsencrypt-prod`, ArgoCD syncing an allowlist from `origin dev`, and
  `values-dev.yaml` overlays.
- Upstream setup today:
  - `global-secrets` is a Job that runs `golang:1.19-alpine`, downloads modules at
    runtime and creates random secrets that do not exist yet. It never regenerates them.
  - external-secrets 0.10 copies them to app namespaces with the `v1beta1` API.
  - `scripts/hacks` uses `pexpect` to log in to the Kanidm CLI as `idm_admin` and creates
    the `editor` group and the `dex` OAuth2 client. It then writes `kanidm.dex` for Dex
    and configures Gitea.
  - `scripts/onboard-user` creates people and adds them to `editor`.
  - Kanidm runs as an app-template StatefulSet with a self-signed `home.arpa` cert. Dex
    talks to it with PKCE disabled.
- Smoke tests only check that ingresses return 200; nothing tests SSO.
- No production cluster exists yet. Production values still carry upstream's settings
  (`khuedoan.com`, …) until `make configure` runs in sub-project 3.

## Decisions

| Topic | Decision |
|---|---|
| Identity provisioning | Groups and OAuth2 clients in git through the **kaniop** operator; real people created by `scripts/onboard-user` (the repo is public) |
| Kanidm deployment | Managed by kaniop (`Kanidm` CR), replacing the app-template StatefulSet |
| Groups | `homelab-admins`, `homelab-editors`, `homelab-users`; anyone in none of them is refused |
| Generated secrets | ESO `Password` generators replace the Go Job; created once (`refreshInterval: 0`); manual rotation, documented |
| Test accounts | `dev-admin`, `dev-editor`, `dev-viewer` (one group each) and `dev-outsider` (no group), dev only |
| Test tooling | Go + chromedp in `test/`, driving `chromium` from nixpkgs |
| Versions | Current stable, verified 2026-10-12: external-secrets chart 2.12.0, kaniop 0.18.0, Kanidm 1.11.2, Dex chart 0.26.0 (Dex 2.46.0); re-check and pin exactly while planning |

## Design

### 1. Secrets chain

- **`platform/external-secrets`:** bump the chart 0.10.2 → 2.12.0.
- **API migration:** move every `ExternalSecret` and `ClusterSecretStore` in the repo to
  `external-secrets.io/v1`. This covers 10 files, including production-only apps:
  - `apps/paperless`, `apps/tailscale`, `apps/wireguard`
  - `platform/dex`, `platform/gitea`, `platform/global-secrets`, `platform/grafana`,
    `platform/renovate`, `platform/woodpecker`, `platform/zot`
- **`platform/global-secrets`:**
  - Delete `files/secret-generator/` and `templates/secret-generator/`.
  - A template iterates over a list in `values.yaml`, with the same entries as the old
    `config.yaml`: names, keys, lengths and special characters. Each entry renders:
    - a `Password` generator
    - an `ExternalSecret` with `refreshInterval: 0` that writes the Secret into
      namespace `global-secrets`
  - `ClusterSecretStore global-secrets` is unchanged in behaviour (API version only).
  - `values-dev.yaml` adds `dex.smoke` (key `client_secret`).
- **Rotation:** a new docs page describes manual rotation:
  1. Delete the target Secret; ESO regenerates it.
  2. Restart its consumers.
  3. For admin passwords stored in an app database, also change the password in the app.

### 2. Kanidm managed by kaniop (`platform/kanidm`)

- **Chart:** the dependency changes from app-template to the kaniop chart (operator and
  CRDs). Delete `templates/issuer.yaml` and `templates/certificate.yaml`.
- **New templates:**
  - `Certificate` for Kanidm from issuer `letsencrypt-prod` (the dev CA in the sandbox).
  - `Kanidm` CR:
    - Kanidm 1.11.2, one replica, 1Gi storage, that TLS secret
    - `domain`/`origin` from values: production uses `values.yaml` (still upstream's
      domain until `make configure`); dev uses `auth.127-0-0-1.nip.io`
    - ingress for the UI with `backend-protocol: HTTPS`
    - cross-namespace `kanidmRef` allowed for namespace `dex`
  - `KanidmGroup` × 3: `homelab-admins`, `homelab-editors`, `homelab-users`.
  - Dev only (`testAccounts: true` in `values-dev.yaml`): `KanidmPersonAccount` for each
    test account, and memberships `dev-admin` → admins, `dev-editor` → editors,
    `dev-viewer` → users. `dev-outsider` has no group.
- **Admin credentials:** the `admin`/`idm_admin` passwords live in the Secret that kaniop
  manages; nothing uses `recover-account` any more.
- **Scripts and Makefile:**
  - `scripts/onboard-user <username> <fullname> <mail> <admins|editors|users>`: reads the
    `idm_admin` password from the kaniop Secret, creates the person, adds them to
    `homelab-<group>` and prints a credential reset link.
  - Delete `scripts/kanidm-reset-password` and `scripts/hacks`.
  - Remove `post-install` from the root Makefile.

### 3. Dex and in-cluster SSO plumbing

- **`platform/dex/templates/kanidm-client.yaml`:**
  - a `KanidmOAuth2Client` named `dex` with `kanidmRef` to the Kanidm in namespace `kanidm`
  - redirect `https://<dex domain>/callback`
  - `scopeMap` granting `openid profile email groups` to the three `homelab-*` groups
  - kaniop writes `dex-kanidm-oauth2-credentials` into namespace `dex`
- **`platform/dex`:**
  - bump the chart 0.16.0 → 0.26.0
  - OIDC connector to `https://<auth domain>/oauth2/openid/dex`, with
    `pkceChallenge: S256`, `insecureEnableGroups: true` and `getUserInfo: true`; client ID
    and secret come from the kaniop Secret
  - drop the `gitea` static client and `GITEA_CLIENT_SECRET`; keep `grafana-sso` for 1c
- **`platform/dex/values-dev.yaml`:**
  - domain `dex.127-0-0-1.nip.io`
  - static client `smoke` with redirect `https://smoke.127-0-0-1.nip.io/callback` and
    secret from `dex.smoke`
  - the connector's `rootCAs` points at `ca.crt`, mounted from Dex's own
    `dex-tls-certificate` Secret. CA-issued certificates carry `ca.crt`, so no
    trust-manager is needed.
- **CoreDNS rewrite (dev only, `metal/`):**
  - an Ansible task that runs only for `env=dev` adds this rule to the kind Corefile and
    restarts CoreDNS:
    `rewrite name regex (.*)\.127-0-0-1\.nip\.io ingress-nginx-controller.ingress-nginx.svc.cluster.local answer auto`
  - Without it, pods resolve `*.127-0-0-1.nip.io` to their own loopback. Production does
    not need this.
- **Allowlist:** `system/argocd/values-dev.yaml` adds `platform/external-secrets`,
  `platform/global-secrets`, `platform/kanidm` and `platform/dex`.

### 4. Tests, workflow, docs

- **Tooling:** add `chromium` to the flake and `github.com/chromedp/chromedp` to
  `test/go.mod`.
- **Account setup helper:**
  - read the `idm_admin` password
  - create a credential reset token per test account
  - chromedp opens the reset link and sets a random password kept only in memory for
    the run
  - if Kanidm's account policy requires MFA, it also enrols TOTP and the test computes
    the codes
- **`TestIdentityLogin`**, for each of `dev-admin`, `dev-editor` and `dev-viewer`:
  1. open Dex `/auth?client_id=smoke…`
  2. log in at Kanidm, and consent on first use
  3. capture the redirect to `smoke…/callback?code=…`
  4. exchange the code at Dex with the `smoke` secret
  5. decode the `id_token` and assert `email` and that `groups` contains the account's
     group
- **`TestIdentityDenied`:** `dev-outsider` logs in at Kanidm and no code comes back.
- **Smoke tests:** in dev, `TestSmoke` also checks the `kanidm` and `dex` ingresses.
  `make dev` runs the smoke test with `filter=Smoke|Identity`.
- **RAM:** after a build, query Prometheus for `container_memory_working_set_bytes`
  summed by namespace and record the table in `docs/installation/sandbox.md`.
- **Docs:**
  - `docs/installation/sandbox.md`: test accounts, logging in, `onboard-user` with
    groups, and the RAM table.
  - New page: manual secret rotation.
  - `CLAUDE.md`: drop the "kanidm CLI 1.11" checklist line, and add notes on kaniop,
    the CoreDNS rewrite, the `smoke` client, and identity config in git vs people by
    script.

## Acceptance criteria (on branch `dev`)

1. `make dev` succeeds from scratch, including the identity tests, and succeeds again
   when run a second time.
2. All 9 allowlisted Applications are Synced and Healthy (1a's five plus
   external-secrets, global-secrets, kanidm and dex).
3. No `external-secrets.io/v1beta1` remains in the repo. Production and dev renders pass
   for every changed chart.
4. `dev-admin`, `dev-editor` and `dev-viewer` log in through Dex with the right `groups`;
   `dev-outsider` is refused.
5. Prometheus has no down targets.
6. The RAM table per namespace is in the docs.
7. `scripts/hacks`, `scripts/kanidm-reset-password` and the `post-install` target are
   gone, and `scripts/onboard-user` works against the new Kanidm.

## Risks

- **kaniop is 0.x** and has had a breaking CRD migration (v0.11). Pin it exactly and
  read the upgrade guide on every bump.
- **The Kanidm `groups` claim format** may be the SPN (`homelab-admins@<domain>`) rather
  than the short name. The tests record the actual value, and 1c's role mapping uses it.
- **Kanidm account policy** may require MFA for persons. The setup helper handles
  TOTP; if Kanidm insists on passkeys, the test needs a virtual WebAuthn authenticator
  (chromedp supports it), and this is verified first in the plan.
- **First sync ordering:** CRDs (ESO, kaniop) and webhooks arrive after their users, as
  in 1a. ArgoCD retries are expected to converge, and the build time is measured.
- **Upstream drift:** `platform/kanidm` no longer matches upstream, so upstream changes
  there will not cherry-pick.

## Out of scope

- 1c: Grafana, ArgoCD and zot SSO (OIDC, API keys, role mapping), and their tests
- Automatic secret rotation (and Reloader)
- `make configure` and a real domain (sub-project 3)
- Gitea, Woodpecker and Renovate in the sandbox
