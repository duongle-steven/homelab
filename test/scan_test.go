package test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/clusterscan"
	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/stretchr/testify/require"
)

const (
	scanBenchNamespace = "scan-bench"
	scanBenchImage     = "docker.io/aquasec/kube-bench:v0.16.0@sha256:75506f222d1eb6ce2a751a5533bdc0a3b54c898e2e49e7751d0ee22cfb862679"
)

// scanBenchJob runs kube-bench on one node. --targets because it detects no control plane on k3s and skips three
// sections otherwise; the node's /nix on PATH because its image has no journalctl, which every k3s check calls
func scanBenchJob(node string) string {
	mounts := map[string]string{"rancher": "/var/lib/rancher", "etc-rancher": "/etc/rancher", "journal": "/var/log/journal", "machine-id": "/etc/machine-id",
		"systemd": "/etc/systemd", "nix": "/nix", "system": "/run/current-system", "cni": "/var/lib/cni", "kubelet": "/var/lib/kubelet"}
	names := []string{}
	for name := range mounts {
		names = append(names, name)
	}
	slices.Sort(names)
	var volumeMounts, volumes strings.Builder
	for _, name := range names {
		fmt.Fprintf(&volumeMounts, "        - {name: %s, mountPath: %s, readOnly: true}\n", name, mounts[name])
		fmt.Fprintf(&volumes, "      - {name: %s, hostPath: {path: %s}}\n", name, mounts[name])
	}
	return fmt.Sprintf(`apiVersion: batch/v1
kind: Job
metadata: {name: bench-%[1]s, namespace: %[2]s}
spec:
  backoffLimit: 0
  template:
    spec:
      nodeName: %[1]s
      hostPID: true
      restartPolicy: Never
      containers:
      - name: kube-bench
        image: %[3]s
        command: [kube-bench, run, --benchmark, k3s-cis-1.11, --targets, "master,etcd,controlplane,node,policies", --json]
        securityContext: {privileged: true}
        env:
        - {name: PATH, value: "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/run/current-system/sw/bin"}
        volumeMounts:
        - {name: cfg, mountPath: /opt/kube-bench/cfg}
%[4]s      volumes:
      - name: cfg
        configMap:
          name: kube-bench-cfg
          items:
          - {key: config.yaml, path: config.yaml}
          - {key: benchmark-config.yaml, path: k3s-cis-1.11/config.yaml}
          - {key: controlplane.yaml, path: k3s-cis-1.11/controlplane.yaml}
          - {key: etcd.yaml, path: k3s-cis-1.11/etcd.yaml}
          - {key: master.yaml, path: k3s-cis-1.11/master.yaml}
          - {key: node.yaml, path: k3s-cis-1.11/node.yaml}
          - {key: policies.yaml, path: k3s-cis-1.11/policies.yaml}
%[5]s`, node, scanBenchNamespace, scanBenchImage, volumeMounts.String(), volumes.String())
}

// TestScanBench runs kube-bench on every testbed node (just testbed-scan). A run stopped midway leaves the privileged
// namespace behind: the next one deletes it first
func TestScanBench(t *testing.T) {
	testbedOnly(t)
	raw, err := os.ReadFile(filepath.Join("..", ".cluster-scan-accepted.yaml"))
	require.NoError(t, err)
	accepted, err := clusterscan.LoadScanAccepted(raw)
	require.NoError(t, err)

	cluster, namespaced := testbedKubectl(""), testbedKubectl(scanBenchNamespace)
	remove := func() {
		if err := k8s.RunKubectlE(t, cluster, "delete", "namespace", scanBenchNamespace, "--ignore-not-found", "--timeout=5m"); err != nil {
			t.Errorf("the privileged namespace %s may be left behind: delete it", scanBenchNamespace)
		}
	}
	remove()
	t.Cleanup(remove)
	require.NoError(t, k8s.KubectlApplyFromStringE(t, cluster, fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %s
  labels: {pod-security.kubernetes.io/enforce: privileged}
`, scanBenchNamespace)))

	cfg := filepath.Join("kube-bench-cfg", "k3s-cis-1.11")
	args := []string{"create", "configmap", "kube-bench-cfg", "--from-file=config.yaml=" + filepath.Join("kube-bench-cfg", "config.yaml"),
		"--from-file=benchmark-config.yaml=" + filepath.Join(cfg, "config.yaml")}
	for _, name := range []string{"controlplane", "etcd", "master", "node", "policies"} {
		args = append(args, "--from-file="+filepath.Join(cfg, name+".yaml"))
	}
	require.NoError(t, k8s.RunKubectlE(t, namespaced, args...))

	nodes, _ := testbedHosts(t)
	for _, node := range nodes {
		require.NoError(t, k8s.KubectlApplyFromStringE(t, namespaced, scanBenchJob(node)))
	}
	for _, node := range nodes {
		job := "job/bench-" + node
		_ = k8s.RunKubectlE(t, namespaced, "wait", job, "--for=jsonpath={.status.conditions[?(@.status==\"True\")].type}=Complete", "--timeout=5m")
		if done, err := k8s.RunKubectlAndGetOutputE(t, namespaced, "get", job, "-o", "jsonpath={.status.succeeded}"); err != nil || done != "1" {
			t.Errorf("%s: kube-bench did not complete", node)
			continue
		}
		report, err := k8s.RunKubectlAndGetOutputE(t, namespaced, "logs", job)
		if err != nil {
			t.Errorf("%s: its report could not be read", node)
			continue
		}
		undecided, err := clusterscan.UndecidedBench([]byte(report), accepted)
		if err != nil {
			t.Errorf("%s: %v", node, err)
			continue
		}
		if len(undecided) > 0 {
			t.Errorf("%s: undecided, fix or accept in .cluster-scan-accepted.yaml: %s", node, strings.Join(undecided, " "))
		}
	}
}

// TestScanKubescape runs kubescape on the sandbox (just dev-scan). --keep-local: nothing leaves the workstation. Its
// exit code stays 0 on findings, as no --compliance-threshold is set, so a non-zero one means the scan did not run
func TestScanKubescape(t *testing.T) {
	if os.Getenv("ENV") != "dev" {
		t.Skip("kubescape runs against the sandbox only (just dev-scan)")
	}
	raw, err := os.ReadFile(filepath.Join("..", ".cluster-scan-accepted.yaml"))
	require.NoError(t, err)
	accepted, err := clusterscan.LoadScanAccepted(raw)
	require.NoError(t, err)

	file := filepath.Join(t.TempDir(), "report.json")
	out, err := exec.Command("kubescape", "scan", "framework", "allcontrols", "--keep-local", "--format", "json", "--output", file).CombinedOutput()
	require.NoError(t, err, "kubescape failed: its last lines: %s", scanTail(out))
	report, err := os.ReadFile(file)
	require.NoError(t, err)
	undecided, err := clusterscan.UndecidedKubescape(report, accepted)
	require.NoError(t, err)
	if len(undecided) > 0 {
		t.Errorf("undecided, fix or accept in .cluster-scan-accepted.yaml:\n%s", strings.Join(undecided, "\n"))
	}
}

func scanTail(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return strings.Join(lines[max(0, len(lines)-5):], "\n")
}
