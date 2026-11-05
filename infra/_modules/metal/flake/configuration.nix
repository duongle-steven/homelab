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

  # lab cluster shutdown powers every node off at once, and a node holding a Ceph volume hangs once the OSDs are gone. systemd's
  # own limits do not help: poweroff-force's final stage took about 8 minutes, and poweroff-immediate leaves PID 1 in sync(), which
  # waits for the volume for good. So cut the power without syncing: a sudden cut for what is left, which the power tests prove
  # survivable. Nothing stops this unit, and systemd-shutdown spares processes whose name starts with @ (coreutils chooses what
  # to run by its name, so it needs --coreutils-prog)
  systemd.services.poweroff-deadline = {
    description = "Cut the power of a poweroff that hangs";
    wantedBy = [ "poweroff.target" ];
    unitConfig.DefaultDependencies = false;
    serviceConfig.ExecStart = ''@${pkgs.bash}/bin/bash @poweroff-deadline -c "(exec -a @poweroff-sleep ${pkgs.coreutils}/bin/coreutils --coreutils-prog=sleep 180) && echo o > /proc/sysrq-trigger"'';
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
