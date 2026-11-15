package offline

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The Trivy server downloads its database when it starts and answers /healthz only after: the chart's liveness probe
// (10 failures 10s apart) killed it mid-download whenever that took over 100s, and every restart downloaded it again
// (19 restarts and about 2GB written during one sandbox suite, 2026-11-15). A native MutatingAdmissionPolicy gives
// the server's container a startup probe, so liveness waits until the database is in place
func TestTrivyServerStartupProbe(t *testing.T) {
	policy := func(kinds ...string) string {
		var docs []string
		for _, kind := range kinds {
			docs = append(docs, renderVelero(t, "system/trivy-operator", "", `select(.kind == "`+kind+`" and .metadata.name == "trivy-server-startup") | del(.spec.matchResources)`))
		}
		return strings.Join(docs, "\n---\n")
	}
	assert.Equal(t, "Ignore", renderVelero(t, "system/trivy-operator", "", `select(.kind == "MutatingAdmissionPolicy" and .metadata.name == "trivy-server-startup") | .spec.failurePolicy`),
		"a failed mutation must not block the server: it then starts as before")
	assert.Equal(t, "trivy-operator", renderVelero(t, "system/trivy-operator", "",
		`select(.kind == "MutatingAdmissionPolicyBinding" and .metadata.name == "trivy-server-startup") | .spec.matchResources.namespaceSelector.matchLabels["kubernetes.io/metadata.name"]`))

	work := t.TempDir()
	mutating := filepath.Join(work, "mutating.yaml")
	// Kyverno's CLI 1.19 applies MutatingAdmissionPolicy only as v1alpha1, whose fields these use unchanged
	writeFile(t, mutating, strings.ReplaceAll(policy("MutatingAdmissionPolicy", "MutatingAdmissionPolicyBinding"),
		"admissionregistration.k8s.io/v1\n", "admissionregistration.k8s.io/v1alpha1\n"))
	pods := filepath.Join(work, "pods.yaml")
	writeFile(t, pods, `apiVersion: v1
kind: Pod
metadata: {name: trivy-server-0, namespace: trivy-operator, labels: {app.kubernetes.io/name: trivy-operator, app.kubernetes.io/component: server}}
spec:
  containers:
    - name: trivy-server
      image: ghcr.io/aquasecurity/trivy:0.76.1
      ports: [{name: trivy-http, containerPort: 4954}]
---
apiVersion: v1
kind: Pod
metadata: {name: scan-vulnerabilityreport-abc, namespace: trivy-operator}
spec:
  containers:
    - name: main
      image: ghcr.io/aquasecurity/trivy:0.76.1`)
	mutated := filepath.Join(work, "mutated.yaml")
	kyvernoApply(t, mutating, pods, "-o", mutated)
	probes := map[string]any{}
	for _, doc := range yamlDocs(t, mutated) {
		if doc["kind"] == "Pod" {
			probes[doc["metadata"].(map[string]any)["name"].(string)] = yqValue(doc, "spec", "containers")
		}
	}
	server := strings.ReplaceAll(strings.ReplaceAll(toJSON(t, probes["trivy-server-0"]), " ", ""), "\n", "")
	assert.Contains(t, server, `"startupProbe":{"failureThreshold":60,"httpGet":{"path":"/healthz","port":"trivy-http"},"periodSeconds":10}`)
	assert.NotContains(t, toJSON(t, probes["scan-vulnerabilityreport-abc"]), "startupProbe", "only the server gets it")
}

func toJSON(t *testing.T, v any) string {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
