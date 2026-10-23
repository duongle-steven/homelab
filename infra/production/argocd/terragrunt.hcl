include "root" {
  path = find_in_parent_folders("root.hcl")
}

# The cluster comes from Ansible (make -C metal) until step 12. Re-applied when system/argocd changes; the first run
# after Forgejo runs switches from the seed to ops/homelab
terraform {
  source = "${find_in_parent_folders("_modules")}//argocd"
}

# Velero's location needs R2's endpoint, which holds the account id (never in git)
dependency "backup" {
  config_path = "../backup"
  # make -C infra check validates without applying
  mock_outputs                            = { endpoint = "https://account.r2.cloudflarestorage.com" }
  mock_outputs_allowed_terraform_commands = ["validate", "init"]
}

inputs = {
  chart           = "${get_repo_root()}/system/argocd"
  values          = "values.yaml"
  environment     = "prod"
  seed            = "values-seed.yaml"
  seed_repository = jsondecode(file("${get_terragrunt_dir()}/../cluster/config.json")).seed_repository
  backup_s3_url   = dependency.backup.outputs.endpoint
}
