include "root" {
  path = find_in_parent_folders("root.hcl")
}

# A new cluster (its id changes) gets the Secret again
dependency "cluster" {
  config_path                             = "../cluster"
  mock_outputs                            = { cluster_id = "not-created-yet" }
  mock_outputs_allowed_terraform_commands = ["validate", "plan"]
}

# Argo CD creates namespace openbao when it syncs platform/openbao
dependencies {
  paths = ["../argocd"]
}

terraform {
  source = "${find_in_parent_folders("_modules")}//openbao"
}

inputs = {
  cluster_id = dependency.cluster.outputs.cluster_id
}
