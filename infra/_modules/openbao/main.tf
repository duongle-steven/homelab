# OpenBao's unseal key (platform/openbao reads it from Secret openbao/openbao-unseal; seal "static"). The key lives in
# this unit's state, outside the cluster: losing it loses everything OpenBao holds
terraform {
  required_version = ">= 1.13"
  required_providers {
    random = {
      source  = "hashicorp/random"
      version = "3.9.1"
    }
  }
}

variable "kubeconfig" {
  type = string
}

variable "namespace" {
  type        = string
  default     = "openbao"
  description = "Namespace of platform/openbao, created by Argo CD"
}

variable "protect" {
  type        = bool
  default     = false
  description = "Refuse to destroy the key (production)"
}

# prevent_destroy cannot take a variable: one resource per setting, only one of them exists
resource "random_bytes" "key" {
  count  = var.protect ? 0 : 1
  length = 32
}

resource "random_bytes" "protected" {
  count  = var.protect ? 1 : 0
  length = 32

  lifecycle {
    prevent_destroy = true
  }
}

locals {
  key = one(concat(random_bytes.key[*].base64, random_bytes.protected[*].base64))
}

# Applied on every run (an idempotent server-side apply): a rebuilt cluster, whose identity OpenTofu cannot see in
# production (nodes from make -C metal), or a deleted Secret gets the same key again
resource "terraform_data" "secret" {
  triggers_replace = [plantimestamp()]

  provisioner "local-exec" {
    command = "sh ${abspath("${path.module}/apply-secret.sh")}"
    environment = {
      KUBECONFIG = var.kubeconfig
      NAMESPACE  = var.namespace
      KEY        = local.key
    }
  }
}
