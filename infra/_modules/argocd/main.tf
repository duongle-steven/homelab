# Bootstrap Argo CD from the repository's system/argocd chart. Applied once and again only when the chart's files
# or the chosen values change; every later change reaches the cluster through Argo CD's own sync from git.
terraform {
  required_version = ">= 1.13"
}

variable "kubeconfig" {
  type = string
}

variable "chart" {
  type        = string
  description = "Absolute path of system/argocd"
}

variable "values" {
  type        = string
  description = "Values file in the chart directory"
}

variable "seed" {
  type        = string
  default     = ""
  description = "Values file used while Forgejo is not running (production's first install)"
}

variable "protect" {
  type        = bool
  default     = false
  description = "Refuse to destroy (production)"
}

locals {
  # Helm writes charts/ and Chart.lock: hash only what the repository holds
  files = sort(setunion(fileset(var.chart, "*.yaml"), fileset(var.chart, "templates/**")))
  hash  = sha1(join("", [for f in local.files : filesha1("${var.chart}/${f}")]))
  environment = {
    KUBECONFIG = var.kubeconfig
    CHART      = var.chart
    VALUES     = var.values
    SEED       = var.seed
  }
  command = "sh ${abspath("${path.module}/bootstrap.sh")}"
}

# prevent_destroy cannot take a variable: one resource per setting, only one of them exists
resource "terraform_data" "bootstrap" {
  count            = var.protect ? 0 : 1
  triggers_replace = [local.hash, var.values, var.seed]

  provisioner "local-exec" {
    command     = local.command
    environment = local.environment
  }
}

resource "terraform_data" "protected" {
  count            = var.protect ? 1 : 0
  triggers_replace = [local.hash, var.values, var.seed]

  provisioner "local-exec" {
    command     = local.command
    environment = local.environment
  }

  lifecycle {
    prevent_destroy = true
  }
}
