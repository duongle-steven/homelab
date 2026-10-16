# Written into OpenBao in step 8, where the charts' ExternalSecrets read them
output "secrets" {
  sensitive = true
  value = {
    "cert-manager/cloudflare" = {
      api_token = cloudflare_account_token.cert_manager.value
    }
    "external-dns/cloudflare" = {
      api_token = cloudflare_account_token.external_dns.value
      zone_id   = data.cloudflare_zone.zone.id
    }
    "cloudflared/cloudflare" = {
      "credentials.json" = jsonencode({
        AccountTag   = local.account_id
        TunnelName   = cloudflare_zero_trust_tunnel_cloudflared.homelab.name
        TunnelID     = cloudflare_zero_trust_tunnel_cloudflared.homelab.id
        TunnelSecret = base64encode(random_password.tunnel_secret.result)
      })
    }
  }
}
