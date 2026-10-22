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

dependency "backup" {
  config_path                             = "../backup"
  mock_outputs                            = { secrets = {} }
  mock_outputs_allowed_terraform_commands = ["validate"]
}

terraform {
  source = "${find_in_parent_folders("_modules")}//secrets"
}

inputs = {
  secrets = merge(dependency.cloudflare.outputs.secrets, dependency.backup.outputs.secrets)
  protect = true
  # Renovate's GitHub App key, only when given (docs/how-to-guides/renovate.md): the state keeps it
  github_app_key = get_env("HOMELAB_GITHUB_APP_KEY", "") == "" ? "" : file(get_env("HOMELAB_GITHUB_APP_KEY"))
}
