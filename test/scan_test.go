package test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// What make testbed-scan and make dev-scan compare a tool's report with (docs/how-to-guides/cluster-scan.md).
// Reports hold command lines and config: nothing here names more than a check, a control and a resource.

// The scan must have seen the cluster: the spike's sandbox had 1307 resources
const scanMinResources = 500

// A resource the sandbox always has and kubescape always reports on
const scanKnownResource = "istio-system/DaemonSet/ztunnel"

// Accepted only by resource, so a new workload that may do these fails the target
var scanDangerous = []string{"C-0185", "C-0187", "C-0035", "C-0065", "C-0191", "C-0057", "C-0045", "C-0048", "C-0041", "C-0038", "C-0046", "C-0012", "C-0259"}

type scanCheck struct {
	Check  string `yaml:"check"`
	Reason string `yaml:"reason"`
}

type scanControl struct {
	ID     string `yaml:"id"`
	Reason string `yaml:"reason"`
}

// scanResource accepts each of its controls on each of its resources, for one reason
type scanResource struct {
	IDs       []string `yaml:"ids"`
	Resources []string `yaml:"resources"`
	Reason    string   `yaml:"reason"`
}

type scanAccepted struct {
	KubeBench []scanCheck `yaml:"kubeBench"`
	Kubescape struct {
		Controls  []scanControl  `yaml:"controls"`
		Resources []scanResource `yaml:"resources"`
	} `yaml:"kubescape"`
}

func loadScanAccepted(raw []byte) (scanAccepted, error) {
	var accepted scanAccepted
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&accepted); err != nil {
		return accepted, fmt.Errorf("accepted findings: %w", err)
	}
	for _, c := range accepted.KubeBench {
		if c.Check == "" || strings.TrimSpace(c.Reason) == "" {
			return accepted, fmt.Errorf("kubeBench check %q needs a number and a reason", c.Check)
		}
	}
	for _, c := range accepted.Kubescape.Controls {
		if c.ID == "" || strings.TrimSpace(c.Reason) == "" {
			return accepted, fmt.Errorf("kubescape control %q needs an id and a reason", c.ID)
		}
		if slices.Contains(scanDangerous, c.ID) {
			return accepted, fmt.Errorf("kubescape control %s is dangerous: accept it by resource", c.ID)
		}
	}
	for _, r := range accepted.Kubescape.Resources {
		if len(r.IDs) == 0 || len(r.Resources) == 0 || strings.TrimSpace(r.Reason) == "" {
			return accepted, fmt.Errorf("kubescape resources %q of %q need ids, resources and a reason", r.Resources, r.IDs)
		}
		for _, resource := range r.Resources {
			fields := strings.Split(resource, "/")
			if _, err := path.Match(resource, ""); err != nil || len(fields) != 3 ||
				strings.Contains(fields[1], "*") || strings.Contains(fields[0], "*") || fields[2] == "*" {
				return accepted, fmt.Errorf("kubescape resource %q must be <namespace>/<Kind>/<name>, with a pattern only inside the name", resource)
			}
		}
	}
	return accepted, nil
}

// undecidedBench returns the FAILs of one node's kube-bench report that nobody decided on. It refuses a report that
// did not really check: a section missing or empty, or a check whose audit command was not found (the image has no
// journalctl unless the node's /nix is mounted, and every k3s check calls it)
func undecidedBench(report []byte, accepted scanAccepted) ([]string, error) {
	var parsed struct {
		Controls []struct {
			ID    string `json:"id"`
			Tests []struct {
				Results []struct {
					Number      string `json:"test_number"`
					Status      string `json:"status"`
					Reason      string `json:"reason"`
					ActualValue string `json:"actual_value"`
				} `json:"results"`
			} `json:"tests"`
		} `json:"Controls"`
	}
	if err := json.Unmarshal(report, &parsed); err != nil {
		return nil, fmt.Errorf("kube-bench report: %w", err)
	}
	checked := map[string]int{}
	undecided := []string{}
	for _, control := range parsed.Controls {
		for _, test := range control.Tests {
			for _, result := range test.Results {
				checked[control.ID]++
				if strings.Contains(result.Reason, ": not found") || strings.Contains(result.ActualValue, ": not found") {
					return nil, fmt.Errorf("kube-bench check %s: its audit command was not found", result.Number)
				}
				if result.Status == "FAIL" && !slices.ContainsFunc(accepted.KubeBench, func(c scanCheck) bool { return c.Check == result.Number }) {
					undecided = append(undecided, result.Number)
				}
			}
		}
	}
	for _, section := range []string{"1", "2", "3", "4", "5"} {
		if checked[section] == 0 {
			return nil, fmt.Errorf("kube-bench report: section %s did not run", section)
		}
	}
	slices.Sort(undecided)
	return slices.Compact(undecided), nil
}

// scanResourceKey is <namespace>/<Kind>/<name>, the last three fields of a resourceID: kubescape prefixes an API
// group and version, and names a binding by its subject and role first. A cluster-scoped resource starts with "/"
func scanResourceKey(resourceID string) string {
	fields := strings.Split(resourceID, "/")
	if len(fields) < 3 {
		return resourceID
	}
	return strings.Join(fields[len(fields)-3:], "/")
}

// undecidedKubescape returns "<control> <resource>" for each failure nobody decided on. It refuses a report that did
// not see the cluster: too few resources, or a known one missing
func undecidedKubescape(report []byte, accepted scanAccepted) ([]string, error) {
	var parsed struct {
		SummaryDetails struct {
			ResourceCounters struct {
				Passed   int `json:"passedResources"`
				Failed   int `json:"failedResources"`
				Skipped  int `json:"skippedResources"`
				Excluded int `json:"excludedResources"`
			} `json:"ResourceCounters"`
		} `json:"summaryDetails"`
		Results []struct {
			ResourceID string `json:"resourceID"`
			Controls   []struct {
				ControlID string `json:"controlID"`
				Status    struct {
					Status string `json:"status"`
				} `json:"status"`
			} `json:"controls"`
		} `json:"results"`
	}
	if err := json.Unmarshal(report, &parsed); err != nil {
		return nil, fmt.Errorf("kubescape report: %w", err)
	}
	counters := parsed.SummaryDetails.ResourceCounters
	if seen := counters.Passed + counters.Failed + counters.Skipped + counters.Excluded; seen < scanMinResources {
		return nil, fmt.Errorf("kubescape report: saw %d resources, fewer than %d", seen, scanMinResources)
	}
	known := false
	undecided := []string{}
	for _, result := range parsed.Results {
		resource := scanResourceKey(result.ResourceID)
		known = known || resource == scanKnownResource
		for _, control := range result.Controls {
			if control.Status.Status != "failed" {
				continue
			}
			if slices.ContainsFunc(accepted.Kubescape.Controls, func(c scanControl) bool { return c.ID == control.ControlID }) {
				continue
			}
			if slices.ContainsFunc(accepted.Kubescape.Resources, func(r scanResource) bool {
				return slices.Contains(r.IDs, control.ControlID) && slices.ContainsFunc(r.Resources, func(pattern string) bool {
					matched, _ := path.Match(pattern, resource)
					return matched
				})
			}) {
				continue
			}
			undecided = append(undecided, control.ControlID+" "+resource)
		}
	}
	if !known {
		return nil, fmt.Errorf("kubescape report: %s is not in the results", scanKnownResource)
	}
	slices.Sort(undecided)
	return slices.Compact(undecided), nil
}

// secretIsEncrypted checks a Secret's raw value in etcd: k3s's provider prefix, and no trace of the plain value
func secretIsEncrypted(raw []byte, value string) error {
	if value == "" {
		return errors.New("no value to look for")
	}
	if !bytes.HasPrefix(raw, []byte("k8s:enc:aescbc:")) {
		return errors.New("the value in etcd lacks the prefix k8s:enc:aescbc:")
	}
	if bytes.Contains(raw, []byte(value)) {
		return errors.New("the plain value is readable in etcd")
	}
	return nil
}

const (
	scanBenchNamespace = "scan-bench"
	scanBenchImage     = "docker.io/aquasec/kube-bench:v0.16.0"
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

// TestScanBench runs kube-bench on every testbed node (make testbed-scan). A run stopped midway leaves the privileged
// namespace behind: the next one deletes it first
func TestScanBench(t *testing.T) {
	testbedOnly(t)
	raw, err := os.ReadFile(filepath.Join("..", ".cluster-scan-accepted.yaml"))
	require.NoError(t, err)
	accepted, err := loadScanAccepted(raw)
	require.NoError(t, err)

	cluster, namespaced := testbedKubectl(""), testbedKubectl(scanBenchNamespace)
	remove := func() {
		_ = k8s.RunKubectlE(t, cluster, "delete", "namespace", scanBenchNamespace, "--ignore-not-found", "--timeout=5m")
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
		undecided, err := undecidedBench([]byte(report), accepted)
		if err != nil {
			t.Errorf("%s: %v", node, err)
			continue
		}
		if len(undecided) > 0 {
			t.Errorf("%s: undecided, fix or accept in .cluster-scan-accepted.yaml: %s", node, strings.Join(undecided, " "))
		}
	}
}

// TestScanKubescape runs kubescape on the sandbox (make dev-scan). --keep-local: nothing leaves the workstation. Its
// exit code stays 0 on findings, as no --compliance-threshold is set, so a non-zero one means the scan did not run
func TestScanKubescape(t *testing.T) {
	if os.Getenv("ENV") != "dev" {
		t.Skip("kubescape runs against the sandbox only (make dev-scan)")
	}
	raw, err := os.ReadFile(filepath.Join("..", ".cluster-scan-accepted.yaml"))
	require.NoError(t, err)
	accepted, err := loadScanAccepted(raw)
	require.NoError(t, err)

	file := filepath.Join(t.TempDir(), "report.json")
	out, err := exec.Command("kubescape", "scan", "framework", "allcontrols", "--keep-local", "--format", "json", "--output", file).CombinedOutput()
	require.NoError(t, err, "kubescape failed: its last lines: %s", scanTail(out))
	report, err := os.ReadFile(file)
	require.NoError(t, err)
	undecided, err := undecidedKubescape(report, accepted)
	require.NoError(t, err)
	if len(undecided) > 0 {
		t.Errorf("undecided, fix or accept in .cluster-scan-accepted.yaml:\n%s", strings.Join(undecided, "\n"))
	}
}

func scanTail(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return strings.Join(lines[max(0, len(lines)-5):], "\n")
}

// The flags kube-bench asks for, the same on k3d and the nodes: the sandbox's whole platform is what proves them harmless
func TestScanHardeningFlags(t *testing.T) {
	k3d, err := os.ReadFile(filepath.Join("..", "infra", "_modules", "k3d", "k3d.yaml"))
	require.NoError(t, err)
	nodes, err := os.ReadFile(filepath.Join("..", "infra", "_modules", "metal", "flake", "profiles", "k3s-server.nix"))
	require.NoError(t, err)
	for _, flag := range []string{
		"--kube-apiserver-arg=service-account-extend-token-expiration=false",
		"--kube-apiserver-arg=audit-log-maxage=30",
		"--kubelet-arg=seccomp-default=true",
		"--kubelet-arg=pod-max-pids=4096",
	} {
		assert.Contains(t, string(k3d), "- arg: "+flag+"\n", "k3d.yaml")
		assert.Contains(t, string(nodes), `"`+flag+`"`, "k3s-server.nix")
	}
}

// What kubescape found and step 15d narrowed: no component here holds cluster-admin or reads every Secret for nothing
func TestScanNarrowedRBAC(t *testing.T) {
	for _, env := range []string{"", "dev"} {
		velero := renderYq(t, "system/velero", env, `select(.kind == "ClusterRoleBinding" and .metadata.name == "velero-server") | .roleRef.name`)
		assert.Equal(t, "velero-volumes", velero, "velero, env=%q", env)
		rules := renderYq(t, "system/velero", env, `select(.kind == "ClusterRole" and .metadata.name == "velero-volumes") | .rules | tojson`)
		assert.NotContains(t, rules, `"*"`, "velero's ClusterRole names what it needs")
		assert.NotContains(t, rules, "secrets", "velero's Secrets are in its own namespace")

		assert.Empty(t, renderYq(t, "platform/grafana", env, `select(.kind == "ClusterRole" or .kind == "ClusterRoleBinding") | .metadata.name`), "grafana")
		for _, namespace := range []string{"grafana", "monitoring-system"} {
			binding := renderYq(t, "platform/grafana", env, `select(.kind == "RoleBinding" and .metadata.namespace == "`+namespace+`") | .roleRef.name`)
			assert.Equal(t, "grafana-sidecar", binding, "grafana in %s", namespace)
			role := renderYq(t, "platform/grafana", env, `select(.kind == "Role" and .metadata.namespace == "`+namespace+`") | .rules | tojson`)
			assert.Contains(t, role, "configmaps", "grafana in %s", namespace)
			assert.NotContains(t, role, "secrets", "grafana in %s", namespace)
		}

		metrics := renderYq(t, "system/monitoring-system", env, `select(.kind == "ClusterRole" and .metadata.name == "monitoring-system-kube-state-metrics") | .rules | tojson`)
		assert.Contains(t, metrics, "pods")
		assert.NotContains(t, metrics, "secrets", "kube-state-metrics")
	}
}

func TestScanAcceptedRefused(t *testing.T) {
	for name, raw := range map[string]string{
		"check without a reason":    "kubeBench: [{check: 1.3.7}]",
		"control without a reason":  "kubescape: {controls: [{id: C-0050}]}",
		"resource without a reason": "kubescape: {resources: [{ids: [C-0057], resources: [a/Pod/b]}]}",
		"a dangerous whole control": "kubescape: {controls: [{id: C-0185, reason: noise}]}",
		"a bad pattern":             "kubescape: {resources: [{ids: [C-0057], resources: ['a/Pod/['], reason: x}]}",
		"not three fields":          "kubescape: {resources: [{ids: [C-0057], resources: [a/Pod], reason: x}]}",
		"every name":                "kubescape: {resources: [{ids: [C-0057], resources: ['a/Pod/*'], reason: x}]}",
		"no ids":                    "kubescape: {resources: [{resources: [a/Pod/b], reason: x}]}",
		"no resources":              "kubescape: {resources: [{ids: [C-0057], reason: x}]}",
		"every namespace":           "kubescape: {resources: [{ids: [C-0057], resources: ['*/Pod/b'], reason: x}]}",
		"a misspelt key":            "kubeBench: [{check: 1.3.7, reasn: x}]",
	} {
		_, err := loadScanAccepted([]byte(raw))
		assert.Error(t, err, name)
	}
	_, err := loadScanAccepted([]byte("kubescape: {controls: [{id: C-0185, reason: noise}]}"))
	assert.ErrorContains(t, err, "C-0185")
	accepted, err := loadScanAccepted([]byte(`
kubeBench: [{check: 1.3.7, reason: Prometheus}]
kubescape:
  controls: [{id: C-0050, reason: noise}]
  resources: [{ids: [C-0057, C-0045], resources: [kube-system/DaemonSet/svclb-*, a/Pod/b], reason: ServiceLB}]
`))
	require.NoError(t, err)
	assert.Len(t, accepted.KubeBench, 1)
	assert.Len(t, accepted.Kubescape.Controls, 1)
	assert.Len(t, accepted.Kubescape.Resources, 1)
}

func TestScanReportsRefused(t *testing.T) {
	for _, report := range []string{"", "not json", "level=warn something\n{\"Controls\":[]}", "{}"} {
		_, err := undecidedBench([]byte(report), scanAccepted{})
		assert.Error(t, err, "kube-bench: %q", report)
		_, err = undecidedKubescape([]byte(report), scanAccepted{})
		assert.Error(t, err, "kubescape: %q", report)
	}
}

func benchReport(results map[string][][3]string) []byte {
	type result struct {
		Number string `json:"test_number"`
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	type test struct {
		Results []result `json:"results"`
	}
	type control struct {
		ID    string `json:"id"`
		Tests []test `json:"tests"`
	}
	report := struct {
		Controls []control `json:"Controls"`
	}{}
	for _, section := range []string{"1", "2", "3", "4", "5"} {
		if rows, ok := results[section]; ok {
			current := test{}
			for _, row := range rows {
				current.Results = append(current.Results, result{row[0], row[1], row[2]})
			}
			report.Controls = append(report.Controls, control{section, []test{current}})
		}
	}
	out, _ := json.Marshal(report)
	return out
}

func TestScanBenchDecided(t *testing.T) {
	full := map[string][][3]string{
		"1": {{"1.2.30", "FAIL", ""}, {"1.2.3", "WARN", ""}},
		"2": {{"2.1", "PASS", ""}},
		"3": {{"3.1.1", "WARN", ""}},
		"4": {{"4.2.4", "FAIL", ""}},
		"5": {{"5.1.1", "WARN", ""}},
	}
	undecided, err := undecidedBench(benchReport(full), scanAccepted{KubeBench: []scanCheck{{"4.2.4", "false"}}})
	require.NoError(t, err)
	assert.Equal(t, []string{"1.2.30"}, undecided)

	undecided, err = undecidedBench(benchReport(full), scanAccepted{})
	require.NoError(t, err)
	assert.Equal(t, []string{"1.2.30", "4.2.4"}, undecided)

	_, err = undecidedBench(benchReport(map[string][][3]string{"1": full["1"], "4": full["4"], "5": full["5"]}), scanAccepted{})
	assert.ErrorContains(t, err, "section 2")

	empty := map[string][][3]string{"1": full["1"], "2": {}, "3": full["3"], "4": full["4"], "5": full["5"]}
	_, err = undecidedBench(benchReport(empty), scanAccepted{})
	assert.ErrorContains(t, err, "section 2")

	blind := map[string][][3]string{"1": {{"1.2.1", "PASS", "journalctl: not found"}}, "2": full["2"], "3": full["3"], "4": full["4"], "5": full["5"]}
	_, err = undecidedBench(benchReport(blind), scanAccepted{})
	assert.ErrorContains(t, err, "1.2.1")
}

func kubescapeReport(resources int, failed map[string][]string) []byte {
	type status struct {
		Status string `json:"status"`
	}
	type control struct {
		ControlID string `json:"controlID"`
		Status    status `json:"status"`
	}
	type result struct {
		ResourceID string    `json:"resourceID"`
		Controls   []control `json:"controls"`
	}
	report := map[string]any{"summaryDetails": map[string]any{"ResourceCounters": map[string]int{"passedResources": resources}}}
	results := []result{}
	for id, controls := range failed {
		current := result{ResourceID: id, Controls: []control{{"C-0001", status{"passed"}}}}
		for _, c := range controls {
			current.Controls = append(current.Controls, control{c, status{"failed"}})
		}
		results = append(results, current)
	}
	report["results"] = results
	out, _ := json.Marshal(report)
	return out
}

func TestScanKubescapeDecided(t *testing.T) {
	accepted := scanAccepted{}
	accepted.Kubescape.Controls = []scanControl{{"C-0050", "noise"}}
	accepted.Kubescape.Resources = []scanResource{
		{[]string{"C-0057"}, []string{"istio-system/DaemonSet/ztunnel", "kube-system/DaemonSet/svclb-*"}, "accepted"},
		{[]string{"C-0185"}, []string{"/ClusterRoleBinding/known"}, "known"},
	}
	undecided, err := undecidedKubescape(kubescapeReport(scanMinResources, map[string][]string{
		"apps/v1/istio-system/DaemonSet/ztunnel":        {"C-0057", "C-0050", "C-0045"},
		"apps/v1/kube-system/DaemonSet/svclb-gateway-1": {"C-0057"},
		"apps/v1/ns/DaemonSet/other":                    {"C-0057", "C-0057"},
		"/ns/ServiceAccount/a/rbac.authorization.k8s.io/v1//ClusterRole/cluster-admin/rbac.authorization.k8s.io/v1//ClusterRoleBinding/known": {"C-0185"},
		"/ns/ServiceAccount/a/rbac.authorization.k8s.io/v1//ClusterRole/cluster-admin/rbac.authorization.k8s.io/v1//ClusterRoleBinding/new":   {"C-0185"},
	}), accepted)
	require.NoError(t, err)
	assert.Equal(t, []string{"C-0045 istio-system/DaemonSet/ztunnel", "C-0057 ns/DaemonSet/other", "C-0185 /ClusterRoleBinding/new"}, undecided)

	_, err = undecidedKubescape(kubescapeReport(scanMinResources-1, map[string][]string{"apps/v1/istio-system/DaemonSet/ztunnel": {}}), accepted)
	assert.ErrorContains(t, err, "resources")

	_, err = undecidedKubescape(kubescapeReport(scanMinResources, map[string][]string{"apps/v1/ns/DaemonSet/other": {}}), accepted)
	assert.ErrorContains(t, err, scanKnownResource)
}

func TestSecretIsEncrypted(t *testing.T) {
	assert.NoError(t, secretIsEncrypted([]byte("k8s:enc:aescbc:v1:aescbckey:\x01\x02\x03"), "plain-value"))
	assert.ErrorContains(t, secretIsEncrypted([]byte("k8s\x00\nplain-value"), "plain-value"), "prefix")
	assert.ErrorContains(t, secretIsEncrypted([]byte("\x01\x02"), "plain-value"), "prefix")
	assert.ErrorContains(t, secretIsEncrypted([]byte("k8s:enc:aescbc:v1:aescbckey:plain-value"), "plain-value"), "plain value")
	assert.Error(t, secretIsEncrypted(nil, ""))
}
