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
