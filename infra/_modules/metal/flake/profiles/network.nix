{
  hostConfig,
  network,
  registry,
  ...
}:

{
  networking = {
    useDHCP = false;
    useNetworkd = true;
    # containerd pulls our images from zot by name (docs/how-to-guides/app-delivery.md): the LAN apps' names are in no
    # public DNS until the LAN DNS step, so the node resolves the registry to the LAN Gateway's VIP itself
    hosts.${registry.address} = [ registry.name ];
  };

  systemd.network.networks."10-uplink" = {
    matchConfig.MACAddress = hostConfig.mac_address;
    address = [ "${hostConfig.ip}/${toString network.prefix_length}" ];
    gateway = [ network.gateway ];
    # config.json's network.dns where the environment names one: production's ISP modem intercepts every UDP/53 query
    # leaving the LAN (slow negative answers), so production resolves through its router's DoH
    dns =
      network.dns or [
        "8.8.8.8"
        "1.1.1.1"
      ];
    # IPv4 only (G2, docs/superpowers/specs/2026-11-24-node-ports-design.md): nothing in the cluster uses IPv6, and a
    # global address from DHCPv6 or router advertisements would stand behind the router's firewall alone. With no IPv6
    # configured, networkd sets disable_ipv6 on the link; the loopback keeps ::1
    networkConfig = {
      DHCP = "no";
      IPv6AcceptRA = false;
      LinkLocalAddressing = "no";
      KeepConfiguration = "static";
    };
  };
}
