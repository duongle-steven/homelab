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

# One rate limit per client IP on the whole zone (docs/how-to-guides/public-surface.md): the Free plan allows one rule,
# counted over 10 s and blocking for 10 s, whose expression may use only the path, so it cannot name the Tunnel's
# hosts. Nothing else in the zone is proxied; a proxied host added later gets it too. Per-account limits stay the
# apps' own (ntfy's per-visitor limits)
resource "cloudflare_ruleset" "rate_limit" {
  zone_id     = data.cloudflare_zone.zone.id
  name        = "${var.resource_prefix}-rate-limit"
  description = "One client IP's requests to the public apps"
  kind        = "zone"
  phase       = "http_ratelimit"
  rules = [{
    ref         = "per_ip"
    description = "Block a client IP over the threshold"
    expression  = "(http.request.uri.path wildcard \"/*\")"
    action      = "block"
    ratelimit = {
      # cf.colo.id: the API requires it beside the IP
      characteristics     = ["cf.colo.id", "ip.src"]
      period              = var.rate_limit_period
      requests_per_period = var.rate_limit_requests
      mitigation_timeout  = 10
    }
  }]
}

# The WAF's managed rules: the zone's managed entry point runs the Free plan's Free Managed Ruleset. Cloudflare creates no
# entry point on a Free zone (the plan of 2026-11-07 found none), and the ruleset's id is not published: it is found by
# name among the managed rulesets the zone may run
data "cloudflare_rulesets" "zone" {
  zone_id = data.cloudflare_zone.zone.id
}

locals {
  waf_managed_rulesets  = [for ruleset in data.cloudflare_rulesets.zone.rulesets : ruleset if ruleset.kind == "managed" && ruleset.phase == "http_request_firewall_managed"]
  free_managed_rulesets = [for ruleset in local.waf_managed_rulesets : ruleset if can(regex("(?i)free", ruleset.name))]
}

resource "cloudflare_ruleset" "managed" {
  zone_id     = data.cloudflare_zone.zone.id
  name        = "${var.resource_prefix}-managed"
  description = "The WAF's managed rules"
  kind        = "zone"
  phase       = "http_request_firewall_managed"
  rules = [{
    ref         = "free_managed"
    description = "Run the Free Managed Ruleset"
    expression  = "true"
    action      = "execute"
    action_parameters = {
      id = one(local.free_managed_rulesets[*].id)
    }
  }]

  lifecycle {
    precondition {
      condition     = length(local.free_managed_rulesets) == 1
      error_message = "Expected one managed WAF ruleset named Free, found: ${jsonencode(local.waf_managed_rulesets[*].name)}."
    }
  }
}
