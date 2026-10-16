# Step 7c: a `cloudflare` unit replacing `external/`'s Cloudflare module

Status: design approved in chat 2026-10-16, written spec awaiting review. Branch: `feat/cloudflare` (PR into `dev`).

## Goal

Production's Cloudflare resources (the Tunnel, its DNS record, the API tokens for cert-manager and external-dns) are
created by an OpenTofu unit in `infra/production/cloudflare`, in upstream `nixos`'s module shape, for the user's
domain `hoangduong.dev`. The unit exposes the secrets those resources produce as a sensitive output; step 8 pushes
them into OpenBao. Production's `domain` becomes `hoangduong.dev`.

## Context

- `external/` (Terraform, state on upstream's Terraform Cloud workspace `khuedoan/homelab-external`, provider
  cloudflare 4.30) has three modules: `cloudflare` (tunnel, record, two tokens, written into Kubernetes Secrets),
  `ntfy` and `extra-secrets` (Kubernetes Secrets from `terraform.tfvars`).
- Upstream's `infra/_modules/cloudflare` (provider v5): zone lookup by name, `cloudflare_zero_trust_tunnel_cloudflared`
  (`config_src = "local"`), a CNAME `<prefix>-tunnel.<domain>` to `<tunnel id>.cfargotunnel.com`, two
  `cloudflare_account_token`s with Zone Read + DNS Write on the zone, and an output `secrets` keyed
  `cert-manager/cloudflare`, `external-dns/cloudflare`, `cloudflared/cloudflare`. It takes `cloudflare_account_id`,
  `resource_prefix`, `domain`, `zone_name`; staging uses `domain = staging.khuedoan.com` in zone `khuedoan.com`.
- Decided 2026-10-16: production uses the whole domain `hoangduong.dev` (staging later `staging.hoangduong.dev`), as
  upstream. The user created a Cloudflare API token (Tunnel Edit, Account API Tokens Edit, Account Settings Read,
  Zone Read, DNS Edit on `hoangduong.dev`) and stored it in `~/.config/homelab/cloudflare.env` (outside the repo);
  checked: active, the zone is active, tunnels and permission groups are readable.
- No production cluster exists, so nothing consumes the Secrets yet; the charts expect `cloudflared-credentials`
  (cloudflared), `cloudflare-api-token` (cert-manager, external-dns).

## Decisions

| Topic | Decision |
|---|---|
| Module | `infra/_modules/cloudflare`, upstream's resources and output; account found from the token (`data "cloudflare_accounts"`), so no account id in the repo |
| Unit | `infra/production/cloudflare`; `domain` and `zone` from `infra/production/cluster/config.json` (new key `zone`), `resource_prefix` `homelab-production` |
| Credentials | `CLOUDFLARE_API_TOKEN` from `~/.config/homelab/cloudflare.env`, loaded by `.envrc` when present |
| Secrets delivery | Output only (`secrets`, sensitive); step 8 writes them into OpenBao, where the charts' ExternalSecrets read them |
| `external/` | Its `cloudflare` module goes; `ntfy` and `extra-secrets` stay (Terraform Cloud) until step 8 moves them to OpenBao |
| Versions | cloudflare/cloudflare 5.28.0, hashicorp/random 3.9.1 |
| Apply | The unit is applied for real against the user's account in this step, after they approve the plan |

## Design

- `infra/_modules/cloudflare/{versions.tf,main.tf,variables.tf,outputs.tf}`: upstream's code, with
  `data "cloudflare_accounts"` (exactly one account, else fail with a message) in place of the `cloudflare_account_id`
  variable.
- `infra/production/cloudflare/terragrunt.hcl`: includes `root.hcl`; inputs `resource_prefix = "homelab-production"`,
  `domain` and `zone_name` read from `../cluster/config.json` (`domain`, `zone`).
- `infra/production/cluster/config.json`: `"domain": "hoangduong.dev"`, `"zone": "hoangduong.dev"`; the sandbox's gets
  `"zone": ""` (no Cloudflare). `scripts/env-values` ignores keys the ApplicationSets do not map.
- `apps/matrix/values.yaml` (exempt from `global.baseDomain`): `khuedoan.com` → `hoangduong.dev`.
- `.envrc`: `source_env_if_exists ~/.config/homelab/cloudflare.env`.
- `external/`: delete `modules/cloudflare` and its `main.tf` block and variables; `system/cloudflared/values.yaml`'s
  comment points at `infra/_modules/cloudflare` and step 8.
- Root `Makefile`: the production `infra` target already applies every unit in `infra/production` (`argocd` and now
  `cloudflare`).

## Error handling

- No token or a token without a permission: the provider fails with Cloudflare's error; `docs` name the permissions.
- More than one account visible to the token: the module fails asking for a token scoped to one account.
- Local state holds the token values and the tunnel secret: `.state/` is git-ignored and readable only by the user
  (step 8 moves state to R2 with encryption).

## Testing

- Offline: `make -C infra check`, `tofu validate` of the module.
- `terragrunt plan` in `infra/production/cloudflare`: 1 tunnel, 1 DNS record, 2 account tokens, 1 random password.
- After the user approves: `terragrunt apply`; then, through Cloudflare's API, the tunnel exists, the CNAME
  `homelab-production-tunnel.hoangduong.dev` points at it, and each new token verifies as active with access to the
  zone's DNS records; `terragrunt output -json secrets` has the three keys (values not printed). A second `plan`
  reports no changes.
- `make dev`: the sandbox is unaffected (98 tests).

## Docs

- `docs/installation/production/external-resources.md`: the Cloudflare token (permissions, file), `infra/production/cloudflare`.
- `CLAUDE.md`: roadmap 7c done; production checklist: the Cloudflare unit was applied, its secrets reach the cluster in
  step 8; `external/` keeps only ntfy and extra secrets.
