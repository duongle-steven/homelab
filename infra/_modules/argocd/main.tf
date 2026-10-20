# Apply Argo CD from the repository's system/argocd chart, again whenever its files, the values, the revision or the
# cluster change; Argo CD syncs everything else (system/argocd-config among it), not itself (upstream nixos's model).
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

variable "revision" {
  type        = string
  default     = ""
  description = "Commit or branch every homelab source and generator syncs (set-revision.sh); empty keeps the values file's"
}

# The values file to apply: the seed on production's first install while Forgejo does not run, never again once Argo
# CD reads Forgejo (choose-values.sh). A change of choice (Forgejo started) re-applies the bootstrap.
data "external" "values" {
  program = ["sh", "${path.module}/choose-values.sh"]
  query = {
    values      = var.values
    environment = var.environment
    seed        = var.seed
    kubeconfig  = var.kubeconfig
    # Where Argo CD reads once switched: the root ApplicationSet's repository in the values file
    forgejo_repository = try(yamldecode(file("${var.chart}/${var.values}"))["argocd-apps"]["applicationsets"]["root"]["generators"][0]["matrix"]["generators"][0]["git"]["repoURL"], "")
  }
}

locals {
  # Helm writes charts/ and Chart.lock: hash only what the repository holds
  files = sort(setunion(fileset(var.chart, "*.yaml"), fileset(var.chart, "templates/**")))
  hash  = sha1(join("", [for f in local.files : filesha1("${var.chart}/${f}")]))
  environment = {
    KUBECONFIG      = var.kubeconfig
    CHART           = var.chart
    VALUES          = data.external.values.result.values
    ENVIRONMENT     = data.external.values.result.environment
    SEED_REPOSITORY = var.seed_repository
    REVISION        = var.revision
  }
  command = "sh ${abspath("${path.module}/bootstrap.sh")}"
}

# It has no destroy-time action: destroying it leaves Argo CD running
resource "terraform_data" "bootstrap" {
  triggers_replace = [local.hash, data.external.values.result.values, var.cluster_id, var.revision]

  provisioner "local-exec" {
    command     = local.command
    environment = local.environment
  }
}

# The sandbox's bootstrap while Argo CD managed itself (production's unit was never applied)
moved {
  from = terraform_data.bootstrap[0]
  to   = terraform_data.bootstrap
}
