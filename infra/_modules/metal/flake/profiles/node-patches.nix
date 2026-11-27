# G11 (docs/superpowers/specs/2026-11-27-node-patches-design.md): the nodes must not age silently. Each node publishes
# when the nixpkgs it was built from last changed, for node-exporter's textfile collector; NodePackagesStale
# (system/monitoring-system) fires past 14 days
{ lib, nixpkgsLastModified, ... }:

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
}
