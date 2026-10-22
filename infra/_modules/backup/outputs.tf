# Written into OpenBao (infra/<env>/secrets, platform/infra-secrets), where system/velero's ExternalSecrets read them.
# The endpoint holds the account id, so it travels in the credentials file (endpoint_url, read by
# velero-plugin-for-aws), never in git.
output "secrets" {
  sensitive = true
  value = {
    "velero/repository" = {
      cloud = <<-EOT
        [default]
        aws_access_key_id = ${cloudflare_account_token.backup.id}
        aws_secret_access_key = ${sha256(cloudflare_account_token.backup.value)}
        endpoint_url = https://${local.account_id}.r2.cloudflarestorage.com
      EOT
    }
    "velero/encryption" = {
      password = random_password.repository.result
    }
  }
}
