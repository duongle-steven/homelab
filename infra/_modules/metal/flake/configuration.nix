{ modulesPath, pkgs, timezone, ... }:

{
  imports = [
    (modulesPath + "/profiles/all-hardware.nix")
    ./disks.nix
  ];

  # The latest stable kernel, not NixOS's LTS (6.18): Ceph's AES256K CephX keys (CVE-2025-30156) need 7.0 or later in
  # the CSI clients (system/rook-ceph). A hotfix: back to an LTS once one has AES256K (lab cluster upgrade reboots)
  boot.kernelPackages = pkgs.linuxPackages_latest;
  boot.loader.systemd-boot.enable = true;
  boot.loader.efi.canTouchEfiVariables = true;

  nix.settings = {
    experimental-features = [
      "nix-command"
      "flakes"
    ];
    trusted-users = [ "admin" ];
  };
  nix.optimise.automatic = true;
  nix.gc = {
    automatic = true;
    dates = "weekly";
    options = "--delete-older-than 30d";
  };

  # The environment's time zone (infra/<env>/cluster/config.json), like every chart's
  time.timeZone = timezone;

  # Debugging on the node (the control plane's metrics endpoints, the testbed tests)
  environment.systemPackages = [ pkgs.curl ];

  # lab cluster shutdown powers every node off at once, and a node holding a Ceph volume hangs once the OSDs are gone: with
  # poweroff-force its final stage hung too, about 11 minutes in all, so go straight to the power off. That is a sudden cut
  # for what is left, which the power tests prove survivable
  systemd.targets.poweroff.unitConfig = {
    JobTimeoutSec = "180s";
    JobTimeoutAction = "poweroff-immediate";
  };

  services.openssh.enable = true;
  security.sudo.wheelNeedsPassword = false;

  users.users.admin = {
    isNormalUser = true;
    extraGroups = [
      "wheel"
    ];
    openssh.authorizedKeys.keyFiles = [ ./authorized_keys ];
  };
  users.users.root.openssh.authorizedKeys.keyFiles = [ ./authorized_keys ];

  system.stateVersion = "26.05";
}
