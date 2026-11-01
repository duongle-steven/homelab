{
  nixpkgs,
  nixpkgs-unstable,
  disko,
  nixie,
  hosts ? { },
  cluster ? null,
}:

{
  installer = nixpkgs.lib.nixosSystem {
    system = "x86_64-linux";
    modules = [
      nixie.nixosModules.nixie-agent
      ./profiles/installer.nix
    ];
  };
}
// nixpkgs.lib.mapAttrs (
  name: hostConfig:
  assert nixpkgs.lib.assertMsg (builtins.hasAttr cluster.init_host hosts)
    "Cluster init_host '${cluster.init_host}' is not present in hosts.json";
  nixpkgs.lib.nixosSystem {
    system = "x86_64-linux";
    specialArgs = {
      inherit hostConfig;
      pkgsUnstable = nixpkgs-unstable.legacyPackages.x86_64-linux;
      network = cluster.network;
      timezone = cluster.timezone;
      k3s = {
        clusterInit = name == cluster.init_host;
        vip = cluster.vip;
        nodeIPs = nixpkgs.lib.mapAttrsToList (_: host: host.ip) hosts;
      };
    };
    modules = [
      disko.nixosModules.disko
      ./configuration.nix
      ./profiles/network.nix
      ./profiles/k3s-server.nix
      ./profiles/kube-vip.nix
      ./profiles/rook-ceph.nix
      ./profiles/zram.nix
      ./profiles/vm-memory.nix
      {
        networking.hostName = name;
      }
    ];
  }
) hosts
