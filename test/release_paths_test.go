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

// Layer one of the testbed (docs/superpowers/specs/2026-11-02-testbed-design.md): Ceph through Argo CD, from GitHub branch
// testbed, which make testbed pushes. Another machine extends the list
func TestTestbedLayerOne(t *testing.T) {
	values := "../system/argocd/values-testbed.yaml"
	yq := func(expr, file string) string {
		out, err := exec.Command("yq", expr, file).Output()
		require.NoError(t, err, expr)
		return strings.TrimSpace(string(out))
	}
	const git = ".argocd-apps.applicationsets.root.generators[0].matrix.generators[0].git"
	assert.Equal(t, "system/rook-ceph", yq(git+`.directories[] | select(.exclude != true) | .path`, values))
	assert.Equal(t, "testbed", yq(git+".revision", values))
	assert.Equal(t, strings.Trim(yq(".seed_repository", "../infra/testbed/cluster/config.json"), `"`), yq(git+".repoURL", values))
	assert.Empty(t, renderYq(t, "system/rook-ceph", "testbed", `select(.kind == "PolicyException") | .metadata.name`), "Kyverno is not in this layer")
	assert.NotEmpty(t, renderYq(t, "system/rook-ceph", "", `select(.kind == "PolicyException") | .metadata.name`))
}

// 12a-1's 2 vCPUs ran the nodes and rook-ceph: more needs a measurement showing CPU is the limit
func TestTestbedVMsDefaults(t *testing.T) {
	raw, err := os.ReadFile("../infra/_modules/testbed-vms/variables.tf")
	require.NoError(t, err)
	assert.Regexp(t, `variable "vcpus" \{[^}]*default\s*=\s*2\s`, string(raw))
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
