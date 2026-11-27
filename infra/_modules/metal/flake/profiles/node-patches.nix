# G11 (docs/superpowers/specs/2026-11-27-node-patches-design.md): the nodes must not age silently. Each node publishes
# when the nixpkgs it was built from last changed, for node-exporter's textfile collector; NodePackagesStale
# (system/monitoring-system) fires past 14 days
{
  config,
  lib,
  pkgs,
  nixpkgsLastModified,
  nodeIndex,
  ...
}:

let
  nix-store-verify = pkgs.writeShellApplication {
    name = "nix-store-verify";
    runtimeInputs = [
      config.nix.package
      pkgs.coreutils
      pkgs.gnugrep
    ];
    text = builtins.readFile ./nix-store-verify.sh;
  };
in
{
  environment.etc."node-exporter/nixpkgs.prom".text = ''
    # HELP node_nixpkgs_last_modified_seconds When the nixpkgs this node was built from last changed (its lock's lastModified).
    # TYPE node_nixpkgs_last_modified_seconds gauge
  ''
  + lib.concatStrings (
    lib.mapAttrsToList (input: modified: ''
      node_nixpkgs_last_modified_seconds{input="${input}"} ${toString modified}
    '') nixpkgsLastModified
  );

  # A copy, not /etc's link into /nix/store: node-exporter reads the host's root under /host/root, where that link would
  # resolve inside its own container. Every switch and boot writes it again
  system.activationScripts.node-exporter-nixpkgs = {
    deps = [ "etc" ];
    text = "install -D -m 0644 /etc/node-exporter/nixpkgs.prom /var/lib/node-exporter/nixpkgs.prom";
  };

  # The store checked weekly against its recorded hashes (nix-store-verify.sh), its result for node-exporter. Idle I/O:
  # it reads the whole store on etcd's disk. Each node on its own hour (03:30, 05:30, 07:30, by its place in hosts.json),
  # and Persistent catches up a missed run after a power cut, spread by an hour so the nodes do not read at once
  systemd.services.nix-store-verify = {
    description = "Check the Nix store against its recorded hashes";
    serviceConfig = {
      Type = "oneshot";
      ExecStart = "${nix-store-verify}/bin/nix-store-verify";
      IOSchedulingClass = "idle";
      Nice = 19;
    };
  };
  systemd.timers.nix-store-verify = {
    wantedBy = [ "timers.target" ];
    timerConfig = {
      OnCalendar = "Sun *-*-* ${lib.fixedWidthNumber 2 (3 + 2 * nodeIndex)}:30:00";
      Persistent = true;
      RandomizedDelaySec = "1h";
    };
  };
}
