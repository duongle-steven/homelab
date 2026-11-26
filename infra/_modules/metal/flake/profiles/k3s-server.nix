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

  # etcdctl: TestTestbedSecretsEncrypted reads a Secret raw, and k3s ships none
  environment.systemPackages = [ pkgs.etcd ];

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

  # A stopgap until Istio matches k3s-server (docs/superpowers/specs/2026-11-25-istio-cni-k3s-name-design.md): in native
  # nftables mode istio-cni finds the kubelet by the comm of the process hosting it and accepts exactly "k3s", while k3s
  # installed on a host runs as "k3s-server" (its launcher executes <data-dir>/bin/k3s-server), so istio-cni crash-loops
  # on every node. It only reads that process's UID, so a process named k3s owned by root, like k3s, gives it the right
  # one; this holds only because k3s runs rootful. Remove it once an Istio release matches k3s-server (tracked in
  # docs/superpowers/deferred-minors.md until the upstream issue's number replaces it here). bash stays the
  # parent, waiting on a background sleep: a lone command would be exec'd and renamed, and a loop around a failing sleep
  # would spin; if sleep ends, so does the unit, and systemd starts it again after a pause
  systemd.services.k3s-name-for-istio =
    let
      named = pkgs.runCommand "k3s-name-for-istio" { } "mkdir -p $out/bin && ln -s ${pkgs.bash}/bin/bash $out/bin/k3s";
    in
    {
      description = "A process named k3s, where istio-cni looks for the kubelet's UID";
      before = [ "k3s.service" ];
      wantedBy = [ "multi-user.target" ];
      serviceConfig = {
        ExecStart = "${named}/bin/k3s -c '${pkgs.coreutils}/bin/sleep infinity & wait'";
        # As k3s itself: istio-cni takes this process's UID for the kubelet's
        User = "root";
        Restart = "always";
        RestartSec = 5;
        CapabilityBoundingSet = "";
        NoNewPrivileges = true;
        PrivateNetwork = true;
        PrivateTmp = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        MemoryMax = "16M";
      };
    };

  systemd.services.k3s.unitConfig = lib.mkIf (!k3s.clusterInit) {
    ConditionPathExists = config.services.k3s.tokenFile;
    RequiresMountsFor = "/var/lib/rancher/k3s";
  };

  # The control plane before the pods (docs/installation/testbed.md, 2026-11-26): etcd and the API server run inside k3s,
  # and under contention the pods won, since the kubelet weights kubepods.slice by its allocatable CPUs (157 for four)
  # against system.slice's 100: etcd's reads took 13 s and k3s lost its leases. k3s gets a slice of its own that
  # outweighs kubepods.slice, and the kubelet reserves a CPU (extraFlags) the scheduler never gives to pods
  systemd.slices.k3s.sliceConfig.CPUWeight = 1000;
  systemd.services.k3s.serviceConfig.Slice = "k3s.slice";

  # The API server's audit policy, shared with the k3d sandbox (step 15c): Alloy sends the log to Loki
  environment.etc."rancher/k3s/audit-policy.yaml".source = ../audit-policy.yaml;
  # The API server reads it only at start: a new policy restarts k3s at the switch (lab cluster upgrade drains first)
  systemd.services.k3s.restartTriggers = [ config.environment.etc."rancher/k3s/audit-policy.yaml".source ];
  systemd.tmpfiles.rules = [
    "d /var/log/kubernetes/audit 0700 root root -"
    # Woodpecker's Buildah steps' Localhost seccomp profile (step 15h, ../seccomp/README.md), shared with the k3d
    # sandbox; the kubelet reads it at each container start
    "d /var/lib/kubelet/seccomp/homelab 0755 root root -"
    "L+ /var/lib/kubelet/seccomp/homelab/buildah.json - - - - ${../seccomp/buildah.json}"
  ];

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
    disable = [
      # kube-vip serves the LoadBalancer Services (profiles/kube-vip.nix). ServiceLB ignores loadBalancerClass when it writes
      # a Service's status: at each change of endpoints it wrote kube-vip's status empty, and with no status kube-proxy has
      # no rule for the VIP. The Gateway's address stayed down when that came after kube-vip's write
      "servicelb"
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
      "--kube-apiserver-arg=audit-log-maxage=30"
      "--kube-apiserver-arg=audit-log-truncate-enabled=true"
      "--kube-apiserver-arg=audit-log-truncate-max-event-size=102400"
      # kube-bench's 1.2.30, 4.2.13 and 4.2.14 (docs/how-to-guides/cluster-scan.md): the same on k3d. A pod naming no
      # seccomp profile gets RuntimeDefault; Woodpecker's Buildah steps get homelab/buildah.json (tmpfiles above)
      "--kube-apiserver-arg=service-account-extend-token-expiration=false"
      "--kubelet-arg=pod-max-pids=4096"
      "--kubelet-arg=seccomp-default=true"
      # A CPU for k3s and the system (the slice above): allocatable drops to the rest, and kubepods.slice's weight with it
      "--kubelet-arg=kube-reserved=cpu=500m"
      "--kubelet-arg=system-reserved=cpu=500m"
      "--node-ip=${hostConfig.ip}"
      "--tls-san=${k3s.vip}"
      "--cluster-cidr=10.42.0.0/16"
      "--service-cidr=10.43.0.0/16"
    ]
    # Slow machines (config.json's slow_control_plane: testbed's VMs inside WSL's Hyper-V): a starved etcd leader misses
    # the default 100 ms heartbeat, and k3s exits when a controller loses its 15 s lease, so every client lists again and
    # etcd starves more. Production keeps the defaults, and so a fast failover
    ++ lib.optionals k3s.slowControlPlane (
      [
        "--etcd-arg=heartbeat-interval=500"
        "--etcd-arg=election-timeout=5000"
      ]
      ++ lib.concatMap
        (component: [
          "--${component}-arg=leader-elect-lease-duration=60s"
          "--${component}-arg=leader-elect-renew-deadline=40s"
        ])
        [
          "kube-controller-manager"
          "kube-scheduler"
          "kube-cloud-controller-manager"
        ]
    );
  };
}
