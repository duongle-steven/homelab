# Cloudflare moved to infra/_modules/cloudflare (step 7c); ntfy and the extra secrets move to OpenBao in step 8

module "ntfy" {
  source = "./modules/ntfy"
  auth   = var.ntfy
}

module "extra_secrets" {
  source = "./modules/extra-secrets"
  data   = var.extra_secrets
}
