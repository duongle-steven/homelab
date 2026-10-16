include "root" {
  path = find_in_parent_folders("root.hcl")
}

dependencies {
  paths = ["../cluster"]
}

terraform {
  source = "${find_in_parent_folders("_modules")}//argocd"
}

inputs = {
  chart  = "${get_repo_root()}/system/argocd"
  values = "values-dev.yaml"
}
