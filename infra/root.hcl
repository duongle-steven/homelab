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

inputs = {
  kubeconfig = local.kubeconfig
}
