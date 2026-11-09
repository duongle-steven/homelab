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
  description = "prod, testbed or dev: which ApplicationSet values scripts/env-values reads (production's seed file switches to seed)"
}

variable "repository" {
  type        = string
  description = "The homelab repository Argo CD syncs (production's ops/homelab on Forgejo, the seed until it switches)"
}

variable "branch" {
  type        = string
  description = "The branch every homelab source and generator syncs, unless revision overrides it"
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

variable "optional_apps" {
  type        = list(string)
  default     = []
  description = "The sandbox's optional apps to deploy (set-sandbox-apps.sh): paperless, opencloud"
}

variable "revision" {
  type        = string
  default     = ""
  description = "Commit or branch every homelab source and generator syncs (set-revision.sh); empty keeps the values file's"
}

variable "backup_s3_url" {
  type        = string
  default     = ""
  description = "S3 endpoint of the off-site backups (production's R2, which holds the account id: never in git), passed to the components as global.backupS3Url (set-backup-endpoint.sh); required in production"
}

# The values file to apply: the seed on production's first install while Forgejo does not run, never again once Argo
# CD reads Forgejo (choose-values.sh). A change of choice (Forgejo started) re-applies the bootstrap.
data "external" "values" {
  program = ["sh", "${path.module}/choose-values.sh"]
  query = {
    values          = var.values
    environment     = var.environment
    seed            = var.seed
    kubeconfig      = var.kubeconfig
    repository      = var.repository
    seed_repository = var.seed_repository
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
    BACKUP_S3_URL   = var.backup_s3_url
    OPTIONAL_APPS   = join(" ", var.optional_apps)
    REPOSITORY      = data.external.values.result.repository
    BRANCH          = var.branch
  }
  command = "sh ${abspath("${path.module}/bootstrap.sh")}"
}

# It has no destroy-time action: destroying it leaves Argo CD running
resource "terraform_data" "bootstrap" {
  triggers_replace = [local.hash, data.external.values.result.values, var.cluster_id, data.external.values.result.repository, var.branch, var.revision, var.backup_s3_url, var.optional_apps]

  lifecycle {
    precondition {
      condition     = var.environment != "prod" || var.backup_s3_url != ""
      error_message = "production needs backup_s3_url: Velero's data mover dials AWS without it"
    }
  }

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
