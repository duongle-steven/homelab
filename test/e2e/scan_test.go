package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/clusterscan"
	"github.com/duongle-steven/homelab/test/internal/kube"
	"github.com/duongle-steven/homelab/test/internal/repo"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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

// ScanBench runs kube-bench on every testbed node (just testbed-scan). A run stopped midway leaves the privileged
// namespace behind: the next one deletes it first
var _ = Describe("ScanBench", Label("testbed", "scan"), func() {
	It("leaves no undecided kube-bench finding", func(ctx SpecContext) {
		raw, err := os.ReadFile(filepath.Join(repo.Root(), ".cluster-scan-accepted.yaml"))
		Expect(err).NotTo(HaveOccurred())
		accepted, err := clusterscan.LoadScanAccepted(raw)
		Expect(err).NotTo(HaveOccurred())

		remove := func(ctx SpecContext) {
			_, err := kube.Kubectl(ctx, "", "delete", "namespace", scanBenchNamespace, "--ignore-not-found", "--timeout=5m")
			Expect(err).NotTo(HaveOccurred(), "the privileged namespace %s may be left behind: delete it", scanBenchNamespace)
		}
		remove(ctx)
		DeferCleanup(remove, NodeTimeout(6*time.Minute))
		Expect(kube.Apply(ctx, "", fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %s
  labels: {pod-security.kubernetes.io/enforce: privileged}
`, scanBenchNamespace))).To(Succeed())

		dir := filepath.Join(repo.Root(), "test", "kube-bench-cfg")
		cfg := filepath.Join(dir, "k3s-cis-1.11")
		args := []string{"create", "configmap", "kube-bench-cfg", "--from-file=config.yaml=" + filepath.Join(dir, "config.yaml"),
			"--from-file=benchmark-config.yaml=" + filepath.Join(cfg, "config.yaml")}
		for _, name := range []string{"controlplane", "etcd", "master", "node", "policies"} {
			args = append(args, "--from-file="+filepath.Join(cfg, name+".yaml"))
		}
		out, err := kube.Kubectl(ctx, scanBenchNamespace, args...)
		Expect(err).NotTo(HaveOccurred(), out)

		nodes, _ := testbedHosts()
		for _, node := range nodes {
			Expect(kube.Apply(ctx, scanBenchNamespace, scanBenchJob(node))).To(Succeed())
		}
		var problems []string
		for _, node := range nodes {
			job := "job/bench-" + node
			_, _ = kube.Kubectl(ctx, scanBenchNamespace, "wait", job, "--for=jsonpath={.status.conditions[?(@.status==\"True\")].type}=Complete", "--timeout=5m")
			if done, err := kube.Kubectl(ctx, scanBenchNamespace, "get", job, "-o", "jsonpath={.status.succeeded}"); err != nil || done != "1" {
				problems = append(problems, node+": kube-bench did not complete")
				continue
			}
			report, err := kube.Kubectl(ctx, scanBenchNamespace, "logs", job)
			if err != nil {
				problems = append(problems, node+": its report could not be read")
				continue
			}
			undecided, err := clusterscan.UndecidedBench([]byte(report), accepted)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", node, err))
				continue
			}
			if len(undecided) > 0 {
				problems = append(problems, fmt.Sprintf("%s: undecided, fix or accept in .cluster-scan-accepted.yaml: %s", node, strings.Join(undecided, " ")))
			}
		}
		Expect(problems).To(BeEmpty())
	}, SpecTimeout(30*time.Minute))
})

// ScanKubescape runs kubescape on the sandbox (just dev-scan). --keep-local: nothing leaves the workstation. Its exit
// code stays 0 on findings, as no --compliance-threshold is set, so a non-zero one means the scan did not run
var _ = Describe("ScanKubescape", Label("sandbox", "scan"), func() {
	It("leaves no undecided kubescape finding", func(ctx SpecContext) {
		raw, err := os.ReadFile(filepath.Join(repo.Root(), ".cluster-scan-accepted.yaml"))
		Expect(err).NotTo(HaveOccurred())
		accepted, err := clusterscan.LoadScanAccepted(raw)
		Expect(err).NotTo(HaveOccurred())

		file := filepath.Join(GinkgoT().TempDir(), "report.json")
		out, err := exec.CommandContext(ctx, "kubescape", "scan", "framework", "allcontrols", "--keep-local", "--format", "json", "--output", file).CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), "kubescape failed: its last lines: %s", scanTail(out))
		report, err := os.ReadFile(file)
		Expect(err).NotTo(HaveOccurred())
		undecided, err := clusterscan.UndecidedKubescape(report, accepted)
		Expect(err).NotTo(HaveOccurred())
		Expect(undecided).To(BeEmpty(), "undecided, fix or accept in .cluster-scan-accepted.yaml")
	}, SpecTimeout(30*time.Minute))
})

func scanTail(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return strings.Join(lines[max(0, len(lines)-5):], "\n")
}
