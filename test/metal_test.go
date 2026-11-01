package test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
	Disk     string `json:"disk"`
	TimeZone string `json:"timeZone"`
	Kernel   string `json:"kernel"`
	Reclaim  struct {
		Condition string `json:"condition"`
		Script    string `json:"script"`
		Timer     string `json:"timer"`
		Start     string `json:"start"`
	} `json:"reclaim"`
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
		cp := exec.Command("cp", "-r", filepath.Join("..", metalFlake)+"/.", dir)
		if out, err := cp.CombinedOutput(); err != nil {
			metalErr = &os.PathError{Op: "cp", Path: string(out), Err: err}
			return
		}
		hosts := `{"node-a": {"mac_address": "02:00:00:00:00:01", "ip": "10.0.0.11", "disk": "/dev/vda"},
			"node-b": {"mac_address": "02:00:00:00:00:02", "ip": "10.0.0.12", "disk": "/dev/vda"},
			"node-c": {"mac_address": "02:00:00:00:00:03", "ip": "10.0.0.13", "disk": "/dev/vda"}}`
		cluster := `{"init_host": "node-a", "vip": "` + metalFixtureVIP + `", "network": {"prefix_length": 24, "gateway": "10.0.0.1"},
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
			timeZone = c.config.time.timeZone;
			kernel = c.config.boot.kernelPackages.kernel.version;
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
		assert.ElementsMatch(t, []string{"traefik", "local-storage", "gateway-api-crd"}, k.Disable, name)
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

// kube-vip holds the API's VIP and serves only Services of its class (the Gateway's and WireGuard's addresses);
// ServiceLB serves the rest, so no cloud controller hands out addresses
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

// zram lets a 3.5 GB staging node keep more in memory
func TestMetalZram(t *testing.T) {
	for name, node := range metalEval(t) {
		assert.True(t, node.Zram, name)
	}
}

// A VM hands back only the pages its kernel frees, never its page cache: on a VM (staging) a timer reclaims cache
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

// The rest of the disk is a partition rook-ceph takes as its OSD: labelled, never formatted
func TestMetalDisk(t *testing.T) {
	for name, node := range metalEval(t) {
		assert.Equal(t, "/dev/vda", node.Disk, name)
		assert.Equal(t, "rook-data", node.Data.Label, name)
		assert.Equal(t, "null", string(node.Data.Content), name)
	}
}

// The nodes' time zone is the environment's (config.json), like every chart's
func TestMetalTimeZone(t *testing.T) {
	for name, node := range metalEval(t) {
		assert.Equal(t, "Asia/Ho_Chi_Minh", node.TimeZone, name)
	}
}

// Each environment's nodes accept only its own keys (infra/<env>/metal/authorized_keys, written into the flake by
// the metal unit): a key that leaks from staging, which runs often on the workstation, is not root on production
func TestMetalKeysPerEnvironment(t *testing.T) {
	ignored := exec.Command("git", "check-ignore", "-q", metalFlake+"/authorized_keys")
	ignored.Dir = ".."
	assert.NoError(t, ignored.Run(), "the flake's authorized_keys is generated, not committed")
	keys := map[string][]string{}
	for _, env := range []string{"staging", "production"} {
		raw, err := os.ReadFile(filepath.Join("..", "infra", env, "metal", "authorized_keys"))
		require.NoError(t, err, env)
		for _, line := range strings.Split(string(raw), "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
				fields := strings.Fields(line)
				require.GreaterOrEqual(t, len(fields), 2, "%s: %q", env, line)
				keys[env] = append(keys[env], fields[1])
			}
		}
	}
	assert.NotEmpty(t, keys["staging"], "staging needs a key")
	for _, k := range keys["staging"] {
		assert.NotContains(t, keys["production"], k, "a key in both environments")
	}
}

// nixie's installer runs from memory and writes the whole system to disk: on a VM it reclaims its page cache too, or
// three parallel installs grow each QEMU to the VM's size and WSL runs out (12a-2's first staging-fresh)
func TestMetalInstallerReclaimsMemory(t *testing.T) {
	metalEval(t)
	dir := t.TempDir()
	cp := exec.Command("cp", "-r", filepath.Join("..", metalFlake)+"/.", dir)
	out, err := cp.CombinedOutput()
	require.NoError(t, err, string(out))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "authorized_keys"), []byte("ssh-ed25519 AAAAfixture fixture\n"), 0o600))
	cmd := exec.Command("nix", "eval", "--raw", "--no-write-lock-file",
		"path:"+dir+"#nixosConfigurations.installer.config.systemd.services.memory-reclaim.unitConfig.ConditionVirtualization")
	got, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, "vm", string(got))
}
