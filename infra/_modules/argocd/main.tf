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

variable "environment" {
  type        = string
  description = "prod or dev: which ApplicationSet values scripts/env-values reads (the seed file switches to seed)"
}

variable "seed" {
  type        = string
  default     = ""
  description = "Values file used while Forgejo is not running (production's first install)"
}

variable "cluster_id" {
  type        = string
  default     = ""
  description = "Identity of the cluster: a new cluster is bootstrapped again"
}

variable "seed_repository" {
  type        = string
  default     = ""
  description = "Repository the seed syncs from (config.json's seed_repository)"
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
    KUBECONFIG      = var.kubeconfig
    CHART           = var.chart
    VALUES          = var.values
    ENVIRONMENT     = var.environment
    SEED_REPOSITORY = var.seed_repository
    SEED            = var.seed
  }
  command = "sh ${abspath("${path.module}/bootstrap.sh")}"
}

# prevent_destroy cannot take a variable: one resource per setting, only one of them exists
resource "terraform_data" "bootstrap" {
  count            = var.protect ? 0 : 1
  triggers_replace = [local.hash, var.values, var.seed, var.cluster_id]

  provisioner "local-exec" {
    command     = local.command
    environment = local.environment
  }
}

# Production bootstraps once: Argo CD manages itself afterwards, and a re-run would need a replacement, which
# prevent_destroy refuses
resource "terraform_data" "protected" {
  count            = var.protect ? 1 : 0
  triggers_replace = [local.hash, var.values, var.seed, var.cluster_id]

  provisioner "local-exec" {
    command     = local.command
    environment = local.environment
  }

  lifecycle {
    prevent_destroy = true
    ignore_changes  = [triggers_replace]
  }
}
