include "root" {
  path = find_in_parent_folders("root.hcl")
}

terraform {
  source = "${find_in_parent_folders("_modules")}//cloudflare"
}

locals {
  config = jsondecode(file("${get_terragrunt_dir()}/../cluster/config.json"))
}

inputs = {
  resource_prefix = "homelab-production"
  domain          = local.config.domain
  zone_name       = local.config.zone
}
