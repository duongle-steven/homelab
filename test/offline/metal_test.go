package offline

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The node flake (infra/_modules/metal/flake), evaluated offline for three fixture hosts: what each node's k3s,
// kube-vip, swap and disks become
const metalFlake = "infra/_modules/metal/flake"

var metalFixtureVIP = "10.0.0.10"

type metalNode struct {
	K3s struct {
		ClusterInit bool     `json:"clusterInit"`
		ServerAddr  string   `json:"serverAddr"`
		ExtraFlags  []string `json:"extraFlags"`
		Disable     []string `json:"disable"`
		Version     string   `json:"version"`
		Restart     []string `json:"restart"`
	} `json:"k3s"`
	Manifests []string          `json:"manifests"`
	KubeVip   []json.RawMessage `json:"kubeVip"`
	Zram      bool              `json:"zram"`
	Data      struct {
		Label   string          `json:"label"`
		Content json.RawMessage `json:"content"`
	} `json:"data"`
	Disk             string              `json:"disk"`
	DNS              []string            `json:"dns"`
	Hosts            map[string][]string `json:"hosts"`
	TimeZone         string              `json:"timeZone"`
	Kernel           string              `json:"kernel"`
	PoweroffDeadline struct {
		WantedBy     []string `json:"wantedBy"`
		Dependencies bool     `json:"dependencies"`
		Exec         string   `json:"exec"`
	} `json:"poweroffDeadline"`
	Reclaim struct {
		Condition string `json:"condition"`
		Script    string `json:"script"`
		Timer     string `json:"timer"`
		Start     string `json:"start"`
	} `json:"reclaim"`
	NodeMetrics struct {
		Nixpkgs       string   `json:"nixpkgs"`
		NixpkgsCopy   string   `json:"nixpkgsCopy"`
		NixpkgsCopyOn []string `json:"nixpkgsCopyOn"`
		Verify        struct {
			Type     string   `json:"type"`
			IOClass  string   `json:"ioClass"`
			Nice     int      `json:"nice"`
			Exec     string   `json:"exec"`
			Calendar string   `json:"calendar"`
			Persist  bool     `json:"persist"`
			Delay    string   `json:"delay"`
			WantedBy []string `json:"wantedBy"`
		} `json:"verify"`
	} `json:"nodeMetrics"`
	Packages []string `json:"packages"`
	K3sName  struct {
		Exec       string   `json:"exec"`
		User       string   `json:"user"`
		Caps       string   `json:"caps"`
		Network    bool     `json:"network"`
		Restart    string   `json:"restart"`
		RestartSec string   `json:"restartSec"`
		Before     []string `json:"before"`
		WantedBy   []string `json:"wantedBy"`
	} `json:"k3sName"`
	Firewall struct {
		TCP   []int  `json:"tcp"`
		UDP   []int  `json:"udp"`
		Extra string `json:"extra"`
	} `json:"firewall"`
	SSH           map[string]any `json:"ssh"`
	Uplink        map[string]any `json:"uplink"`
	TunnelOffload struct {
		Condition string   `json:"condition"`
		Script    string   `json:"script"`
		WantedBy  []string `json:"wantedBy"`
	} `json:"tunnelOffload"`
}

var (
	metalOnce  sync.Once
	metalNodes map[string]metalNode
	metalErr   error
)

// metalEval evaluates every fixture host once (a NixOS evaluation takes a while)
func metalEval(t *testing.T) map[string]metalNode {
	t.Helper()
	metalOnce.Do(func() {
		dir := t.TempDir()
		cp := exec.Command("cp", "-r", filepath.Join(repo.Root(), metalFlake)+"/.", dir)
		if out, err := cp.CombinedOutput(); err != nil {
			metalErr = &os.PathError{Op: "cp", Path: string(out), Err: err}
			return
		}
		// node-a names its own disk; node-b and node-c take config.json's (the environment's)
		hosts := `{"node-a": {"mac_address": "02:00:00:00:00:01", "ip": "10.0.0.11", "disk": "/dev/vda"},
			"node-b": {"mac_address": "02:00:00:00:00:02", "ip": "10.0.0.12"},
			"node-c": {"mac_address": "02:00:00:00:00:03", "ip": "10.0.0.13"}}`
		cluster := `{"init_host": "node-a", "vip": "` + metalFixtureVIP + `", "disk": "/dev/nvme0n1",
			"network": {"prefix_length": 24, "gateway": "10.0.0.1", "dns": ["10.0.0.1"]},
			"service_vips": {"gateway": "10.0.0.2", "wireguard": "10.0.0.3"}, "domain": "example.test", "timezone": "Asia/Ho_Chi_Minh"}`
		for name, body := range map[string]string{"hosts.json": hosts, "cluster.json": cluster, "authorized_keys": "ssh-ed25519 AAAAfixture fixture\n"} {
			if metalErr = os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); metalErr != nil {
				return
			}
		}
		apply := `cs: builtins.mapAttrs (n: c: let k = c.config.services.k3s; m = k.manifests; in {
			k3s = { inherit (k) clusterInit serverAddr extraFlags disable; version = k.package.version;
				restart = map toString (c.config.systemd.services.k3s.restartTriggers or []); };
			manifests = builtins.attrNames m;
			kubeVip = if m ? kube-vip then m.kube-vip.content else [];
			zram = c.config.zramSwap.enable;
			data = { inherit (c.config.disko.devices.disk.main.content.partitions.data) label content; };
			disk = c.config.disko.devices.disk.main.device;
			dns = c.config.systemd.network.networks."10-uplink".dns;
			hosts = c.config.networking.hosts;
			timeZone = c.config.time.timeZone;
			k3sName = let s = c.config.systemd.services.k3s-name-for-istio or null; in if s == null then {} else {
				exec = s.serviceConfig.ExecStart; user = s.serviceConfig.User or ""; caps = s.serviceConfig.CapabilityBoundingSet;
				network = s.serviceConfig.PrivateNetwork; restart = s.serviceConfig.Restart;
				restartSec = toString (s.serviceConfig.RestartSec or "");
				inherit (s) before wantedBy;
			};
			firewall = { tcp = c.config.networking.firewall.allowedTCPPorts; udp = c.config.networking.firewall.allowedUDPPorts;
				extra = c.config.networking.firewall.extraCommands; };
			ssh = { inherit (c.config.services.openssh.settings) PasswordAuthentication KbdInteractiveAuthentication PermitRootLogin; };
			uplink = c.config.systemd.network.networks."10-uplink".networkConfig;
			packages = map (p: p.pname or p.name) c.config.environment.systemPackages;
			kernel = c.config.boot.kernelPackages.kernel.version;
			tunnelOffload = let s = c.config.systemd.services.vm-tunnel-offload or null; in if s == null then {} else {
				condition = s.unitConfig.ConditionVirtualization or "";
				script = s.script;
				wantedBy = s.wantedBy;
			};
			poweroffDeadline = let s = c.config.systemd.services.poweroff-deadline or null; in if s == null then {} else {
				inherit (s) wantedBy;
				dependencies = s.unitConfig.DefaultDependencies or true;
				exec = s.serviceConfig.ExecStart;
			};
			nodeMetrics = {
				nixpkgs = (c.config.environment.etc."node-exporter/nixpkgs.prom" or { text = ""; }).text;
				nixpkgsCopy = (c.config.system.activationScripts.node-exporter-nixpkgs or { text = ""; }).text;
				nixpkgsCopyOn = (c.config.system.activationScripts.node-exporter-nixpkgs or { deps = []; }).deps;
				verify = let s = c.config.systemd.services.nix-store-verify or null; tm = c.config.systemd.timers.nix-store-verify or null;
				in if s == null || tm == null then {} else {
					type = s.serviceConfig.Type; ioClass = s.serviceConfig.IOSchedulingClass; nice = s.serviceConfig.Nice;
					exec = toString s.serviceConfig.ExecStart; calendar = tm.timerConfig.OnCalendar;
					persist = tm.timerConfig.Persistent; delay = toString tm.timerConfig.RandomizedDelaySec;
					wantedBy = tm.wantedBy;
				};
			};
			reclaim = let s = c.config.systemd.services.memory-reclaim or null; in if s == null then {} else {
				condition = s.unitConfig.ConditionVirtualization or "";
				script = s.script;
				timer = c.config.systemd.timers.memory-reclaim.timerConfig.OnUnitActiveSec or "";
				start = c.config.systemd.timers.memory-reclaim.timerConfig.OnActiveSec or "";
			};
		}) (removeAttrs cs [ "installer" ])`
		cmd := exec.Command("nix", "eval", "--json", "--no-write-lock-file", "path:"+dir+"#nixosConfigurations", "--apply", apply)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			metalErr = &os.PathError{Op: "nix eval", Path: stderr.String(), Err: err}
			return
		}
		metalErr = json.Unmarshal(out, &metalNodes)
	})
	require.NoError(t, metalErr)
	require.Len(t, metalNodes, 3)
	return metalNodes
}

// Every node is a k3s server with this repository's flags: only init_host starts the cluster, the others join
// through the VIP; the audit log (step 15c), secrets encryption, the control plane's metrics; ServiceLB stays on
func TestMetalK3sFlags(t *testing.T) {
	for name, node := range metalEval(t) {
		k := node.K3s
		assert.Equal(t, name == "node-a", k.ClusterInit, name)
		if name != "node-a" {
			assert.Equal(t, "https://"+metalFixtureVIP+":6443", k.ServerAddr, name)
		}
		flags := strings.Join(k.ExtraFlags, " ")
		ip := map[string]string{"node-a": "10.0.0.11", "node-b": "10.0.0.12", "node-c": "10.0.0.13"}[name]
		for _, f := range []string{"--secrets-encryption", "--node-ip=" + ip, "--tls-san=" + metalFixtureVIP,
			"--etcd-expose-metrics", "--disable-helm-controller"} {
			assert.Contains(t, k.ExtraFlags, f, name)
		}
		for _, f := range auditFlags {
			assert.Contains(t, flags, "--kube-apiserver-arg="+f, name)
		}
		assert.ElementsMatch(t, []string{"servicelb", "traefik", "local-storage", "gateway-api-crd"}, k.Disable, name)
		assert.True(t, strings.HasPrefix(k.Version, "1.37."), "%s runs k3s %s, the sandbox 1.37", name, k.Version)
		assert.Contains(t, node.Manifests, "gateway-api", name)
		// The API server reads the audit policy only at start: a new policy restarts k3s at the switch
		policy := false
		for _, r := range k.Restart {
			policy = policy || strings.HasSuffix(r, "audit-policy.yaml")
		}
		assert.True(t, policy, "%s: k3s does not restart when the audit policy changes (%v)", name, k.Restart)
	}
}

// kube-vip holds the API's VIP and serves the Services of its class (the Gateway's and WireGuard's addresses), the only
// LoadBalancer Services there are: ServiceLB is off, so no cloud controller hands out addresses
func TestMetalKubeVip(t *testing.T) {
	for name, node := range metalEval(t) {
		assert.NotContains(t, node.Manifests, "kube-vip-cloud-controller", name)
		var all strings.Builder
		for _, r := range node.KubeVip {
			all.Write(r)
		}
		manifest := all.String()
		assert.Contains(t, manifest, `"ghcr.io/kube-vip/kube-vip:v1.3.1"`, name)
		for _, env := range []string{`"name":"svc_enable","value":"true"`, `"name":"lb_class_only","value":"true"`,
			`"name":"lb_class_name","value":"kube-vip.io/kube-vip-class"`, `"name":"address","value":"` + metalFixtureVIP + `"`} {
			assert.Contains(t, manifest, env, name)
		}
	}
}

// zram lets a 3.5 GB testbed node keep more in memory
func TestMetalZram(t *testing.T) {
	for name, node := range metalEval(t) {
		assert.True(t, node.Zram, name)
	}
}

// A VM hands back only the pages its kernel frees, never its page cache: on a VM (testbed) a timer reclaims cache
// each minute so the node keeps some memory free, which free page reporting returns to WSL; hardware never runs it
func TestMetalVMMemoryReclaim(t *testing.T) {
	for name, node := range metalEval(t) {
		assert.Equal(t, "vm", node.Reclaim.Condition, name)
		assert.Equal(t, "1min", node.Reclaim.Timer, name)
		assert.Equal(t, "1min", node.Reclaim.Start, "%s: starts after an upgrade too, not only at boot", name)
		assert.Contains(t, node.Reclaim.Script, "/sys/fs/cgroup/memory.reclaim", name)
		assert.Contains(t, node.Reclaim.Script, "swappiness=0", "%s: only the page cache, never swap", name)
	}
}

// A node holding a Ceph volume hangs at poweroff once the OSDs are gone, PID 1 itself in sync(): a unit started with the
// poweroff, which nothing stops, cuts the power without syncing. Its processes' names start with @, which systemd-shutdown spares
func TestMetalPoweroffDeadline(t *testing.T) {
	for name, node := range metalEval(t) {
		deadline := node.PoweroffDeadline
		assert.Equal(t, []string{"poweroff.target"}, deadline.WantedBy, name)
		assert.False(t, deadline.Dependencies, "%s: the default dependencies stop it at shutdown", name)
		assert.Regexp(t, `^@\S+/bash @poweroff-deadline -c `, deadline.Exec, name)
		assert.Contains(t, deadline.Exec, "exec -a @poweroff-sleep", name)
		assert.Contains(t, deadline.Exec, "--coreutils-prog=sleep 180", name)
		assert.Contains(t, deadline.Exec, "echo o > /proc/sysrq-trigger", name)
	}
}

// Its sleep really sleeps under that name: NixOS's coreutils is one program choosing by its name, so a wrong one exits at
// once, and every poweroff becomes a sudden cut
func TestMetalPoweroffDeadlineWaits(t *testing.T) {
	coreutils, err := exec.LookPath("coreutils")
	require.NoError(t, err)
	for name, node := range metalEval(t) {
		trigger := filepath.Join(t.TempDir(), "sysrq-trigger")
		script := regexp.MustCompile(`-c "(.*)"$`).FindStringSubmatch(node.PoweroffDeadline.Exec)
		require.Len(t, script, 2, name)
		command := regexp.MustCompile(`\S+/bin/coreutils`).ReplaceAllString(script[1], coreutils)
		command = strings.ReplaceAll(strings.ReplaceAll(command, "--coreutils-prog=sleep 180", "--coreutils-prog=sleep 2"), "/proc/sysrq-trigger", trigger)
		started := time.Now()
		out, err := exec.Command("bash", "-c", command).CombinedOutput()
		require.NoError(t, err, string(out))
		assert.GreaterOrEqual(t, time.Since(started), 2*time.Second, "%s: it cut the power at once: %s", name, out)
		written, err := os.ReadFile(trigger)
		require.NoError(t, err, name)
		assert.Equal(t, "o\n", string(written), name)
		break
	}
}

// The nodes run the latest stable kernel, 7.0 or later: Ceph's AES256K CephX keys (CVE-2025-30156) need it for the CSI
// clients (TestRookCephAES256K); NixOS's default LTS (6.18) has no AES256K. Back to an LTS once one has it
func TestMetalKernel(t *testing.T) {
	for name, node := range metalEval(t) {
		var major int
		_, err := fmt.Sscanf(node.Kernel, "%d.", &major)
		assert.NoError(t, err, name)
		assert.GreaterOrEqual(t, major, 7, "%s runs Linux %s", name, node.Kernel)
	}
}

// On a VM, virtio-net on Linux 7.3 hands flannel's VXLAN to the peer as UDP tunnel GSO, and the peer's guest refuses it
// ("bad gso", 100 lines a minute on every node): a VM turns that offload off; hardware never runs it
func TestMetalVMTunnelOffload(t *testing.T) {
	for name, node := range metalEval(t) {
		assert.Equal(t, "vm", node.TunnelOffload.Condition, name)
		assert.Contains(t, node.TunnelOffload.Script, "tx-udp_tnl-segmentation off", name)
		assert.Contains(t, node.TunnelOffload.Script, "tx-udp_tnl-csum-segmentation off", name)
		assert.Contains(t, node.TunnelOffload.WantedBy, "multi-user.target", name)
	}
}

// The rest of the disk is a partition rook-ceph takes as its OSD: labelled, never formatted
func TestMetalDisk(t *testing.T) {
	for name, node := range metalEval(t) {
		want := "/dev/nvme0n1"
		if name == "node-a" {
			want = "/dev/vda"
		}
		assert.Equal(t, want, node.Disk, name)
		assert.Equal(t, "rook-data", node.Data.Label, name)
		assert.Equal(t, "null", string(node.Data.Content), name)
	}
}

// The nodes resolve through the environment's network.dns (production: the Archer, whose DoH avoids the ISP modem's
// interception of UDP/53, step 12b-1)
func TestMetalDNS(t *testing.T) {
	for name, node := range metalEval(t) {
		assert.Equal(t, []string{"10.0.0.1"}, node.DNS, name)
	}
}

// Without config.json's disk and network.dns (the testbed) a node keeps /dev/sda and the public resolvers
func TestMetalDefaults(t *testing.T) {
	metalEval(t)
	dir := t.TempDir()
	cp := exec.Command("cp", "-r", filepath.Join(repo.Root(), metalFlake)+"/.", dir)
	out, err := cp.CombinedOutput()
	require.NoError(t, err, string(out))
	for name, body := range map[string]string{
		"hosts.json": `{"node-a": {"mac_address": "02:00:00:00:00:01", "ip": "10.0.0.11"}}`,
		"cluster.json": `{"init_host": "node-a", "vip": "10.0.0.10", "network": {"prefix_length": 24, "gateway": "10.0.0.1"},
			"service_vips": {"gateway": "10.0.0.2", "wireguard": "10.0.0.3"}, "domain": "example.test", "timezone": "Asia/Ho_Chi_Minh"}`,
		"authorized_keys": "ssh-ed25519 AAAAfixture fixture\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	got, err := exec.Command("nix", "eval", "--json", "--no-write-lock-file", "path:"+dir+"#nixosConfigurations.node-a.config",
		"--apply", `c: { disk = c.disko.devices.disk.main.device; dns = c.systemd.network.networks."10-uplink".dns; }`).Output()
	require.NoError(t, err)
	var node struct {
		Disk string   `json:"disk"`
		DNS  []string `json:"dns"`
	}
	require.NoError(t, json.Unmarshal(got, &node))
	assert.Equal(t, "/dev/sda", node.Disk)
	assert.Equal(t, []string{"8.8.8.8", "1.1.1.1"}, node.DNS)
}

// Production's config.json names what its nodes need (step 12b-1)
func TestProductionConfig(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repo.Root(), "infra", "production", "cluster", "config.json"))
	require.NoError(t, err)
	var config struct {
		InitHost string `json:"init_host"`
		Disk     string `json:"disk"`
		Network  struct {
			DNS []string `json:"dns"`
		} `json:"network"`
	}
	require.NoError(t, json.Unmarshal(raw, &config))
	assert.Equal(t, "homelab-1", config.InitHost)
	assert.Equal(t, "/dev/nvme0n1", config.Disk)
	assert.Equal(t, []string{"192.168.0.1"}, config.Network.DNS)
}

// The nodes' time zone is the environment's (config.json), like every chart's
func TestMetalTimeZone(t *testing.T) {
	for name, node := range metalEval(t) {
		assert.Equal(t, "Asia/Ho_Chi_Minh", node.TimeZone, name)
	}
}

// Each environment's nodes accept only its own keys (infra/<env>/metal/authorized_keys, written into the flake by
// the metal unit): a key that leaks from testbed, which runs often on the workstation, is not root on production
func TestMetalKeysPerEnvironment(t *testing.T) {
	ignored := exec.Command("git", "check-ignore", "-q", metalFlake+"/authorized_keys")
	ignored.Dir = repo.Root()
	assert.NoError(t, ignored.Run(), "the flake's authorized_keys is generated, not committed")
	keys := map[string][]string{}
	for _, env := range []string{"testbed", "production"} {
		raw, err := os.ReadFile(filepath.Join(repo.Root(), "infra", env, "metal", "authorized_keys"))
		require.NoError(t, err, env)
		for _, line := range strings.Split(string(raw), "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
				fields := strings.Fields(line)
				require.GreaterOrEqual(t, len(fields), 2, "%s: %q", env, line)
				keys[env] = append(keys[env], fields[1])
			}
		}
	}
	assert.NotEmpty(t, keys["testbed"], "testbed needs a key")
	for _, k := range keys["testbed"] {
		assert.NotContains(t, keys["production"], k, "a key in both environments")
	}
}

// nixie's installer runs from memory and writes the whole system to disk: on a VM it reclaims its page cache too, or
// three parallel installs grow each QEMU to the VM's size and WSL runs out (12a-2's first testbed-fresh)
func TestMetalInstallerReclaimsMemory(t *testing.T) {
	metalEval(t)
	dir := t.TempDir()
	cp := exec.Command("cp", "-r", filepath.Join(repo.Root(), metalFlake)+"/.", dir)
	out, err := cp.CombinedOutput()
	require.NoError(t, err, string(out))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "authorized_keys"), []byte("ssh-ed25519 AAAAfixture fixture\n"), 0o600))
	cmd := exec.Command("nix", "eval", "--raw", "--no-write-lock-file",
		"path:"+dir+"#nixosConfigurations.installer.config.systemd.services.memory-reclaim.unitConfig.ConditionVirtualization")
	got, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, "vm", string(got))
}

// metalExtraFlags evaluates node-a's k3s flags with extra keys in cluster.json
func metalExtraFlags(t *testing.T, extra string) []string {
	t.Helper()
	dir := t.TempDir()
	out, err := exec.Command("cp", "-r", filepath.Join(repo.Root(), metalFlake)+"/.", dir).CombinedOutput()
	require.NoError(t, err, string(out))
	files := map[string]string{
		"hosts.json":      `{"node-a": {"mac_address": "02:00:00:00:00:01", "ip": "10.0.0.11"}}`,
		"cluster.json":    `{"init_host": "node-a", "vip": "10.0.0.10", "network": {"prefix_length": 24, "gateway": "10.0.0.1"}, "service_vips": {"gateway": "10.0.0.2", "wireguard": "10.0.0.3"}, "domain": "example.test", "timezone": "Asia/Ho_Chi_Minh"` + extra + `}`,
		"authorized_keys": "ssh-ed25519 AAAAfixture fixture\n",
	}
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	got, err := exec.Command("nix", "eval", "--json", "--no-write-lock-file", "path:"+dir+"#nixosConfigurations.node-a.config.services.k3s.extraFlags").Output()
	require.NoError(t, err)
	var flags []string
	require.NoError(t, json.Unmarshal(got, &flags))
	return flags
}

// A control plane on slow machines (testbed's VMs inside WSL's Hyper-V, config.json's slow_control_plane) tolerates a
// starved etcd leader: with the defaults k3s lost its leases and exited 18 times in 35 minutes of a first sync, and
// every restart made it worse. Production keeps the defaults, and so a fast failover
func TestMetalSlowControlPlane(t *testing.T) {
	flags := metalExtraFlags(t, `, "slow_control_plane": true`)
	for _, f := range []string{
		"--etcd-arg=heartbeat-interval=500", "--etcd-arg=election-timeout=5000",
		"--kube-controller-manager-arg=leader-elect-lease-duration=60s", "--kube-controller-manager-arg=leader-elect-renew-deadline=40s",
		"--kube-scheduler-arg=leader-elect-lease-duration=60s", "--kube-scheduler-arg=leader-elect-renew-deadline=40s",
		"--kube-cloud-controller-manager-arg=leader-elect-lease-duration=60s", "--kube-cloud-controller-manager-arg=leader-elect-renew-deadline=40s",
	} {
		assert.Contains(t, flags, f)
	}
	assert.NotContains(t, strings.Join(metalExtraFlags(t, ""), " "), "heartbeat-interval")
	for env, want := range map[string]bool{"testbed": true, "production": false} {
		raw, err := os.ReadFile(filepath.Join(repo.Root(), "infra", env, "cluster", "config.json"))
		require.NoError(t, err)
		var config struct {
			Slow bool `json:"slow_control_plane"`
		}
		require.NoError(t, json.Unmarshal(raw, &config))
		assert.Equal(t, want, config.Slow, env)
	}
}

// Production's nodes (step 12b-1): nixie records each install in the committed hosts.json (upstream's way), so a
// recorded machine is never installed again from any checkout; the disk comes from config.json, never hosts.json,
// whose unknown fields nixie drops
func TestProductionMetalUnit(t *testing.T) {
	dir := filepath.Join(repo.Root(), "infra", "production", "metal")
	raw, err := os.ReadFile(filepath.Join(dir, "hosts.json"))
	require.NoError(t, err)
	var hosts map[string]map[string]string
	require.NoError(t, json.Unmarshal(raw, &hosts))
	want := map[string][2]string{
		"homelab-1": {"192.168.0.11", "38:22:e2:2c:dc:35"},
		"homelab-2": {"192.168.0.12", "10:62:e5:16:d2:ca"},
		"homelab-3": {"192.168.0.13", "10:e7:c6:0f:63:a0"},
	}
	require.Len(t, hosts, len(want))
	for name, w := range want {
		require.Contains(t, hosts, name)
		assert.Equal(t, w[0], hosts[name]["ip"], name)
		assert.Equal(t, w[1], hosts[name]["mac_address"], name)
		assert.NotContains(t, hosts[name], "disk", "%s: the disk goes in config.json", name)
	}

	unit, err := os.ReadFile(filepath.Join(dir, "terragrunt.hcl"))
	require.NoError(t, err)
	assert.Contains(t, string(unit), `"${get_repo_root()}/scripts/metal-nixie", "${get_terragrunt_dir()}/hosts.json"`,
		"nixie gets the committed hosts.json itself, never a copy")
	assert.NotContains(t, string(unit), "testbed-nixie")
	assert.NotContains(t, string(unit), `"--environment", "testbed"`)
	assert.Equal(t, 3, strings.Count(string(unit), `"--environment", "production"`), "enroll, kubeconfig and upgrade")

	argocd, err := os.ReadFile(filepath.Join(repo.Root(), "infra", "production", "argocd", "terragrunt.hcl"))
	require.NoError(t, err)
	assert.Regexp(t, `dependencies \{\s*paths = \["\.\./metal"\]`, string(argocd), "the bootstrap waits for the nodes")
}

// testbed-trust, which every just infra deploy production runs, names ssh's own error when it gives up: a changed host
// key after a reinstall or a key missing from the agent otherwise read as a dead node after 5 minutes (12b-1's review)
func TestTrustNamesSSHError(t *testing.T) {
	hosts := filepath.Join(t.TempDir(), "hosts.json")
	require.NoError(t, os.WriteFile(hosts, []byte(`{"a": {"ip": "127.0.0.1", "mac_address": "02:00:00:00:00:01"}}`), 0o600))
	cmd := exec.Command(filepath.Join(repo.Root(), "scripts", "testbed-trust"), hosts)
	cmd.Env = append(os.Environ(), "TESTBED_TRUST_WAIT=1", "TESTBED_TRUST_INTERVAL=1")
	out, err := cmd.CombinedOutput()
	require.Error(t, err, string(out))
	assert.Contains(t, string(out), "no SSH from 127.0.0.1")
	assert.Regexp(t, `ssh said: \S`, string(out), "the last ssh error is shown")
}

// node-fio writes to the raw rook-data partition: its guard refuses anything that is not plainly empty, so it can never
// destroy a Ceph OSD once 12b-2 gives the disk to Ceph (12b-1's review). Checked on files standing in for the partition
func TestNodeFioGuard(t *testing.T) {
	dir := t.TempDir()
	run := func(data, rook string) (string, error) {
		cmd := exec.Command("sh", filepath.Join(repo.Root(), "scripts", "node-fio-remote"))
		cmd.Env = append(os.Environ(), "NODE_FIO_GUARD_ONLY=1", "NODE_FIO_DATA="+data, "NODE_FIO_ROOK="+rook)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	empty := filepath.Join(dir, "empty")
	require.NoError(t, os.WriteFile(empty, make([]byte, 1<<20), 0o600))
	osd := filepath.Join(dir, "osd")
	require.NoError(t, os.WriteFile(osd, append([]byte("bluestore block device\n"), make([]byte, 1<<20)...), 0o600))
	rook := filepath.Join(dir, "rook")
	require.NoError(t, os.Mkdir(rook, 0o700))
	none := filepath.Join(dir, "no-rook")

	out, err := run(empty, none)
	require.NoError(t, err, out)
	assert.Contains(t, out, "is empty")
	out, err = run(osd, none)
	assert.Error(t, err, out)
	assert.Contains(t, out, "refusing", "a Ceph OSD's partition")
	out, err = run(empty, rook)
	assert.Error(t, err, out)
	assert.Contains(t, out, "Rook has state")
	out, err = run(filepath.Join(dir, "missing"), none)
	assert.Error(t, err, out)
}

// Nothing but lab cluster upgrade reboots a node: NixOS nodes change only through it, and it drains and reboots on a
// kernel change. kured, which upstream ran for distributions that patch themselves, would reboot NixOS nodes in turn
// forever: its sentinel (needs-restarting) does not exist there (12b-2)
func TestNoRebootDaemon(t *testing.T) {
	tracked, err := exec.Command("git", "-C", repo.Root(), "ls-files", "system/kured").Output()
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(tracked)), "system/kured deploys a reboot daemon")
	out, _ := exec.Command("git", "-C", repo.Root(), "grep", "-l", "needs-restarting", "--", "system", "platform", "apps").Output()
	assert.Empty(t, strings.TrimSpace(string(out)), "a reboot sentinel for another distribution")
}

// containerd pulls our images from zot by name (docs/how-to-guides/app-delivery.md): until LAN DNS, every node resolves
// registry.<domain> to the LAN Gateway's VIP itself
func TestMetalRegistryHost(t *testing.T) {
	for name, node := range metalEval(t) {
		assert.Contains(t, node.Hosts["10.0.0.2"], "registry.example.test", name)
	}
}

// istio-cni (native nftables) finds the kubelet by the comm of the process hosting it, and Istio 1.32 matches only
// "k3s", while host-installed k3s runs as "k3s-server": a process named k3s, owned by root like k3s, with nothing else
// (docs/superpowers/specs/2026-11-25-istio-cni-k3s-name-design.md)
func TestMetalK3sNameForIstio(t *testing.T) {
	for name, n := range metalEval(t) {
		require.NotEmpty(t, n.K3sName.Exec, name)
		assert.Regexp(t, `^\S+/bin/k3s -c `, n.K3sName.Exec, "%s: the executable is named k3s", name)
		// bash stays the parent (a lone command would be exec'd and renamed) and waits without spinning: a loop around a
		// failing sleep would burn a CPU; if sleep ends, the unit ends and systemd starts it again after a pause
		assert.Regexp(t, `/bin/sleep infinity & wait'$`, n.K3sName.Exec, name)
		assert.NotContains(t, n.K3sName.Exec, "while", name)
		assert.Equal(t, "5", n.K3sName.RestartSec, name)
		// root, as k3s itself: istio-cni takes this process's UID for the kubelet's, and another would mark the probes
		// wrongly without failing
		assert.Equal(t, "root", n.K3sName.User, name)
		assert.Equal(t, "", n.K3sName.Caps, name)
		assert.Equal(t, true, n.K3sName.Network, name)
		assert.Equal(t, "always", n.K3sName.Restart, name)
		assert.Contains(t, n.K3sName.Before, "k3s.service", name)
		assert.Contains(t, n.K3sName.WantedBy, "multi-user.target", name)
	}
}

// The stopgap is tracked until it goes: while the node flake runs k3s-name-for-istio, docs/superpowers/deferred-minors.md
// says what removes it (the final review, 2026-11-25)
func TestMetalK3sNameForIstioTracked(t *testing.T) {
	profile, err := os.ReadFile(filepath.Join(repo.Root(), metalFlake, "profiles", "k3s-server.nix"))
	require.NoError(t, err)
	if !strings.Contains(string(profile), "k3s-name-for-istio") {
		return
	}
	minors, err := os.ReadFile(filepath.Join(repo.Root(), "docs", "superpowers", "deferred-minors.md"))
	require.NoError(t, err)
	assert.Regexp(t, "(?m)^\\|.*k3s-name-for-istio.*k3s-server.*\\|", string(minors), "the stopgap's removal is tracked")
}

// G2 (docs/superpowers/specs/2026-11-24-node-ports-design.md): the cluster's own ports from the nodes only (the kubelet
// also from the pods, where Prometheus scrapes it); the rest of the LAN keeps SSH, 6443, 80 and 443
func TestMetalClusterPortsFromNodes(t *testing.T) {
	for name, n := range metalEval(t) {
		// 22: OpenSSH opens it itself (openFirewall), by key only (TestMetalSSHKeysOnly)
		assert.ElementsMatch(t, []int{22, 6443, 80, 443}, n.Firewall.TCP, name)
		assert.Empty(t, n.Firewall.UDP, name)
		for _, ip := range []string{"10.0.0.11", "10.0.0.12", "10.0.0.13"} {
			assert.Regexp(t, `-s `+regexp.QuoteMeta(ip)+` -p tcp -m multiport --dports [0-9,]*\b2379,2380,10250\b`, n.Firewall.Extra, name)
			assert.Contains(t, n.Firewall.Extra, "-s "+ip+" -p udp --dport 51820 -j nixos-fw-accept", name)
		}
		pods := regexp.MustCompile(`-s 10\.42\.0\.0/16 -p tcp -m multiport --dports (\S+) `).FindStringSubmatch(n.Firewall.Extra)
		require.NotNil(t, pods, name)
		assert.Contains(t, strings.Split(pods[1], ","), "10250", name)
		assert.NotContains(t, strings.Split(pods[1], ","), "2379", "pods never reach etcd: %s", name)
		assert.NotContains(t, n.Firewall.Extra, "10.42.0.0/16 -p udp", name)
		assert.NotContains(t, n.Firewall.Extra, "8472", "%s: VXLAN is gone (G12)", name)
		// Every accept in the extra rules names an allowed source: a line opening a cluster port, or any port, to
		// everyone would pass the checks above (the G2 review)
		allowed := map[string]bool{"10.0.0.11": true, "10.0.0.12": true, "10.0.0.13": true, "10.42.0.0/16": true}
		for _, line := range strings.Split(n.Firewall.Extra, "\n") {
			if !strings.Contains(line, "-j nixos-fw-accept") && !strings.Contains(line, "-j ACCEPT") {
				continue
			}
			source := regexp.MustCompile(`-s (\S+)`).FindStringSubmatch(line)
			if assert.NotNil(t, source, "%s: an accept without a source: %s", name, line) {
				assert.True(t, allowed[source[1]], "%s: an accept from %s: %s", name, source[1], line)
			}
		}
	}
}

// SSH signs in by key only, said explicitly: today only the absence of passwords stops a password sign-in
func TestMetalSSHKeysOnly(t *testing.T) {
	for name, n := range metalEval(t) {
		assert.Equal(t, false, n.SSH["PasswordAuthentication"], name)
		assert.Equal(t, false, n.SSH["KbdInteractiveAuthentication"], name)
		assert.Equal(t, "prohibit-password", n.SSH["PermitRootLogin"], name)
	}
}

// No IPv6 on the uplink: nothing in the cluster uses it, and a global address would stand behind the router's firewall
// alone (with no IPv6 configured, networkd sets disable_ipv6 on the link)
func TestMetalNoIPv6(t *testing.T) {
	for name, n := range metalEval(t) {
		assert.Equal(t, "no", n.Uplink["DHCP"], name)
		assert.Equal(t, false, n.Uplink["IPv6AcceptRA"], name)
		assert.Equal(t, "no", n.Uplink["LinkLocalAddressing"], name)
	}
}

// A node joins only once the others accept its address (G2): the installed nodes take the flake's changes (lab cluster
// upgrade, which skips a node just installed from the same flake) before enroll waits for the new one to join etcd
func TestMetalUpgradesBeforeEnroll(t *testing.T) {
	for _, env := range []string{"testbed", "production"} {
		unit, err := os.ReadFile(filepath.Join(repo.Root(), "infra", env, "metal", "terragrunt.hcl"))
		require.NoError(t, err)
		order := map[string]int{}
		for _, hook := range []string{"nixie", "trust", "upgrade", "enroll"} {
			order[hook] = strings.Index(string(unit), `after_hook "`+hook+`"`)
			require.GreaterOrEqual(t, order[hook], 0, "%s: hook %s", env, hook)
		}
		assert.Less(t, order["trust"], order["upgrade"], "%s: the host keys before upgrade's SSH", env)
		assert.Less(t, order["upgrade"], order["enroll"], "%s: the others accept the new node before it joins", env)
		if env == "testbed" {
			leftovers := strings.Index(string(unit), `after_hook "leftovers"`)
			assert.Less(t, leftovers, order["upgrade"], "nothing outside the testbed's list runs while the nodes upgrade")
		}
	}
}

// G12 (docs/superpowers/specs/2026-11-26-lan-encryption-design.md): the nodes carry pod traffic through flannel's
// WireGuard, which encrypts it and authenticates each packet with the sending node's key; the one-node sandbox keeps
// k3s's default VXLAN, with nothing crossing between nodes
func TestMetalFlannelWireGuard(t *testing.T) {
	for name, n := range metalEval(t) {
		assert.Contains(t, n.K3s.ExtraFlags, "--flannel-backend=wireguard-native", name)
		assert.Contains(t, n.Packages, "wireguard-tools", "%s: NodeWireGuard reads the peers with wg", name)
	}
	k3d, err := os.ReadFile(filepath.Join(repo.Root(), "infra", "_modules", "k3d", "k3d.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(k3d), "flannel-backend", "the sandbox keeps VXLAN")
}

// Each node publishes how old the nixpkgs it was built from is (G11, docs/superpowers/specs/2026-11-27-node-patches-design.md):
// the locked inputs' lastModified, for node-exporter's textfile collector, copied out of /etc at activation since
// node-exporter reads the host's root under /host/root, where a link into /nix/store resolves inside its own container
func TestMetalNixpkgsAge(t *testing.T) {
	lock := filepath.Join(repo.Root(), metalFlake, "flake.lock")
	modified := func(input string) string {
		out, err := exec.Command("jq", "-r", ".nodes[.nodes.root.inputs[\""+input+"\"]].locked.lastModified", lock).CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	for name, node := range metalEval(t) {
		m := node.NodeMetrics
		assert.Contains(t, m.Nixpkgs, "# TYPE node_nixpkgs_last_modified_seconds gauge\n", name)
		assert.Contains(t, m.Nixpkgs, `node_nixpkgs_last_modified_seconds{input="nixpkgs"} `+modified("nixpkgs")+"\n", name)
		assert.Contains(t, m.Nixpkgs, `node_nixpkgs_last_modified_seconds{input="nixpkgs-unstable"} `+modified("nixpkgs-unstable")+"\n", name)
		assert.Contains(t, m.NixpkgsCopy, "install -D -m 0644 /etc/node-exporter/nixpkgs.prom /var/lib/node-exporter/nixpkgs.prom", name)
		assert.Contains(t, m.NixpkgsCopyOn, "etc", "%s: copies after /etc is in place", name)
	}
}

// A weekly store check on each node, at idle I/O (it reads the whole store, on etcd's disk), each node on its own hour
// so a power cut's catch-up (Persistent) does not load every disk at once
func TestMetalStoreVerify(t *testing.T) {
	calendars := map[string]bool{}
	for name, node := range metalEval(t) {
		v := node.NodeMetrics.Verify
		assert.Equal(t, "oneshot", v.Type, name)
		assert.Equal(t, "idle", v.IOClass, name)
		assert.Equal(t, 19, v.Nice, name)
		assert.Contains(t, v.Exec, "nix-store-verify", name)
		assert.Regexp(t, `^Sun \*-\*-\* \d\d:30:00$`, v.Calendar, name)
		calendars[v.Calendar] = true
		assert.True(t, v.Persist, name)
		assert.Equal(t, "1h", v.Delay, name)
		assert.Contains(t, v.WantedBy, "timers.target", name)
	}
	assert.Len(t, calendars, 3, "each node on its own hour: %v", calendars)
}
