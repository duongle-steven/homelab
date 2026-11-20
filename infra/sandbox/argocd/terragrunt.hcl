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
  # The deploy groups just dev chose (space-separated, lab test scope --groups; docs/how-to-guides/sandbox-tests.md)
  sandbox_groups = compact(split(" ", get_env("HOMELAB_SANDBOX_GROUPS", "")))
}
