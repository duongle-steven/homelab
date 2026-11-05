{ config, pkgs, ... }:

let
  cluster-startup = pkgs.writeShellApplication {
    name = "cluster-startup";
    runtimeInputs = [
      config.services.k3s.package
      pkgs.coreutils
      pkgs.gnugrep
    ];
    text = builtins.readFile ./cluster-startup.sh;
  };
in
{
  environment.systemPackages = [ cluster-startup ];

  # Undoes an ordered shutdown when the nodes boot, with nobody home. It never holds up boot or k3s: nothing depends on it
  systemd.services.cluster-startup = {
    description = "Undo lab cluster shutdown";
    wantedBy = [ "multi-user.target" ];
    after = [ "k3s.service" ];
    # A failure, or not yet a majority of nodes back, tries again for as long as it takes
    startLimitIntervalSec = 0;
    serviceConfig = {
      Type = "oneshot";
      ExecStart = "${cluster-startup}/bin/cluster-startup";
      TimeoutStartSec = "infinity";
      Restart = "on-failure";
      RestartForceExitStatus = 75;
      RestartSec = 30;
    };
  };
}
