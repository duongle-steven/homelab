package test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Staging's tests run only against staging (make staging: ENV=staging), its three NixOS VMs
func stagingOnly(t *testing.T) {
	if os.Getenv("ENV") != "staging" {
		t.Skip("staging tests run against staging only (make staging)")
	}
}

// stagingHosts reads the VMs' names and addresses (infra/staging/metal/hosts.json), sorted by name
func stagingHosts(t *testing.T) (names []string, ips map[string]string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "infra", "staging", "metal", "hosts.json"))
	require.NoError(t, err)
	var hosts map[string]struct {
		IP string `json:"ip"`
	}
	require.NoError(t, json.Unmarshal(raw, &hosts))
	ips = map[string]string{}
	for name, host := range hosts {
		names = append(names, name)
		ips[name] = host.IP
	}
	sort.Strings(names)
	return names, ips
}

func stagingSSH(t *testing.T, ip, command string) (string, error) {
	t.Helper()
	out, err := exec.Command("ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "root@"+ip, command).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func virsh(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("virsh", append([]string{"-c", "qemu:///system"}, args...)...).CombinedOutput()
	require.NoError(t, err, string(out))
	return strings.TrimSpace(string(out))
}

func stagingKubectl(namespace string) *k8s.KubectlOptions {
	options := k8s.NewKubectlOptions("", "", namespace)
	options.Logger = logger.Discard
	return options
}

// waitNodeReady waits until node reports Ready
func waitNodeReady(t *testing.T, node string, wait time.Duration) {
	t.Helper()
	require.Eventually(t, func() bool {
		out, err := k8s.RunKubectlAndGetOutputE(t, stagingKubectl(""), "get", "node", node,
			"-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
		return err == nil && out == "True"
	}, wait, 10*time.Second, "%s is not Ready", node)
}

// Three k3s servers, each an etcd member that sees a leader
func TestStagingNodes(t *testing.T) {
	stagingOnly(t)
	names, ips := stagingHosts(t)
	require.Len(t, names, 3)
	for _, name := range names {
		waitNodeReady(t, name, 5*time.Minute)
		roles, err := k8s.RunKubectlAndGetOutputE(t, stagingKubectl(""), "get", "node", name, "-o", "jsonpath={.metadata.labels}")
		require.NoError(t, err)
		assert.Contains(t, roles, `"node-role.kubernetes.io/control-plane":"true"`, name)
		assert.Contains(t, roles, `"node-role.kubernetes.io/etcd":"true"`, name)
		metrics, err := stagingSSH(t, ips[name], "curl -s http://127.0.0.1:2381/metrics | grep '^etcd_server_has_leader '")
		require.NoError(t, err, metrics)
		assert.Equal(t, "etcd_server_has_leader 1", metrics, name)
	}
}

// The API's VIP moves: with the node holding it shut down, the API still answers through the VIP from another node
func TestStagingVIPMoves(t *testing.T) {
	stagingOnly(t)
	holder, err := k8s.RunKubectlAndGetOutputE(t, stagingKubectl("kube-system"), "get", "lease", "plndr-cp-lock",
		"-o", "jsonpath={.spec.holderIdentity}")
	require.NoError(t, err)
	require.NotEmpty(t, holder, "no kube-vip leader for the control plane")
	bootID := func() string {
		id, _ := k8s.RunKubectlAndGetOutputE(t, stagingKubectl(""), "get", "node", holder, "-o", "jsonpath={.status.nodeInfo.bootID}")
		return id
	}
	booted := bootID()
	t.Logf("%s holds the VIP: shutting it down", holder)
	virsh(t, "shutdown", holder)
	defer func() {
		require.Eventually(t, func() bool { return virsh(t, "domstate", holder) == "shut off" }, 3*time.Minute, 5*time.Second)
		virsh(t, "start", holder)
		// The API server reports the node's last status until its kubelet reports again: wait for the new boot
		require.Eventually(t, func() bool { id := bootID(); return id != "" && id != booted }, 10*time.Minute, 10*time.Second,
			"%s did not come back", holder)
		waitNodeReady(t, holder, 10*time.Minute)
	}()
	require.Eventually(t, func() bool { return virsh(t, "domstate", holder) == "shut off" }, 3*time.Minute, 5*time.Second,
		"%s did not shut down", holder)
	start := time.Now()
	assert.Eventually(t, func() bool {
		out, err := k8s.RunKubectlAndGetOutputE(t, stagingKubectl(""), "get", "--raw", "/readyz")
		return err == nil && out == "ok"
	}, 2*time.Minute, 5*time.Second, "the API did not answer through the VIP without %s", holder)
	t.Logf("the API answered through the VIP %s after %s's shutdown", time.Since(start).Round(time.Second), holder)
	next, err := k8s.RunKubectlAndGetOutputE(t, stagingKubectl("kube-system"), "get", "lease", "plndr-cp-lock",
		"-o", "jsonpath={.spec.holderIdentity}")
	require.NoError(t, err)
	assert.NotEqual(t, holder, next, "the VIP did not move")
}

// Ceph on three nodes: healthy, an OSD on each node, and a volume written on one node read back from another
func TestStagingCeph(t *testing.T) {
	stagingOnly(t)
	ceph := stagingKubectl("rook-ceph")
	require.Eventually(t, func() bool {
		health, err := k8s.RunKubectlAndGetOutputE(t, ceph, "get", "cephcluster", "-o", "jsonpath={.items[*].status.ceph.health}")
		return err == nil && health == "HEALTH_OK"
	}, 20*time.Minute, 15*time.Second, "Ceph is not HEALTH_OK")
	osds, err := k8s.RunKubectlAndGetOutputE(t, ceph, "get", "pods", "-l", "app=rook-ceph-osd",
		"-o", `jsonpath={range .items[*]}{.spec.nodeName}{" "}{.status.phase}{"\n"}{end}`)
	require.NoError(t, err)
	names, _ := stagingHosts(t)
	for _, name := range names {
		assert.Contains(t, osds, name+" Running", "no running OSD on %s", name)
	}

	ns := fmt.Sprintf("staging-ceph-%d", time.Now().Unix())
	options := stagingKubectl(ns)
	k8s.CreateNamespace(t, options, ns)
	defer k8s.DeleteNamespace(t, options, ns)
	pod := func(name, node, command string) string {
		return fmt.Sprintf(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":%q},"spec":{"nodeName":%q,"restartPolicy":"Never",
			"containers":[{"name":"c","image":"busybox:1.37.0","command":["sh","-c",%q],"volumeMounts":[{"name":"v","mountPath":"/data"}]}],
			"volumes":[{"name":"v","persistentVolumeClaim":{"claimName":"data"}}]}}`, name, node, command)
	}
	claim := `{"apiVersion":"v1","kind":"PersistentVolumeClaim","metadata":{"name":"data"},
		"spec":{"accessModes":["ReadWriteOnce"],"storageClassName":"standard-rwo","resources":{"requests":{"storage":"1Gi"}}}}`
	k8s.KubectlApplyFromString(t, options, claim)
	k8s.KubectlApplyFromString(t, options, pod("writer", names[0], "echo staging-ceph > /data/probe && sync"))
	waitPodSucceeded(t, options, "writer")
	k8s.RunKubectl(t, options, "delete", "pod", "writer", "--wait")
	k8s.KubectlApplyFromString(t, options, pod("reader", names[1], "cat /data/probe"))
	waitPodSucceeded(t, options, "reader")
	logs, err := k8s.RunKubectlAndGetOutputE(t, options, "logs", "reader")
	require.NoError(t, err)
	assert.Equal(t, "staging-ceph", logs, "written on %s, read on %s", names[0], names[1])
}

func waitPodSucceeded(t *testing.T, options *k8s.KubectlOptions, name string) {
	t.Helper()
	require.Eventually(t, func() bool {
		phase, err := k8s.RunKubectlAndGetOutputE(t, options, "get", "pod", name, "-o", "jsonpath={.status.phase}")
		require.NotEqual(t, "Failed", phase, "pod %s failed", name)
		return err == nil && phase == "Succeeded"
	}, 5*time.Minute, 5*time.Second, "pod %s did not finish", name)
}
