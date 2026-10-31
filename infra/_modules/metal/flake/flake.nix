# The nodes' NixOS (upstream nixos's infra/_modules/metal/flake): one configuration per machine of hosts.json, and the
# installer nixie boots them into. The Terragrunt unit infra/<env>/metal writes hosts.json and cluster.json (the
# environment's infra/<env>/cluster/config.json)
{
  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-26.05";
    # k3s 1.37, the sandbox's version: nixos-26.05 stops at 1.36
    nixpkgs-unstable.url = "github:nixos/nixpkgs/nixos-unstable";
    disko = {
      url = "github:nix-community/disko";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    nixie.url = "github:khuedoan/nixie";
  };

  outputs =
    {
      nixpkgs,
      nixpkgs-unstable,
      disko,
      nixie,
      ...
    }:
    {
      nixosConfigurations = import ./. {
        inherit
          nixpkgs
          nixpkgs-unstable
          disko
          nixie
          ;
        hosts =
          if builtins.pathExists ./hosts.json then
            builtins.fromJSON (builtins.readFile ./hosts.json)
          else
            { };
        cluster =
          if builtins.pathExists ./cluster.json then
            builtins.fromJSON (builtins.readFile ./cluster.json)
          else
            null;
      };
    };
}
