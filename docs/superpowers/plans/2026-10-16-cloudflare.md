# Cloudflare Unit (Step 7c) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `infra/production/cloudflare` creates the Tunnel, its CNAME and the cert-manager/external-dns tokens for `hoangduong.dev`, with a sensitive `secrets` output for step 8.

**Architecture:** Upstream's `infra/_modules/cloudflare`, with the account found from the token; inputs from `infra/production/cluster/config.json`; the token from `~/.config/homelab/cloudflare.env` via `.envrc`.

**Tech Stack:** OpenTofu 1.13, Terragrunt 1.1, cloudflare/cloudflare 5.28.0, hashicorp/random 3.9.1.

**Spec:** `docs/superpowers/specs/2026-10-16-cloudflare-design.md`

## Global Constraints

- `resource_prefix` `homelab-production`; `domain` and `zone` `hoangduong.dev` (from `config.json`).
- No account id, token or secret value in the repository or in any printed output.
- Apply only after the user approves the plan output.

## Review Focus

1. The second plan must show no changes (tokens and tunnel are stable; `random_password` kept in state). Task 3.
2. The tokens must reach the zone's DNS only (policy resources = the zone id). Task 3 verifies via the API.
3. `scripts/env-values` and `check-env-config` must accept the new `zone` key without passing it to charts. Task 1.
4. `.envrc` must not fail when the token file is missing (other users, CI). Task 1.
5. The sandbox is unaffected. Task 4 (`make dev`).

---

### Task 1: Module, unit, config, credentials

- [ ] Write `infra/_modules/cloudflare/versions.tf` (`required_version >= 1.13`, cloudflare 5.28.0, random 3.9.1, `provider "cloudflare" {}` reading `CLOUDFLARE_API_TOKEN`), `main.tf` (upstream's resources; `data "cloudflare_accounts" "this" {}` and `local.account_id` from its single result, with a `precondition`/`check` failing "the token must see exactly one account"), `variables.tf` (`resource_prefix`, `domain`, `zone_name`), `outputs.tf` (upstream's `secrets`, sensitive).
- [ ] `infra/production/cloudflare/terragrunt.hcl`: include root; source `_modules//cloudflare`; inputs from `jsondecode(file("${get_terragrunt_dir()}/../cluster/config.json"))`: `domain`, `zone_name = .zone`; `resource_prefix = "homelab-production"`.
- [ ] `config.json`: production `domain` `hoangduong.dev`, `zone` `hoangduong.dev`; sandbox `zone` `""`. `apps/matrix/values.yaml`: `khuedoan.com` → `hoangduong.dev`.
- [ ] `.envrc`: add `source_env_if_exists ~/.config/homelab/cloudflare.env`.
- [ ] Check: `make -C infra check`; `scripts/env-values prod root` (no `zone` line); `scripts/check-env-config | tail -1` (0 failures; the production renders now carry `hoangduong.dev`).
- [ ] Commit `feat(infra): cloudflare unit for hoangduong.dev; production domain`.

### Task 2: `external/` and docs

- [ ] Delete `external/modules/cloudflare` and its block in `external/main.tf` and the Cloudflare variables in `external/variables.tf`, `external/terraform.tfvars.example`, `external/versions.tf` (provider), keeping ntfy and extra secrets; `system/cloudflared/values.yaml` comment → `infra/_modules/cloudflare` (secrets reach the cluster in step 8).
- [ ] `docs/installation/production/external-resources.md`: the Cloudflare token (permissions table, `~/.config/homelab/cloudflare.env`), the unit; CLAUDE.md: roadmap 7c done, checklist entries (unit applied; its secrets reach the cluster in step 8; `external/` keeps ntfy and extra secrets on Terraform Cloud until step 8).
- [ ] `mkdocs build --strict`; commit `docs: Cloudflare from infra/production/cloudflare`.

### Task 3: Plan, approval, apply, verify

- [ ] `cd infra/production/cloudflare && terragrunt plan` → expect 5 to add (tunnel, record, 2 tokens, random password); show the user the resource list.
- [ ] After the user approves: `terragrunt apply`.
- [ ] Verify through the API (no values printed): the tunnel `homelab-production-tunnel` exists; the CNAME `homelab-production-tunnel.hoangduong.dev` → `<tunnel id>.cfargotunnel.com`; each token from `terragrunt output -json secrets` verifies `active` and can list the zone's DNS records; `secrets` has the three keys.
- [ ] `terragrunt plan -detailed-exitcode` → 0.

### Task 4: Prove the sandbox

- [ ] Basic checks and `make dev` → 98 tests.
