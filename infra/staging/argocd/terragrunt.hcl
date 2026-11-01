include "root" {
  path = find_in_parent_folders("root.hcl")
}

# Production's deploy path on staging: Argo CD seeded from GitHub branch staging (make staging pushes it), then
# ops/homelab on staging's Forgejo once Argo CD's own credential reads it (choose-values.sh), as production switches.
# The nodes come first (infra/staging/metal); make staging-clean destroys this state with the VMs, so a new cluster is
# bootstrapped again
dependencies {
  paths = ["../metal"]
}

terraform {
  source = "${find_in_parent_folders("_modules")}//argocd"
}

inputs = {
  chart            = "${get_repo_root()}/system/argocd"
  values           = "values-staging.yaml"
  environment      = "staging"
  seed             = "values-staging.yaml"
  seed_environment = "staging"
  seed_revision    = "staging"
  seed_repository  = jsondecode(file("${get_terragrunt_dir()}/../cluster/config.json")).seed_repository
}
