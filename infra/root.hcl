# Included by every unit: infra/<env>/<unit>/terragrunt.hcl
locals {
  env        = split("/", path_relative_to_include())[0]
  unit       = basename(get_original_terragrunt_dir())
  kubeconfig = "${get_repo_root()}/infra/${local.env}/kubeconfig.yaml"

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

# The sandbox keeps local state (production's moves to R2 in Task 4 of the step 8c plan)
remote_state {
  backend = "local"
  generate = {
    path      = "backend.tf"
    if_exists = "overwrite_terragrunt"
  }
  config = {
    path = "${get_repo_root()}/.state/${path_relative_to_include()}/terraform.tfstate"
  }
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
          method   = method.aes_gcm.${local.state_key}
          enforced = true
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

# Local state holds secrets (Cloudflare tokens, the tunnel secret): readable by the user only
terraform {
  after_hook "state_permissions" {
    commands = ["apply", "destroy", "import", "refresh", "state"]
    execute  = ["chmod", "-R", "go-rwx", "${get_repo_root()}/.state"]
    # Also after a failed apply, which may already have written secrets
    run_on_error = true
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
    execute  = ["sh", "-c", "[ -n \"$HOMELAB_STATE_PASSPHRASE\" ] || { echo 'HOMELAB_STATE_PASSPHRASE is not set: see docs/installation/production/external-resources.md (~/.config/homelab/state.env)' >&2; exit 1; }"]
  }
}

inputs = {
  kubeconfig = local.kubeconfig
}
