include "root" {
  path = find_in_parent_folders("root.hcl")
}

terraform {
  source = "${find_in_parent_folders("_modules")}//k3d"
}

inputs = {
  manifest_dir     = get_terragrunt_dir()
  audit_policy     = "${get_repo_root()}/infra/_modules/metal/flake/audit-policy.yaml"
  seccomp_profiles = "${get_repo_root()}/infra/_modules/metal/flake/seccomp"
  # HOMELAB_SANDBOX_PROXY=0 (GitHub Actions): no localhost proxy, its ports may be taken on the runner
  proxy = get_env("HOMELAB_SANDBOX_PROXY", "1") != "0"
  # HOMELAB_SANDBOX_CACHE=0 (GitHub Actions): no image caches, each runner starts empty
  cache = get_env("HOMELAB_SANDBOX_CACHE", "1") != "0"
}
