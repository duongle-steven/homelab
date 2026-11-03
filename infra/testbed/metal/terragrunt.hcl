# The nodes (upstream nixos's infra/<env>/metal): nixie installs the machines of hosts.json that it has not recorded
# (it boots them from the network, then nixos-anywhere), lab enrolls them into one k3s cluster and writes the
# kubeconfig. nixie needs root for its ports (67, 69, 4011, 80)
locals {
  cluster = jsondecode(file("${get_terragrunt_dir()}/../cluster/config.json"))
}

include "root" {
  path = find_in_parent_folders("root.hcl")
}

terraform {
  source = "${find_in_parent_folders("_modules")}//metal"

  after_hook "nixie" {
    commands    = ["apply"]
    working_dir = "${get_working_dir()}/flake"
    execute = [
      # nixie as root on a copy of hosts.json in .state/ (it records installs there), with Wake-on-LAN played for the
      # VMs (scripts/testbed-wake)
      "${get_repo_root()}/scripts/testbed-nixie", "${get_terragrunt_dir()}/hosts.json",
      # The libvirt bridge's own address (the nodes' gateway): left to itself nixie picks WSL's mirrored interface,
      # which the VMs cannot reach
      "--address", local.cluster.network.gateway,
      "--installer", "path:.#nixosConfigurations.installer",
      "--flake", "path:.",
      "--ssh-agent-socket", get_env("SSH_AUTH_SOCK", ""),
    ]
  }

  # Trust each node's host key on first contact (lab cluster checks known_hosts): only on the workstation's own
  # libvirt network; make testbed-fresh forgets them with the disks. Production's are checked by hand
  after_hook "trust" {
    commands    = ["apply"]
    working_dir = get_repo_root()
    execute     = ["scripts/testbed-trust", "infra/testbed/metal/hosts.json"]
  }

  after_hook "enroll" {
    commands    = ["apply"]
    working_dir = get_repo_root()
    execute     = ["scripts/lab", "cluster", "enroll", "--environment", "testbed"]
  }

  after_hook "kubeconfig" {
    commands    = ["apply"]
    working_dir = get_repo_root()
    execute     = ["scripts/lab", "cluster", "kubeconfig", "--environment", "testbed", "--output", "${get_repo_root()}/infra/testbed/kubeconfig.yaml"]
  }

  # Nodes already installed take the flake's changes in place, one at a time (drain, nixos-rebuild, wait, uncordon)
  after_hook "upgrade" {
    commands    = ["apply"]
    working_dir = get_repo_root()
    execute     = ["scripts/lab", "cluster", "upgrade", "--environment", "testbed", "--flake", "${get_working_dir()}/flake"]
  }
}

dependencies {
  paths = ["../vms"]
}

inputs = {
  hosts           = jsondecode(file("${get_terragrunt_dir()}/hosts.json"))
  cluster         = jsondecode(file("${get_terragrunt_dir()}/../cluster/config.json"))
  authorized_keys = file("${get_terragrunt_dir()}/authorized_keys")
}
