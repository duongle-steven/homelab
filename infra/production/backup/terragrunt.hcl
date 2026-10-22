include "root" {
  path = find_in_parent_folders("root.hcl")
}

# The R2 bucket for off-site backups, its write-only token and Kopia's password (docs/how-to-guides/backup-and-restore.md)
terraform {
  source = "${find_in_parent_folders("_modules")}//backup"
}

inputs = {
  resource_prefix = "homelab-production"
}
