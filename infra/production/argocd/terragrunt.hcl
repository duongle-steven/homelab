include "root" {
  path = find_in_parent_folders("root.hcl")
}

# The cluster comes from Ansible (make -C metal) until step 12. Re-applied when system/argocd changes; the first run
# after Forgejo runs switches from the seed to ops/homelab
terraform {
  source = "${find_in_parent_folders("_modules")}//argocd"
}

inputs = {
  chart           = "${get_repo_root()}/system/argocd"
  values          = "values.yaml"
  environment     = "prod"
  seed            = "values-seed.yaml"
  seed_repository = jsondecode(file("${get_terragrunt_dir()}/../cluster/config.json")).seed_repository
}
