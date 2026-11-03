include "root" {
  path = find_in_parent_folders("root.hcl")
}

terraform {
  source = "${find_in_parent_folders("_modules")}//testbed-vms"
}

inputs = {
  hosts   = jsondecode(file("${get_terragrunt_dir()}/../metal/hosts.json"))
  network = jsondecode(file("${get_terragrunt_dir()}/../cluster/config.json")).network
  # Layer 3 at 2 vCPUs: 1% idle under load, k3s losing its leases (docs/installation/testbed.md); 4 is the ceiling
  vcpus = 4
}
