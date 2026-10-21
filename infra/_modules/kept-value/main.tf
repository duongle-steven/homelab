# A value given once (the GitHub App's private key) and kept in this unit's encrypted state: later applies may give
# none. A different value is refused until applied with -replace=<module>.terraform_data.kept, so a key is never
# dropped or swapped silently.
variable "value" {
  type      = string
  default   = ""
  sensitive = true
}

resource "terraform_data" "kept" {
  input = var.value

  lifecycle {
    ignore_changes = [input]
    postcondition {
      condition     = var.value == "" || var.value == self.output
      error_message = "A different value is kept: apply with -replace=<this module>.terraform_data.kept to take the new one."
    }
  }
}

output "value" {
  value     = terraform_data.kept.output
  sensitive = true
}
