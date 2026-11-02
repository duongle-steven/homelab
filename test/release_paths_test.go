package test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// Every path staging's gate names matches a tracked file: a renamed directory would otherwise drop out of the gate
// unseen (lab infra forgejo verify, infra/testbed/release-paths.yaml)
func TestReleasePathsExist(t *testing.T) {
	raw, err := os.ReadFile("../infra/testbed/release-paths.yaml")
	require.NoError(t, err)
	var release struct {
		Paths []string `json:"paths"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &release))
	require.NotEmpty(t, release.Paths)
	for _, glob := range release.Paths {
		out, err := exec.Command("git", "-C", "..", "ls-files", "--", ":(glob)"+glob).Output()
		require.NoError(t, err, glob)
		assert.NotEmpty(t, strings.TrimSpace(string(out)), "%s matches no tracked file", glob)
	}
}

// What staging's reduction leaves out, whole or switched off inside a chart, is what the gate does not ask staging to
// prove; the first step of the spec's order holds the core and the deploy path, which must stay
func TestStagingKeepsTheCore(t *testing.T) {
	out, err := exec.Command("yq", `.argocd-apps.applicationsets.root.generators[0].matrix.generators[0].git.directories[] | select(.exclude) | .path`, "../system/argocd/values-testbed.yaml").Output()
	require.NoError(t, err)
	for _, core := range []string{"system/istio-system", "system/cert-manager", "system/rook-ceph", "system/kyverno", "system/monitoring-system",
		"platform/external-secrets", "platform/openbao", "platform/generated-secrets", "platform/infra-secrets", "platform/kanidm", "platform/cloudnative-pg", "platform/forgejo", "system/argocd-config"} {
		assert.NotContains(t, strings.Fields(string(out)), core)
	}
	for _, left := range []string{"system/trivy-operator", "system/loki", "system/velero", "platform/grafana", "platform/zot", "platform/woodpecker", "platform/secrets-rotation"} {
		assert.Contains(t, strings.Fields(string(out)), left)
	}
}

// The workstation's multi-node environment is the testbed: staging names only an optional environment on its own hardware
func TestNoStagingLeftovers(t *testing.T) {
	out, err := exec.Command("git", "-C", "..", "grep", "-il", "staging", "--",
		":!docs/superpowers/", ":!CLAUDE.md", ":!docs/installation/testbed.md", ":!docs/installation/staging.md", ":!cli/internal/guard/", ":!cli/internal/cluster/inventory.go", ":!scripts/render-chart", ":!scripts/testbed-mark", ":!test/release_paths_test.go", ":!test/values_layers_test.go",
		":!cli/internal/cli/infra_forgejo.go", ":!cli/internal/env/env_test.go", ":!Makefile").CombinedOutput()
	if err != nil {
		return // git grep exits 1 when nothing matches
	}
	assert.Empty(t, strings.TrimSpace(string(out)))
}
