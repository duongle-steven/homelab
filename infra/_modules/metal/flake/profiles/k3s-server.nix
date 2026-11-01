{
  config,
  lib,
  pkgs,
  pkgsUnstable,
  hostConfig,
  k3s,
  ...
}:

let
  gatewayAPI = "v1.7.0";
in
{
  assertions = [
    {
      assertion =
        config.services.k3s.clusterInit
        || (config.services.k3s.serverAddr != "" && config.services.k3s.tokenFile != null);
      message = "Non-initializer k3s servers require serverAddr and tokenFile.";
    }
  ];

  # TODO revisit THP after HA testing
  boot.kernelParams = [ "transparent_hugepage=never" ];

  # k3s requires these to route traffic between pods and services.
  boot.kernelModules = [ "br_netfilter" ];
  boot.kernel.sysctl = {
    "net.ipv4.ip_forward" = 1;
    "net.bridge.bridge-nf-call-iptables" = 1;
  };

  networking.firewall = {
    # https://docs.k3s.io/installation/requirements#inbound-rules-for-k3s-nodes
    allowedTCPPorts = [
      6443 # Kubernetes API server
      10250 # kubelet
      2379 # etcd client
      2380 # etcd peer
      80 # HTTP (ServiceLB, kube-vip's Gateway address)
      443 # HTTPS
    ];
    allowedUDPPorts = [
      8472 # flannel VXLAN
    ];
    # Metrics are reachable only from cluster pods and nodes: node exporter, etcd, kube-proxy, controller manager,
    # scheduler
    extraCommands = lib.concatMapStringsSep "\n" (
      source:
      "iptables -A nixos-fw -s ${source} -p tcp -m multiport --dports 9100,2381,10249,10257,10259 -j nixos-fw-accept"
    ) ([ "10.42.0.0/16" ] ++ k3s.nodeIPs);
  };

  systemd.services.k3s.unitConfig = lib.mkIf (!k3s.clusterInit) {
    ConditionPathExists = config.services.k3s.tokenFile;
    RequiresMountsFor = "/var/lib/rancher/k3s";
  };

  # The API server's audit policy, shared with the k3d sandbox (step 15c): Alloy sends the log to Loki
  environment.etc."rancher/k3s/audit-policy.yaml".source = ../audit-policy.yaml;
  # The API server reads it only at start: a new policy restarts k3s at the switch (lab cluster upgrade drains first)
  systemd.services.k3s.restartTriggers = [ config.environment.etc."rancher/k3s/audit-policy.yaml".source ];
  systemd.tmpfiles.rules = [ "d /var/log/kubernetes/audit 0700 root root -" ];

  services.k3s = {
    enable = true;
    role = "server";
    package = pkgsUnstable.k3s_1_37;
    # k3s's packaged Gateway API CRDs are an older release (disabled below): the latest from upstream instead
    manifests.gateway-api.source = pkgs.fetchurl {
      url = "https://github.com/kubernetes-sigs/gateway-api/releases/download/${gatewayAPI}/standard-install.yaml";
      hash = "sha256-TiYCxMvhr7w5TYb0rGUi4aUmOwETO7oeQKbRpUnnU6E=";
    };
    # Joiners remain stopped until lab enrolls them with the secure token.
    clusterInit = k3s.clusterInit;
    serverAddr = if k3s.clusterInit then "" else "https://${k3s.vip}:6443";
    tokenFile = if k3s.clusterInit then null else "/var/lib/rancher/k3s/enrollment/token";
    # ServiceLB stays: kube-vip serves only its class (profiles/kube-vip.nix)
    disable = [
      "traefik" # Istio serves the Gateways
      "local-storage" # rook-ceph serves the volumes
      "gateway-api-crd"
    ];
    extraFlags = [
      "--secrets-encryption"
      "--disable-helm-controller"
      # Control plane metrics for kube-prometheus-stack
      "--etcd-expose-metrics"
      "--kube-controller-manager-arg=bind-address=0.0.0.0"
      "--kube-scheduler-arg=bind-address=0.0.0.0"
      "--kube-proxy-arg=metrics-bind-address=0.0.0.0"
      # The API server's audit log; an event over 100KiB keeps who and what without its bodies (Loki refuses a line
      # over 256KB)
      "--kube-apiserver-arg=audit-policy-file=/etc/rancher/k3s/audit-policy.yaml"
      "--kube-apiserver-arg=audit-log-path=/var/log/kubernetes/audit/audit.log"
      "--kube-apiserver-arg=audit-log-maxsize=100"
      "--kube-apiserver-arg=audit-log-maxbackup=3"
      "--kube-apiserver-arg=audit-log-truncate-enabled=true"
      "--kube-apiserver-arg=audit-log-truncate-max-event-size=102400"
      "--node-ip=${hostConfig.ip}"
      "--tls-san=${k3s.vip}"
      "--cluster-cidr=10.42.0.0/16"
      "--service-cidr=10.43.0.0/16"
    ];
  };
}
