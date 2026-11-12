include "root" {
  path = find_in_parent_folders("root.hcl")
}

# The nodes come first (infra/production/metal): it writes the kubeconfig the bootstrap applies with
dependencies {
  paths = ["../metal"]
}

# The cluster comes from infra/production/metal (nixie, lab cluster enroll). Re-applied when system/argocd changes; the first run
# after Forgejo runs switches from the seed to ops/homelab
terraform {
  source = "${find_in_parent_folders("_modules")}//argocd"
}

# Velero's location needs R2's endpoint, which holds the account id (never in git)
dependency "backup" {
  config_path = "../backup"
  # just infra check validates without applying
  mock_outputs                            = { endpoint = "https://account.r2.cloudflarestorage.com" }
  mock_outputs_allowed_terraform_commands = ["validate", "init"]
}

inputs = {
  chart       = "${get_repo_root()}/system/argocd"
  values      = "values.yaml"
  environment = "prod"
  seed        = "values-seed.yaml"
  # ops/homelab on Forgejo once Argo CD reads it; choose-values.sh keeps seed_repository until then
  repository      = "http://forgejo-http.forgejo:3000/ops/homelab"
  branch          = "master"
  seed_repository = jsondecode(file("${get_terragrunt_dir()}/../cluster/config.json")).seed_repository
  backup_s3_url   = dependency.backup.outputs.endpoint
  # platform for a new cluster's first deploys (12b-2): unset keeps the cluster's stage (infra/_modules/argocd/resolve-stage.sh)
  stage = get_env("HOMELAB_PRODUCTION_STAGE", "")
}
