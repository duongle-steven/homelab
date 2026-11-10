include "root" {
  path = find_in_parent_folders("root.hcl")
}

# A new cluster (its id changes) is bootstrapped again
dependency "cluster" {
  config_path                             = "../cluster"
  mock_outputs                            = { cluster_id = "not-created-yet" }
  mock_outputs_allowed_terraform_commands = ["validate", "plan"]
}

terraform {
  source = "${find_in_parent_folders("_modules")}//argocd"
}

inputs = {
  chart       = "${get_repo_root()}/system/argocd"
  values      = "values-dev.yaml"
  environment = "dev"
  # config.json's seed_repository: the GitHub repository, its branch sandbox pushed by just dev
  repository = jsondecode(file("${get_terragrunt_dir()}/../cluster/config.json")).seed_repository
  branch     = "sandbox"
  cluster_id = dependency.cluster.outputs.cluster_id
  # GitHub Actions tests one commit (just dev --revision <sha>) instead of branch sandbox
  revision = get_env("HOMELAB_SANDBOX_REVISION", "")
  # The optional apps just dev asks for (space-separated; docs/installation/sandbox.md)
  optional_apps = compact(split(" ", get_env("HOMELAB_SANDBOX_APPS", "")))
}
