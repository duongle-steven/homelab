# Offline (test/cloudflare_test.go runs it): the edge rules plan as intended, against a mock provider
mock_provider "cloudflare" {
  override_data {
    target = data.cloudflare_accounts.this
    values = { result = [{ id = "account", name = "account", type = "standard", created_on = "2026-01-01T00:00:00Z", managed_by = null, settings = null }] }
  }
  override_data {
    target = data.cloudflare_zone.zone
    values = { id = "00000000000000000000000000000001" }
  }
  override_data {
    target = data.cloudflare_account_api_token_permission_groups_list.dns
    values = { result = [
      { id = "read", name = "Zone Read", category = "zone", is_selectable = true, scopes = ["com.cloudflare.api.account.zone"] },
      { id = "write", name = "DNS Write", category = "dns", is_selectable = true, scopes = ["com.cloudflare.api.account.zone"] },
    ] }
  }
  override_data {
    target = data.cloudflare_rulesets.zone
    values = { rulesets = [{ id = "managed", kind = "zone", phase = "http_request_firewall_managed", name = "default", description = "", last_updated = "2026-01-01T00:00:00Z", version = "1" }] }
  }
}

variables {
  resource_prefix = "homelab-test"
  domain          = "example.org"
  zone_name       = "example.org"
}

run "edge" {
  command = plan

  assert {
    condition     = cloudflare_ruleset.rate_limit.phase == "http_ratelimit" && cloudflare_ruleset.rate_limit.kind == "zone"
    error_message = "the rate limit is the zone's http_ratelimit entry point"
  }
  assert {
    condition     = length(cloudflare_ruleset.rate_limit.rules) == 1
    error_message = "the Free plan allows one rate-limiting rule"
  }
  assert {
    condition     = cloudflare_ruleset.rate_limit.rules[0].action == "block"
    error_message = "the rule blocks"
  }
  assert {
    condition     = contains(cloudflare_ruleset.rate_limit.rules[0].ratelimit.characteristics, "ip.src") && contains(cloudflare_ruleset.rate_limit.rules[0].ratelimit.characteristics, "cf.colo.id")
    error_message = "counted per client IP (cf.colo.id is required by the API)"
  }
  assert {
    condition     = cloudflare_ruleset.rate_limit.rules[0].ratelimit.requests_per_period == var.rate_limit_requests && cloudflare_ruleset.rate_limit.rules[0].ratelimit.period == var.rate_limit_period
    error_message = "the threshold comes from the variables"
  }
  assert {
    condition     = cloudflare_ruleset.rate_limit.rules[0].ratelimit.period == 10 && cloudflare_ruleset.rate_limit.rules[0].ratelimit.mitigation_timeout == 10
    error_message = "the Free plan counts over 10 s and blocks for 10 s"
  }
  assert {
    condition     = !strcontains(cloudflare_ruleset.rate_limit.rules[0].expression, "http.host")
    error_message = "the Free plan's expression may use only the path"
  }
}

run "no_managed_rules" {
  command = plan

  override_data {
    target = data.cloudflare_rulesets.zone
    values = { rulesets = [] }
  }

  expect_failures = [check.managed_rules]
}
