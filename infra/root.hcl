# Included by every unit: infra/<env>/<unit>/terragrunt.hcl
locals {
  env        = split("/", path_relative_to_include())[0]
  unit       = basename(get_original_terragrunt_dir())
  kubeconfig = "${get_repo_root()}/infra/${local.env}/kubeconfig.yaml"
  production = local.env == "production"
  # Production only: the account and OAuth token of `cf auth login` (empty strings for the sandbox, which never calls cf)
  cloudflare   = jsondecode(run_cmd("--terragrunt-global-cache", "--terragrunt-quiet", "${get_repo_root()}/scripts/cloudflare-credentials", local.env))
  state_bucket = "homelab-${local.env}-tfstate"
  # R2's S3 endpoint, and the command that runs lab (a fake endpoint and a stub in test/state_token_refresh_test.go)
  state_endpoint = get_env("HOMELAB_STATE_ENDPOINT", "https://${local.cloudflare.account_id}.r2.cloudflarestorage.com")
  lab            = get_env("HOMELAB_LAB", "go -C ${get_repo_root()}/cli run ./cmd/lab")

  # OpenTofu state encryption (every environment): the passphrase reaches OpenTofu only as TF_VAR_state_passphrase.
  # _PREVIOUS (scripts/rotate-state-passphrase) or HOMELAB_STATE_MIGRATE=1 (unencrypted state) adds a fallback.
  state_passphrase          = get_env("HOMELAB_STATE_PASSPHRASE", "")
  state_passphrase_previous = get_env("HOMELAB_STATE_PASSPHRASE_PREVIOUS", "")
  state_migrate             = get_env("HOMELAB_STATE_MIGRATE", "") == "1"
  # OpenTofu keeps a key provider's salt under its name in the encrypted state, so the fallback must carry the name that
  # encrypted it: the rotation script writes the new key as "next" (HOMELAB_STATE_ROTATING=1), then renames it "current"
  state_key          = get_env("HOMELAB_STATE_ROTATING", "") == "1" ? "next" : "current"
  state_fallback_key = local.state_key == "next" ? "current" : "next"
  state_commands     = ["init", "validate", "plan", "apply", "destroy", "refresh", "import", "output", "show", "state", "console", "taint", "untaint", "providers"]
}

# Production's state is on R2 through its S3 API (one object per unit, a lock file while a command runs), with
# expiring credentials from lab (r2-state.config); the sandbox's stays local. Both are encrypted (encryption.tf).
# Neither generated file holds a secret.
generate "backend" {
  path      = "backend.tf"
  if_exists = "overwrite_terragrunt"
  contents  = <<-EOF
    terraform {
    %{~if local.production}
      backend "s3" {
        bucket                      = "${local.state_bucket}"
        key                         = "${local.unit}/tfstate.json"
        region                      = "auto"
        endpoints                   = { s3 = "${local.state_endpoint}" }
        use_path_style              = true
        use_lockfile                = true
        profile                     = "r2-state"
        shared_config_files         = ["./r2-state.config"]
        skip_credentials_validation = true
        skip_region_validation      = true
        skip_requesting_account_id  = true
        skip_metadata_api_check     = true
        skip_s3_checksum            = true
      }
    %{~else}
      backend "local" {
        path = "${get_repo_root()}/.state/${path_relative_to_include()}/terraform.tfstate"
      }
    %{~endif}
    }
  EOF
}

generate "r2_credentials" {
  path      = "r2-state.config"
  if_exists = "overwrite_terragrunt"
  disable   = !local.production
  contents  = <<-EOF
    [profile r2-state]
    credential_process = ${local.lab} infra state credentials --account-id ${local.cloudflare.account_id} --bucket ${local.state_bucket}
  EOF
}

generate "encryption" {
  path      = "encryption.tf"
  if_exists = "overwrite_terragrunt"
  contents  = <<-EOF
    variable "state_passphrase" {
      type      = string
      sensitive = true
    }
    %{~if local.state_passphrase_previous != ""}
    variable "state_passphrase_previous" {
      type      = string
      sensitive = true
    }
    %{~endif}
    terraform {
      encryption {
        key_provider "pbkdf2" "${local.state_key}" {
          passphrase = var.state_passphrase
        }
        method "aes_gcm" "${local.state_key}" {
          keys = key_provider.pbkdf2.${local.state_key}
        }
        %{~if local.state_passphrase_previous != ""}
        key_provider "pbkdf2" "${local.state_fallback_key}" {
          passphrase = var.state_passphrase_previous
        }
        method "aes_gcm" "${local.state_fallback_key}" {
          keys = key_provider.pbkdf2.${local.state_fallback_key}
        }
        %{~endif}
        %{~if local.state_migrate}
        method "unencrypted" "migrate" {}
        %{~endif}
        state {
          method = method.aes_gcm.${local.state_key}
          # OpenTofu forbids an unencrypted fallback while enforced
          enforced = ${!local.state_migrate}
          %{~if local.state_passphrase_previous != "" || local.state_migrate}
          fallback {
            method = ${local.state_passphrase_previous != "" ? "method.aes_gcm.${local.state_fallback_key}" : "method.unencrypted.migrate"}
          }
          %{~endif}
        }
        plan {
          method   = method.aes_gcm.${local.state_key}
          enforced = true
        }
      }
    }
  EOF
}

# Local state (the sandbox) holds secrets (Cloudflare tokens, the tunnel secret): readable by the user only
terraform {
  after_hook "state_permissions" {
    commands = ["apply", "destroy", "import", "refresh", "state"]
    # Production writes nothing under .state/ (its state is on R2), which may not exist
    if      = !local.production
    execute = ["chmod", "-R", "go-rwx", "${get_repo_root()}/.state"]
    # Also after a failed apply, which may already have written secrets
    run_on_error = true
  }

  extra_arguments "cloudflare_oauth" {
    commands = ["plan", "apply", "destroy", "import", "refresh"]
    env_vars = {
      CLOUDFLARE_API_TOKEN = local.cloudflare.token
      CLOUDFLARE_API_KEY   = ""
      CLOUDFLARE_EMAIL     = ""
    }
  }

  # Production's init reconfigures, so a backend change (http to s3 in step 8d) never offers to copy state (state
  # moves between backends with `state push`, never `init -migrate-state`)
  extra_arguments "backend_reconfigure" {
    commands  = ["init"]
    arguments = local.production ? ["-reconfigure"] : []
  }

  before_hook "state_bucket" {
    commands = ["init"]
    if       = local.production
    execute  = ["sh", "-c", "${local.lab} infra state ensure --account-id ${local.cloudflare.account_id} --bucket ${local.state_bucket}"]
  }

  extra_arguments "state_passphrase" {
    commands = local.state_commands
    env_vars = {
      TF_VAR_state_passphrase          = local.state_passphrase
      TF_VAR_state_passphrase_previous = local.state_passphrase_previous
    }
  }

  # Parsing works without it (make -C infra check); any command that reads or writes state needs it
  before_hook "state_passphrase" {
    commands = local.state_commands
    execute = ["sh", "-c", <<-EOT
      [ -n "$HOMELAB_STATE_PASSPHRASE" ] || { echo 'HOMELAB_STATE_PASSPHRASE is not set: see docs/installation/production/external-resources.md (~/.config/homelab/state.env)' >&2; exit 1; }
      # One fallback only: unencrypted (MIGRATE) or the old passphrase (PREVIOUS)
      [ "$HOMELAB_STATE_MIGRATE" != 1 ] || [ -z "$HOMELAB_STATE_PASSPHRASE_PREVIOUS" ] || { echo 'HOMELAB_STATE_MIGRATE and HOMELAB_STATE_PASSPHRASE_PREVIOUS cannot both be set' >&2; exit 1; }
    EOT
    ]
  }
}

inputs = {
  kubeconfig = local.kubeconfig
}
