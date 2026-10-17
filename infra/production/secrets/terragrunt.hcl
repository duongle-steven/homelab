include "root" {
  path = find_in_parent_folders("root.hcl")
}

# Argo CD creates namespace infra-secrets when it syncs platform/infra-secrets
dependencies {
  paths = ["../argocd"]
}

dependency "cloudflare" {
  config_path = "../cloudflare"
  # Validation runs before the unit was applied
  mock_outputs                            = { secrets = {} }
  mock_outputs_allowed_terraform_commands = ["validate"]
}

terraform {
  source = "${find_in_parent_folders("_modules")}//secrets"
}

inputs = {
  secrets = dependency.cloudflare.outputs.secrets
  protect = true
}
