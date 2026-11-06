variable "resource_prefix" {
  type        = string
  description = "Prefix of the tunnel, its DNS record and the tokens (homelab-<env>)"
}

variable "domain" {
  type        = string
  description = "The environment's domain (config.json domain)"
}

variable "zone_name" {
  type        = string
  description = "The Cloudflare zone holding the domain (config.json zone)"
}

# The rate limit's threshold per client IP (Free plan: one rule, counted over 10 s, blocking for 10 s). A cold load of
# Element web passes far below it, and system/public-scan's nuclei stays under half of it (test/public_scan_test.go)
variable "rate_limit_requests" {
  type        = number
  description = "Requests one client IP may make in rate_limit_period before Cloudflare blocks it"
  default     = 200
}

variable "rate_limit_period" {
  type        = number
  description = "The rate limit's counting period in seconds (the Free plan allows only 10)"
  default     = 10
}
