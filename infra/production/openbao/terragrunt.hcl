include "root" {
  path = find_in_parent_folders("root.hcl")
}

# Argo CD creates namespace openbao when it syncs platform/openbao
dependencies {
  paths = ["../argocd"]
}

terraform {
  source = "${find_in_parent_folders("_modules")}//openbao"
}

inputs = {
  protect = true
}
