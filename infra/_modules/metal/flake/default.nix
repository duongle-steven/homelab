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
      # The environment's disk (config.json's disk) when hosts.json names none: nixie keeps only the fields it knows
      defaultDisk = cluster.disk or null;
      timezone = cluster.timezone;
      k3s = {
        clusterInit = name == cluster.init_host;
        vip = cluster.vip;
        slowControlPlane = cluster.slow_control_plane or false;
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
      ./profiles/cluster-startup.nix
      {
        networking.hostName = name;
      }
    ];
  }
) hosts
