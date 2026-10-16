include "root" {
  path = find_in_parent_folders("root.hcl")
}

terraform {
  source = "${find_in_parent_folders("_modules")}//k3d"
}

inputs = {
  manifest_dir = get_terragrunt_dir()
}
