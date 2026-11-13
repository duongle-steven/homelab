package offline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/clusterscan"
	"github.com/duongle-steven/homelab/test/internal/render"
	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The flags kube-bench asks for, the same on k3d and the nodes: the sandbox's whole platform is what proves them harmless
func TestScanHardeningFlags(t *testing.T) {
	k3d, err := os.ReadFile(filepath.Join(repo.Root(), "infra", "_modules", "k3d", "k3d.yaml"))
	require.NoError(t, err)
	nodes, err := os.ReadFile(filepath.Join(repo.Root(), "infra", "_modules", "metal", "flake", "profiles", "k3s-server.nix"))
	require.NoError(t, err)
	for _, flag := range []string{
		"--kube-apiserver-arg=service-account-extend-token-expiration=false",
		"--kube-apiserver-arg=audit-log-maxage=30",
		"--kubelet-arg=pod-max-pids=4096",
		"--kubelet-arg=seccomp-default=true",
	} {
		assert.Contains(t, string(k3d), "- arg: "+flag+"\n", "k3d.yaml")
		assert.Contains(t, string(nodes), `"`+flag+`"`, "k3s-server.nix")
	}
	// seccomp-default gives the golden path's Buildah step RuntimeDefault, which refuses its unshare: its Localhost
	// profile sits beside the kubelet (step 15h)
	assert.Contains(t, string(nodes), "/var/lib/kubelet/seccomp/homelab/buildah.json", "k3s-server.nix")
	assert.Contains(t, string(nodes), "../seccomp/buildah.json", "k3s-server.nix")
	k3dModule, err := os.ReadFile(filepath.Join(repo.Root(), "infra", "_modules", "k3d", "main.tf"))
	require.NoError(t, err)
	assert.Contains(t, string(k3dModule), ":/var/lib/kubelet/seccomp/homelab/buildah.json@server:*", "main.tf")
}

// What kubescape found and step 15d narrowed: no component here holds cluster-admin or reads every Secret for nothing
func TestScanNarrowedRBAC(t *testing.T) {
	for _, env := range []string{"", "dev"} {
		velero := render.Yq(t, "system/velero", env, `select(.kind == "ClusterRoleBinding" and .metadata.name == "velero-server") | .roleRef.name`)
		assert.Equal(t, "velero-volumes", velero, "velero, env=%q", env)
		rules := render.Yq(t, "system/velero", env, `select(.kind == "ClusterRole" and .metadata.name == "velero-volumes") | .rules | tojson`)
		others := render.Yq(t, "system/velero", env, `select(.kind == "ClusterRole" and .metadata.name == "velero-volumes") | .rules[] | select(.apiGroups[0] != "velero.io") | tojson`)
		assert.NotContains(t, others, `"*"`, "velero's ClusterRole names what it needs")
		assert.NotContains(t, rules, "secrets", "velero's Secrets are in its own namespace")
		assert.Contains(t, rules, `"velero.io"`, "its controllers watch Velero's kinds at cluster scope: a restore waits for them forever")
		volumes := render.Yq(t, "system/velero", env, `select(.kind == "ClusterRole" and .metadata.name == "velero-volumes") | .rules[] | select(.resources[] == "persistentvolumes") | .verbs[]`)
		assert.Contains(t, strings.Fields(volumes), "patch", "velero rebinds restored volumes")
		assert.NotContains(t, strings.Fields(volumes), "create", "a volume velero creates could be a hostPath one its pods mount")

		assert.Empty(t, render.Yq(t, "platform/grafana", env, `select(.kind == "ClusterRole" or .kind == "ClusterRoleBinding") | .metadata.name`), "grafana")
		for _, namespace := range []string{"grafana", "monitoring-system"} {
			binding := render.Yq(t, "platform/grafana", env, `select(.kind == "RoleBinding" and .metadata.namespace == "`+namespace+`") | .roleRef.name`)
			assert.Equal(t, "grafana-sidecar", binding, "grafana in %s", namespace)
			role := render.Yq(t, "platform/grafana", env, `select(.kind == "Role" and .metadata.namespace == "`+namespace+`") | .rules | tojson`)
			assert.Contains(t, role, "configmaps", "grafana in %s", namespace)
			assert.NotContains(t, role, "secrets", "grafana in %s", namespace)
		}

		metrics := render.Yq(t, "system/monitoring-system", env, `select(.kind == "ClusterRole" and .metadata.name == "monitoring-system-kube-state-metrics") | .rules | tojson`)
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
		"any namespace character":   "kubescape: {resources: [{ids: [C-0057], resources: ['?/Pod/b'], reason: x}]}",
		"a class for the Kind":      "kubescape: {resources: [{ids: [C-0057], resources: ['a/[A-Z]od/b'], reason: x}]}",
		"every name, spelt longer":  "kubescape: {resources: [{ids: [C-0057], resources: ['a/Pod/?*', 'a/Pod/[a-z]*'], reason: x}]}",
		"reading every Secret":      "kubescape: {controls: [{id: C-0015, reason: noise}]}",
		"creating pods":             "kubescape: {controls: [{id: C-0186, reason: noise}]}",
		"a misspelt key":            "kubeBench: [{check: 1.3.7, reasn: x}]",
	} {
		_, err := clusterscan.LoadScanAccepted([]byte(raw))
		assert.Error(t, err, name)
	}
	_, err := clusterscan.LoadScanAccepted([]byte("kubescape: {controls: [{id: C-0185, reason: noise}]}"))
	assert.ErrorContains(t, err, "C-0185")
	accepted, err := clusterscan.LoadScanAccepted([]byte(`
kubeBench: [{check: 1.3.7, reason: Prometheus}]
kubescape:
  controls: [{id: C-0050, reason: noise}]
  resources: [{ids: [C-0057, C-0045], resources: [kube-system/DaemonSet/svclb-*, a/Pod/b], reason: ServiceLB}]
`))
	require.NoError(t, err)
	assert.Len(t, accepted.KubeBench, 1)
	assert.Len(t, accepted.Kubescape.Controls, 1)
	assert.Len(t, accepted.Kubescape.Resources, 1)

	// The repository's own file loads, so a refused entry fails the basic checks rather than the next scan
	raw, err := os.ReadFile(filepath.Join(repo.Root(), ".cluster-scan-accepted.yaml"))
	require.NoError(t, err)
	_, err = clusterscan.LoadScanAccepted(raw)
	assert.NoError(t, err)
}

func TestScanReportsRefused(t *testing.T) {
	for _, report := range []string{"", "not json", "level=warn something\n{\"Controls\":[]}", "{}"} {
		_, err := clusterscan.UndecidedBench([]byte(report), clusterscan.Accepted{})
		assert.Error(t, err, "kube-bench: %q", report)
		_, err = clusterscan.UndecidedKubescape([]byte(report), clusterscan.Accepted{})
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
	undecided, err := clusterscan.UndecidedBench(benchReport(full), clusterscan.Accepted{KubeBench: []clusterscan.Check{{Check: "4.2.4", Reason: "false"}}})
	require.NoError(t, err)
	assert.Equal(t, []string{"1.2.30"}, undecided)

	undecided, err = clusterscan.UndecidedBench(benchReport(full), clusterscan.Accepted{})
	require.NoError(t, err)
	assert.Equal(t, []string{"1.2.30", "4.2.4"}, undecided)

	_, err = clusterscan.UndecidedBench(benchReport(map[string][][3]string{"1": full["1"], "4": full["4"], "5": full["5"]}), clusterscan.Accepted{})
	assert.ErrorContains(t, err, "section 2")

	empty := map[string][][3]string{"1": full["1"], "2": {}, "3": full["3"], "4": full["4"], "5": full["5"]}
	_, err = clusterscan.UndecidedBench(benchReport(empty), clusterscan.Accepted{})
	assert.ErrorContains(t, err, "section 2")

	blind := map[string][][3]string{"1": {{"1.2.1", "PASS", "journalctl: not found"}}, "2": full["2"], "3": full["3"], "4": full["4"], "5": full["5"]}
	_, err = clusterscan.UndecidedBench(benchReport(blind), clusterscan.Accepted{})
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
	accepted := clusterscan.Accepted{}
	accepted.Kubescape.Controls = []clusterscan.Control{{ID: "C-0050", Reason: "noise"}}
	accepted.Kubescape.Resources = []clusterscan.Resource{
		{IDs: []string{"C-0057"}, Resources: []string{"istio-system/DaemonSet/ztunnel", "kube-system/DaemonSet/svclb-*"}, Reason: "accepted"},
		{IDs: []string{"C-0185"}, Resources: []string{"/ClusterRoleBinding/known"}, Reason: "known"},
	}
	undecided, err := clusterscan.UndecidedKubescape(kubescapeReport(clusterscan.MinResources, map[string][]string{
		"apps/v1/istio-system/DaemonSet/ztunnel":        {"C-0057", "C-0050", "C-0045"},
		"apps/v1/kube-system/DaemonSet/svclb-gateway-1": {"C-0057"},
		"apps/v1/ns/DaemonSet/other":                    {"C-0057", "C-0057"},
		"/ns/ServiceAccount/a/rbac.authorization.k8s.io/v1//ClusterRole/cluster-admin/rbac.authorization.k8s.io/v1//ClusterRoleBinding/known": {"C-0185"},
		"/ns/ServiceAccount/a/rbac.authorization.k8s.io/v1//ClusterRole/cluster-admin/rbac.authorization.k8s.io/v1//ClusterRoleBinding/new":   {"C-0185"},
	}), accepted)
	require.NoError(t, err)
	assert.Equal(t, []string{"C-0045 istio-system/DaemonSet/ztunnel", "C-0057 ns/DaemonSet/other", "C-0185 /ClusterRoleBinding/new"}, undecided)

	_, err = clusterscan.UndecidedKubescape(kubescapeReport(clusterscan.MinResources-1, map[string][]string{"apps/v1/istio-system/DaemonSet/ztunnel": {}}), accepted)
	assert.ErrorContains(t, err, "resources")

	_, err = clusterscan.UndecidedKubescape(kubescapeReport(clusterscan.MinResources, map[string][]string{"apps/v1/ns/DaemonSet/other": {}}), accepted)
	assert.ErrorContains(t, err, clusterscan.KnownResource)
}

func TestSecretIsEncrypted(t *testing.T) {
	assert.NoError(t, clusterscan.SecretIsEncrypted([]byte("k8s:enc:aescbc:v1:aescbckey:\x01\x02\x03"), "plain-value"))
	assert.ErrorContains(t, clusterscan.SecretIsEncrypted([]byte("k8s\x00\nplain-value"), "plain-value"), "prefix")
	assert.ErrorContains(t, clusterscan.SecretIsEncrypted([]byte("\x01\x02"), "plain-value"), "prefix")
	assert.ErrorContains(t, clusterscan.SecretIsEncrypted([]byte("k8s:enc:aescbc:v1:aescbckey:plain-value"), "plain-value"), "plain value")
	assert.Error(t, clusterscan.SecretIsEncrypted(nil, ""))
}
