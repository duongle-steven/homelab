package test

import (
	"encoding/json"
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
		for name, body := range map[string]string{"hosts.json": hosts, "cluster.json": cluster} {
			if metalErr = os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); metalErr != nil {
				return
			}
		}
		apply := `cs: builtins.mapAttrs (n: c: let k = c.config.services.k3s; m = k.manifests; in {
			k3s = { inherit (k) clusterInit serverAddr extraFlags disable; version = k.package.version; };
			manifests = builtins.attrNames m;
			kubeVip = if m ? kube-vip then m.kube-vip.content else [];
			zram = c.config.zramSwap.enable;
			data = { inherit (c.config.disko.devices.disk.main.content.partitions.data) label content; };
			disk = c.config.disko.devices.disk.main.device;
			timeZone = c.config.time.timeZone;
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
