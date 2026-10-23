# Off-site backups (upstream nixos's module): the R2 bucket Velero writes to, a token that can write only that bucket,
# and Kopia's repository password. All three outlive the cluster (prevent_destroy): the password must never exist only
# in OpenBao, which is itself in the backups. Their secrets are the output `secrets`; infra/<env>/secrets writes them
# into OpenBao (docs/how-to-guides/backup-and-restore.md).

# The account the token belongs to: no account id in the repository
data "cloudflare_accounts" "this" {
  lifecycle {
    postcondition {
      condition     = length(self.result) == 1
      error_message = "The Cloudflare token must see exactly one account (scope it to yours)."
    }
  }
}

locals {
  account_id = data.cloudflare_accounts.this.result[0].id
}

data "cloudflare_account_api_token_permission_groups_list" "backup" {
  account_id = local.account_id

  # Cloudflare renaming the group would otherwise create a token without it
  lifecycle {
    postcondition {
      condition     = length([for group in self.result : group if group.name == "Workers R2 Storage Bucket Item Write"]) == 1
      error_message = "Cloudflare's permission group \"Workers R2 Storage Bucket Item Write\" was not found."
    }
  }
}

resource "random_password" "repository" {
  length = 32

  lifecycle {
    prevent_destroy = true
  }
}

resource "cloudflare_r2_bucket" "backup" {
  account_id = local.account_id
  name       = "${var.resource_prefix}-backup"

  lifecycle {
    prevent_destroy = true
  }
}

resource "cloudflare_account_token" "backup" {
  account_id = local.account_id
  name       = cloudflare_r2_bucket.backup.name
  policies = [{
    effect = "allow"
    permission_groups = [
      for group in data.cloudflare_account_api_token_permission_groups_list.backup.result : { id = group.id }
      if group.name == "Workers R2 Storage Bucket Item Write"
    ]
    resources = jsonencode({
      "com.cloudflare.edge.r2.bucket.${local.account_id}_default_${cloudflare_r2_bucket.backup.name}" = "*"
    })
  }]
}
