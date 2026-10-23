terraform {
  required_version = ">= 1.13"
  required_providers {
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "5.28.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "3.9.1"
    }
  }
}

# CLOUDFLARE_API_TOKEN from the environment (~/.config/homelab/cloudflare.env, loaded by .envrc)
provider "cloudflare" {}
