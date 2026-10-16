# Included by every unit: infra/<env>/<unit>/terragrunt.hcl
locals {
  env        = split("/", path_relative_to_include())[0]
  unit       = basename(get_original_terragrunt_dir())
  kubeconfig = "${get_repo_root()}/infra/${local.env}/kubeconfig.yaml"
}

# Local state until step 8 (Cloudflare R2 with state encryption)
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

# Local state holds secrets (Cloudflare tokens, the tunnel secret): readable by the user only
terraform {
  after_hook "state_permissions" {
    commands = ["apply", "destroy", "import", "refresh", "state"]
    execute  = ["chmod", "-R", "go-rwx", "${get_repo_root()}/.state"]
    # Also after a failed apply, which may already have written secrets
    run_on_error = true
  }
}

inputs = {
  kubeconfig = local.kubeconfig
}
