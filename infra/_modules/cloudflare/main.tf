# Cloudflare resources of one environment (upstream nixos's module): the Tunnel the public Gateway is published
# through, its DNS record, and DNS tokens for cert-manager (DNS01) and external-dns. Their secrets are the output
# `secrets`; step 8 writes them into OpenBao.

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

data "cloudflare_zone" "zone" {
  filter = {
    name    = var.zone_name
    account = { id = local.account_id }
  }
}

data "cloudflare_account_api_token_permission_groups_list" "dns" {
  account_id = local.account_id
  scope      = "com.cloudflare.api.account.zone"

  # Cloudflare renaming a group would otherwise create tokens without it
  lifecycle {
    postcondition {
      condition     = length([for group in self.result : group if contains(["Zone Read", "DNS Write"], group.name)]) == 2
      error_message = "Cloudflare's permission groups \"Zone Read\" and \"DNS Write\" were not both found."
    }
  }
}

locals {
  dns_policies = [{
    effect = "allow"
    permission_groups = [
      for group in data.cloudflare_account_api_token_permission_groups_list.dns.result : { id = group.id }
      if contains(["Zone Read", "DNS Write"], group.name)
    ]
    resources = jsonencode({
      "com.cloudflare.api.account.zone.${data.cloudflare_zone.zone.id}" = "*"
    })
  }]
}

resource "random_password" "tunnel_secret" {
  length  = 64
  special = false
}

resource "cloudflare_zero_trust_tunnel_cloudflared" "homelab" {
  account_id    = local.account_id
  name          = "${var.resource_prefix}-tunnel"
  config_src    = "local"
  tunnel_secret = base64encode(random_password.tunnel_secret.result)
}

# Not proxied, not accessible. Just a record for auto-created CNAMEs by external-dns.
resource "cloudflare_dns_record" "tunnel" {
  zone_id = data.cloudflare_zone.zone.id
  type    = "CNAME"
  name    = "${var.resource_prefix}-tunnel.${var.domain}"
  content = "${cloudflare_zero_trust_tunnel_cloudflared.homelab.id}.cfargotunnel.com"
  proxied = false
  ttl     = 1 # Auto
}

resource "cloudflare_account_token" "external_dns" {
  account_id = local.account_id
  name       = "${var.resource_prefix}-external-dns"
  policies   = local.dns_policies
}

resource "cloudflare_account_token" "cert_manager" {
  account_id = local.account_id
  name       = "${var.resource_prefix}-cert-manager"
  policies   = local.dns_policies
}
