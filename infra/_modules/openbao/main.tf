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
  description = "Refuse to replace or destroy the key (production)"
}

resource "random_bytes" "key" {
  length = 32
}

# prevent_destroy cannot take a variable, so it guards the key from a resource of its own: with protect set, a plan
# that would replace or destroy the key fails. Changing protect never changes the key
resource "terraform_data" "protected" {
  count            = var.protect ? 1 : 0
  triggers_replace = [sha256(random_bytes.key.base64)]

  lifecycle {
    prevent_destroy = true
  }
}

# Separate from the Secret below so its timeout message shows: OpenTofu hides the output of a provisioner whose
# environment holds the key
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

# Applied on every run (an idempotent server-side apply): a rebuilt cluster, whose identity OpenTofu cannot see in
# production (nodes from make -C metal), or a deleted Secret gets the same key again
resource "terraform_data" "secret" {
  triggers_replace = [plantimestamp()]
  depends_on       = [terraform_data.namespace]

  provisioner "local-exec" {
    command = "sh ${abspath("${path.module}/apply-secret.sh")}"
    environment = {
      KUBECONFIG = var.kubeconfig
      NAMESPACE  = var.namespace
      KEY        = random_bytes.key.base64
    }
  }
}
