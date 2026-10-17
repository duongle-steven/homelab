# Step 8b: OpenTofu's secrets reach OpenBao through External Secrets; ntfy runs in the cluster; `external/` removed

Status: design approved in chat 2026-10-17, written spec awaiting review. Branch: `feat/infra-secrets` (PR into `dev`).

## Goal

Every secret the cluster needs comes from OpenBao. The ones OpenTofu creates (Cloudflare's tokens and Tunnel
credentials, WireGuard's private key) or receives (Tailscale's auth key) reach OpenBao the way generated secrets do: as
Kubernetes Secrets that a `PushSecret` writes into OpenBao, through a writer that can never touch `generated/`.
Alerts go to a self-hosted ntfy (like upstream `nixos`), published through the Cloudflare Tunnel. `external/` (Terraform
Cloud state, ntfy, extra secrets) and the k8up-era `scripts/backup` are removed.

## Context

- After 8a: generated secrets go Password generator → Secret in `global-secrets` → `PushSecret` (store
  `openbao-writer`, role `writer`, `secret/<namespace>/generated/*` only) → OpenBao. OpenTofu writes only OpenBao's
  unseal key, as Secret `openbao/openbao-unseal` (unit `infra/<env>/openbao`, server-side apply, value on stdin).
- `infra/production/cloudflare` (7c) outputs `secrets` (sensitive): `cert-manager/cloudflare` (`api_token`),
  `external-dns/cloudflare` (`api_token`, `zone_id`), `cloudflared/cloudflare` (`credentials.json`). The token that
  created them (`~/.config/homelab/cloudflare.env`) never leaves the workstation and is not in state (checked). The
  charts expect Secrets `cloudflare-api-token` (cert-manager key `api-token`, external-dns key `value`) and
  `cloudflared-credentials` (`credentials.json`); nothing creates them yet.
- `apps/wireguard.yaml` and `apps/tailscale.yaml` (production only) read `wireguard/private-key` (`private-key`) and
  `tailscale/auth-key` (`auth-key`), which nothing writes yet.
- `external/` (upstream's Terraform Cloud workspace) keeps `ntfy` (Secret `monitoring-system/webhook-transformer`:
  ntfy.sh URL and a secret topic, read by Alertmanager's relay container) and `extra-secrets` (Secret
  `global-secrets/external`, read by nothing since 8a; `scripts/backup` still points at it and is broken).
- Upstream `nixos` has no `external/`: ntfy (`binwiederhier/ntfy`) runs in the cluster with `deny-all`, users and
  tokens declared in its environment from OpenBao, `NTFY_UPSTREAM_BASE_URL=https://ntfy.sh` (iOS push), a route on the
  LAN Gateway only; Alertmanager posts to it with `?template=alertmanager` and a token file, and its Watchdog receiver
  posts a message delayed 15 minutes with `sequence_id: watchdog`, so a stopped heartbeat lets the last one fire (a
  dead man's switch inside ntfy). Upstream has no VPN apps; the home network here sits behind CGNAT, so a LAN-only ntfy
  would be unreachable outside home until step 12.
- Decided 2026-10-17: OpenTofu generates what it can (WireGuard key); Tailscale's auth key comes from a user-only file;
  OpenTofu's values go through Kubernetes Secrets and `PushSecret` (not `kubectl exec` into OpenBao: ESO re-pushes
  within an hour if OpenBao loses its data, which has no backup until step 10, and it is the same pattern as
  `global-secrets`); ntfy self-hosted and published through the Tunnel, with iOS push through ntfy.sh (iPhone and
  Android), in this step.
- Versions (2026-10-17): ntfy v2.31.1, OJFord/wireguard 0.4.1+2, hashicorp/random 3.9.1.

## Decisions

| Topic | Decision |
|---|---|
| Delivery | OpenTofu unit `infra/<env>/secrets` writes Secrets into namespace `infra-secrets`; `platform/infra-secrets` pushes each to OpenBao |
| Writer | `SecretStore` `openbao-infra` (service account `openbao-infra`, role `infra`, audience `openbao`) in `infra-secrets` |
| Policy `infra` | `create`, `read`, `update` on `secret/data/*` and `secret/metadata/*`; `deny` on `secret/data/+/generated/*` and `secret/metadata/+/generated/*` |
| Push policy | `updatePolicy: Replace` (OpenTofu is the source: a rotated Cloudflare token reaches OpenBao), `deletionPolicy: None`, hourly |
| Cloudflare | Production: the `cloudflare` unit's output `secrets`, as a Terragrunt dependency |
| WireGuard | `wireguard_asymmetric_key` in the unit; output `wireguard_public_key` (for peers); a `prevent_destroy` guard in production, as `infra/<env>/openbao` |
| Tailscale | `TF_VAR_tailscale_auth_key` from `~/.config/homelab/secrets.env` (user-only, loaded by `.envrc` when present); unset: nothing written |
| Secret names | One Secret per OpenBao path, `<namespace>.<name>` (`cert-manager.cloudflare`, …), keys = properties |
| ntfy | `apps/ntfy.yaml` (namespace `ntfy`), routes on the LAN Gateway and `global.publicGateway`, `deny-all`, signup and attachments off, `NTFY_UPSTREAM_BASE_URL=https://ntfy.sh` in production only |
| ntfy users | `alerts` (write-only on topic `alerts`, Alertmanager) and `phone` (read-only, people's apps); passwords and tokens from `platform/global-secrets` |
| Alertmanager | Posts to `http://ntfy.ntfy/alerts?template=alertmanager` with the `alerts` token; Watchdog as upstream (delayed message, `sequence_id`); relay container `webhook-transformer` removed; enabled in the sandbox too |
| Removed | `external/`, `scripts/backup`, the root Makefile's `external`, `backup`, `restore` targets, `system/monitoring-system/files/webhook-transformer`, egress 443 for `monitoring-system` |

## Design

### OpenTofu unit `secrets`

`infra/_modules/secrets` takes `kubeconfig`, `secrets` (map path → map property → value, sensitive, default `{}`),
`tailscale_auth_key` (sensitive, default `""`) and `protect`. It adds `wireguard/private-key` (`private-key`, from
`wireguard_asymmetric_key`) and, when set, `tailscale/auth-key` (`auth-key`). It waits for namespace `infra-secrets`
(as `infra/_modules/openbao`'s `wait-namespace.sh`: its own step, so the timeout message shows) and applies every Secret
server-side, the values on stdin (one JSON document, never an argument or a file), on every run. Secrets it no longer
renders are left alone (removal is by hand: deleting an OpenBao value is an admin decision).

`infra/production/secrets`: depends on `../openbao` and `../cloudflare` (output `secrets`), `protect = true`.
`infra/sandbox/secrets`: depends on `../openbao`, no Cloudflare. Units run in both `make infra env=production` and
`make dev`.

### `platform/infra-secrets`

A chart in namespace `infra-secrets`: service account `openbao-infra`, `SecretStore` `openbao-infra`, and a `PushSecret`
per entry of `secrets` in its values (`<namespace>/<name>` with its properties):

| Entry | Properties | Environments |
|---|---|---|
| `cert-manager/cloudflare` | `api_token` | production |
| `external-dns/cloudflare` | `api_token`, `zone_id` | production |
| `cloudflared/cloudflare` | `credentials.json` | production |
| `wireguard/private-key` | `private-key` | production, sandbox |
| `tailscale/auth-key` | `auth-key` | production |

`values-dev.yaml` lists only the sandbox's entries, so no `PushSecret` waits for a Secret its environment never writes.
The namespace needs no mesh `allow` beyond the baseline (nothing calls it; External Secrets' controller makes the
calls).

### OpenBao

`platform/openbao/values.yaml` gains policy `infra` and role `infra` (`openbao-infra` in `infra-secrets`, audience
`openbao`, 10m). Production has not initialised yet, so its first start loads them; the sandbox is rebuilt.

### Consumers

- `system/cert-manager`: `ExternalSecret` `cloudflare-api-token` (`api-token` ← `cert-manager/cloudflare#api_token`),
  production only (the sandbox issues from its CA).
- `system/external-dns`: `ExternalSecret` `cloudflare-api-token` (`value` ← `external-dns/cloudflare#api_token`).
- `system/cloudflared`: `ExternalSecret` `cloudflared-credentials` (`credentials.json`).
- `system/monitoring-system`: `ExternalSecret` `ntfy` (`token` ← `monitoring-system/generated/ntfy#token`), mounted
  through `alertmanagerSpec.secrets`.

### ntfy

`apps/ntfy.yaml` (app-template): image `docker.io/binwiederhier/ntfy:v2.31.1`, `serve`, `NTFY_BASE_URL`
`https://ntfy.<baseDomain>`, `NTFY_BEHIND_PROXY`, cache and auth files on a 1Gi PVC, `NTFY_AUTH_DEFAULT_ACCESS=deny-all`,
`NTFY_ENABLE_SIGNUP=false`, no attachment cache dir (attachments off), `NTFY_AUTH_ACCESS` `alerts:alerts:wo` and
`phone:alerts:ro`, `NTFY_AUTH_USERS` and `NTFY_AUTH_TOKENS` from an `ExternalSecret` (the bcrypt hashes come from ESO's
`htpasswd` template function, as zot's). `global-secrets` entries generate each user's password and token (ntfy
tokens are `tk_` plus 29 characters) and push them to `ntfy/generated/…`; the `alerts` token also to
`monitoring-system/generated/ntfy`. Routes: the LAN Gateway and `global.publicGateway` (`public` in production: the
Tunnel; external-dns publishes the record). `allow`: the Gateways on its port, Alertmanager's service account.
Egress: `ntfy` 443 (ntfy.sh, production). `NTFY_UPSTREAM_BASE_URL` stays empty in the sandbox, so it sends nothing out.

People subscribe in the ntfy app (Android, iOS) to server `https://ntfy.<domain>`, topic `alerts`, user `phone`
(password from OpenBao or `global-secrets`, documented).

## Error handling

- A Secret not written yet (Tailscale's key unset, OpenTofu not applied): its `PushSecret` is not ready ("secret not
  found"); the consumer's `ExternalSecret` is not ready; nothing else is affected.
- OpenBao loses its data: `infra-secrets` and `global-secrets` push everything again within an hour.
- Namespace `infra-secrets` never appears: the unit fails after 30 minutes with a visible message.
- A token outside `generated/` collides with a generated path: refused by the `infra` policy; `scripts/check-secret-stores`
  refuses it before merge.
- ntfy down or the Tunnel down: alerts do not reach phones; the Watchdog's delayed message cannot fire either. An
  external dead man's switch is an idea, not in this step.

## Testing

- Offline: renders; `scripts/check-secret-stores` extended (only `platform/global-secrets` pushes `generated/` through
  `openbao-writer`, only `platform/infra-secrets` pushes elsewhere through `openbao-infra`; each push has a reader);
  `scripts/check-mesh-policies` (ntfy's `allow`, Alertmanager → ntfy in `EXTRA`); `scripts/check-env-config` (egress);
  `make -C infra check`.
- Sandbox (`test/`): the `secrets` unit's `wireguard/private-key` reaches OpenBao and equals the Secret in
  `infra-secrets`; after its OpenBao value is deleted it comes back (forced sync); role `infra` writes outside
  `generated/` but neither reads nor writes `generated/`; ntfy refuses requests without credentials, refuses a publish
  by `phone`, and an alert posted to Alertmanager reaches topic `alerts` (read as `phone`); the Watchdog's scheduled
  message exists; ntfy answers through the Gateway.
- Production: `terragrunt plan` of `infra/production/secrets` reads the Cloudflare output (no apply before the cluster
  exists).
- `make dev-clean; make dev`: every test passes.

## Docs

- `docs/concepts/secrets-management.md`: secrets from OpenTofu (`infra-secrets`), the two writers; the extra-secrets
  section replaced.
- New `docs/how-to-guides/alerts.md`: subscribing to ntfy.
- `docs/installation/production/external-resources.md` and other pages naming `external/` or `make external`; VPN
  page: keys come from OpenBao / OpenTofu.
- `CLAUDE.md`: roadmap 8b done; rules (two writers); production checklist (Cloudflare secrets reach the cluster;
  `secrets.env`; ntfy's public route untested; WireGuard's public key from the unit's output); Ideas: an external
  dead man's switch for the Watchdog.
