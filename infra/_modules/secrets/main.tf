# Secrets OpenTofu creates or receives, written as Kubernetes Secrets into namespace infra-secrets, from where
# platform/infra-secrets pushes them to OpenBao (secret/<namespace>/<name>)
terraform {
  required_version = ">= 1.13"
  required_providers {
    wireguard = {
      source  = "OJFord/wireguard"
      version = "0.4.1+2"
    }
  }
}

variable "kubeconfig" {
  type = string
}

variable "namespace" {
  type        = string
  default     = "infra-secrets"
  description = "Namespace of platform/infra-secrets, created by Argo CD"
}

variable "secrets" {
  type        = map(map(string))
  default     = {}
  sensitive   = true
  description = "OpenBao path (<namespace>/<name>) to properties, from other units (infra/<env>/cloudflare's output secrets)"
}

variable "protect" {
  type        = bool
  default     = false
  description = "Refuse to replace or destroy the WireGuard key (production): every peer would need the new public key"
}

# The cluster's WireGuard key (apps/wireguard.yaml); peers need its public key (output)
resource "wireguard_asymmetric_key" "wireguard" {}

# prevent_destroy cannot take a variable, so it guards the key from a resource of its own (as infra/_modules/openbao):
# with protect set, a plan that would replace or destroy the key fails
resource "terraform_data" "protected" {
  count            = var.protect ? 1 : 0
  triggers_replace = [wireguard_asymmetric_key.wireguard.public_key]

  lifecycle {
    prevent_destroy = true
  }
}

locals {
  secrets = merge(
    var.secrets,
    { "wireguard/private-key" = { "private-key" = wireguard_asymmetric_key.wireguard.private_key } },
  )
  manifest = yamlencode({
    apiVersion = "v1"
    kind       = "List"
    items = [for path, data in local.secrets : {
      apiVersion = "v1"
      kind       = "Secret"
      metadata   = { name = replace(path, "/", "."), namespace = var.namespace }
      stringData = data
    }]
  })
}

# Separate from the Secrets below so its timeout message shows: OpenTofu hides the output of a provisioner whose
# environment holds secrets
resource "terraform_data" "namespace" {
  triggers_replace = [plantimestamp()]

  provisioner "local-exec" {
    command = "sh ${abspath("${path.module}/wait-namespace.sh")}"
    environment = {
      KUBECONFIG = var.kubeconfig
      NAMESPACE  = var.namespace
    }
  }
}

# Applied on every run (an idempotent server-side apply): a rebuilt cluster or a deleted Secret gets the same values
resource "terraform_data" "secrets" {
  triggers_replace = [plantimestamp()]
  depends_on       = [terraform_data.namespace]

  provisioner "local-exec" {
    command = "sh ${abspath("${path.module}/apply-secrets.sh")}"
    environment = {
      KUBECONFIG = var.kubeconfig
      MANIFEST   = local.manifest
    }
  }
}

output "wireguard_public_key" {
  value       = wireguard_asymmetric_key.wireguard.public_key
  description = "The cluster's WireGuard public key, for its peers"
}
