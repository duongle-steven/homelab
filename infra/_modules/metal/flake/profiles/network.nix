{ hostConfig, network, ... }:

{
  networking = {
    useDHCP = false;
    useNetworkd = true;
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
    networkConfig = {
      DHCP = "ipv6";
      IPv6AcceptRA = true;
      KeepConfiguration = "static";
    };
  };
}
