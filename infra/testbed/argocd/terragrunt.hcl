include "root" {
  path = find_in_parent_folders("root.hcl")
}

# Argo CD syncs GitHub branch testbed, which just testbed pushes, as the sandbox's syncs branch sandbox. The nodes come
# first (infra/testbed/metal); just testbed-clean drops this state with the VMs, so a new cluster is bootstrapped again
dependencies {
  paths = ["../metal"]
}

terraform {
  source = "${find_in_parent_folders("_modules")}//argocd"
}

inputs = {
  chart       = "${get_repo_root()}/system/argocd"
  values      = "values-testbed.yaml"
  environment = "testbed"
}
