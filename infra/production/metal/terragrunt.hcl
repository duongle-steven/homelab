# Production's nodes (upstream nixos's infra/<env>/metal, step 12b-1): nixie installs the machines of hosts.json that
# it has not recorded (it wakes them, boots them from the network, then nixos-anywhere) and records each in hosts.json
# itself, which is committed; lab enrolls them into one k3s cluster and writes the kubeconfig
include "root" {
  path = find_in_parent_folders("root.hcl")
}

terraform {
  source = "${find_in_parent_folders("_modules")}//metal"

  after_hook "nixie" {
    commands    = ["apply"]
    working_dir = "${get_working_dir()}/flake"
    execute = [
      # The committed hosts.json itself (nixie records installs there); the workstation's LAN address from
      # HOMELAB_PXE_ADDRESS, needed only while a host is unrecorded
      "${get_repo_root()}/scripts/metal-nixie", "${get_terragrunt_dir()}/hosts.json",
      "--installer", "path:.#nixosConfigurations.installer",
      "--flake", "path:.",
      "--ssh-agent-socket", get_env("SSH_AUTH_SOCK", ""),
    ]
  }

  # Trust each node's host key on first contact (lab cluster checks known_hosts), for hosts.json's addresses only:
  # an accepted risk during the install on the home LAN; check a fingerprint on the machine's console
  # (docs/installation/production/deployment.md)
  after_hook "trust" {
    commands    = ["apply"]
    working_dir = get_repo_root()
    execute     = ["scripts/testbed-trust", "infra/production/metal/hosts.json"]
  }

  after_hook "enroll" {
    commands    = ["apply"]
    working_dir = get_repo_root()
    execute     = ["scripts/lab", "cluster", "enroll", "--environment", "production"]
  }

  after_hook "kubeconfig" {
    commands    = ["apply"]
    working_dir = get_repo_root()
    execute     = ["scripts/lab", "cluster", "kubeconfig", "--environment", "production", "--output", "${get_repo_root()}/infra/production/kubeconfig.yaml"]
  }

  # Nodes already installed take the flake's changes in place, one at a time (drain, nixos-rebuild, wait, uncordon)
  after_hook "upgrade" {
    commands    = ["apply"]
    working_dir = get_repo_root()
    execute     = ["scripts/lab", "cluster", "upgrade", "--environment", "production", "--flake", "${get_working_dir()}/flake"]
  }
}

inputs = {
  hosts           = jsondecode(file("${get_terragrunt_dir()}/hosts.json"))
  cluster         = jsondecode(file("${get_terragrunt_dir()}/../cluster/config.json"))
  authorized_keys = file("${get_terragrunt_dir()}/authorized_keys")
}
