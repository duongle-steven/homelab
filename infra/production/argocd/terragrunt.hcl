include "root" {
  path = find_in_parent_folders("root.hcl")
}

# The cluster comes from Ansible (make -C metal) until step 11
terraform {
  source = "${find_in_parent_folders("_modules")}//argocd"
}

inputs = {
  chart       = "${get_repo_root()}/system/argocd"
  values      = "values.yaml"
  environment = "prod"
  seed        = "values-seed.yaml"
  protect     = true
}
