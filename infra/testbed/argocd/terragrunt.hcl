include "root" {
  path = find_in_parent_folders("root.hcl")
}

# Production's deploy path on testbed: Argo CD seeded from GitHub branch testbed (make testbed pushes it), then
# ops/homelab on testbed's Forgejo once Argo CD's own credential reads it (choose-values.sh), as production switches.
# The nodes come first (infra/testbed/metal); make testbed-clean destroys this state with the VMs, so a new cluster is
# bootstrapped again
dependencies {
  paths = ["../metal"]
}

terraform {
  source = "${find_in_parent_folders("_modules")}//argocd"
}

inputs = {
  chart            = "${get_repo_root()}/system/argocd"
  values           = "values-testbed.yaml"
  environment      = "testbed"
  seed             = "values-testbed.yaml"
  seed_environment = "testbed"
  seed_revision    = "testbed"
  seed_repository  = jsondecode(file("${get_terragrunt_dir()}/../cluster/config.json")).seed_repository
}
