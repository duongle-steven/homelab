include "root" {
  path = find_in_parent_folders("root.hcl")
}

terraform {
  source = "${find_in_parent_folders("_modules")}//testbed-vms"
}

inputs = {
  hosts   = jsondecode(file("${get_terragrunt_dir()}/../metal/hosts.json"))
  network = jsondecode(file("${get_terragrunt_dir()}/../cluster/config.json")).network
}
