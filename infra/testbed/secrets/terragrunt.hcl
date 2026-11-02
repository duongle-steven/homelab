include "root" {
  path = find_in_parent_folders("root.hcl")
}

# Argo CD creates namespace infra-secrets when it syncs platform/infra-secrets
dependencies {
  paths = ["../argocd"]
}

terraform {
  source = "${find_in_parent_folders("_modules")}//secrets"
}
