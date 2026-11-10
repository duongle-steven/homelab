package offline

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/render"
	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// Every path the testbed's gate names matches a tracked file: a renamed directory would otherwise drop out of the gate
// unseen (lab infra forgejo verify, infra/testbed/release-paths.yaml)
func TestReleasePathsExist(t *testing.T) {
	raw, err := os.ReadFile(repo.Root() + "/infra/testbed/release-paths.yaml")
	require.NoError(t, err)
	var release struct {
		Paths  []string `json:"paths"`
		NotRun []string `json:"not_run"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &release))
	require.NotEmpty(t, release.Paths)
	for _, glob := range append(release.Paths, release.NotRun...) {
		out, err := exec.Command("git", "-C", repo.Root(), "ls-files", "--", ":(glob)"+glob).Output()
		require.NoError(t, err, glob)
		assert.NotEmpty(t, strings.TrimSpace(string(out)), "%s matches no tracked file", glob)
	}
}

// The testbed's layers (docs/superpowers/specs/2026-11-02-testbed-design.md), through Argo CD, from GitHub branch testbed,
// which just testbed pushes. Another machine extends the list
func TestTestbedLayers(t *testing.T) {
	values := repo.Root() + "/system/argocd/values-testbed.yaml"
	yq := func(expr, file string) string {
		out, err := exec.Command("yq", expr, file).Output()
		require.NoError(t, err, expr)
		return strings.TrimSpace(string(out))
	}
	const git = `[.argocd-apps.applicationsets.root.generators | .. | select(has("directories"))][0]`
	assert.Equal(t, "system/rook-ceph\nsystem/cert-manager\nsystem/istio-system", yq(git+`.directories[] | select(.exclude != true) | .path`, values))
	// The repository and the branch come from the cluster Secret the testbed's unit writes
	assert.Equal(t, "{{ .metadata.annotations.revision }}", yq(git+".revision", values))
	assert.Equal(t, "{{ .metadata.annotations.repository }}", yq(git+".repoURL", values))
	unit, err := os.ReadFile(repo.Root() + "/infra/testbed/argocd/terragrunt.hcl")
	require.NoError(t, err)
	assert.Regexp(t, `branch\s+= "testbed"`, string(unit))
	assert.Contains(t, string(unit), `"${get_terragrunt_dir()}/../cluster/config.json")).seed_repository`)
	assert.Empty(t, render.Yq(t, "system/rook-ceph", "testbed", `select(.kind == "PolicyException") | .metadata.name`), "Kyverno is not in this layer")
	assert.NotEmpty(t, render.Yq(t, "system/rook-ceph", "", `select(.kind == "PolicyException") | .metadata.name`))
	// Layer 2 needs neither Kyverno nor the monitoring operator: their kinds are switched off, and only here
	for _, chart := range []string{"system/istio-system", "system/cert-manager"} {
		for _, kind := range []string{"PolicyException", "PodMonitor", "ServiceMonitor"} {
			assert.Empty(t, render.Yq(t, chart, "testbed", fmt.Sprintf(`select(.kind == %q) | .metadata.name`, kind)), "%s renders a %s", chart, kind)
		}
	}
	assert.NotEmpty(t, render.Yq(t, "system/istio-system", "", `select(.kind == "PolicyException") | .metadata.name`))
	assert.NotEmpty(t, render.Yq(t, "system/istio-system", "", `select(.kind == "PodMonitor") | .metadata.name`))
	assert.NotEmpty(t, render.Yq(t, "system/cert-manager", "", `select(.kind == "ServiceMonitor") | .metadata.name`))
}

// 12a-1's 2 vCPUs ran the nodes and rook-ceph: more needs a measurement showing CPU is the limit
func TestTestbedVMsDefaults(t *testing.T) {
	raw, err := os.ReadFile(repo.Root() + "/infra/_modules/testbed-vms/variables.tf")
	require.NoError(t, err)
	assert.Regexp(t, `variable "vcpus" \{[^}]*default\s*=\s*2\s`, string(raw))
}

// The workstation's multi-node environment is the testbed: staging names only an optional environment on its own hardware
func TestOldEnvironmentNameGone(t *testing.T) {
	out, err := exec.Command("git", "-C", repo.Root(), "grep", "-il", "staging", "--",
		":!docs/superpowers/", ":!CLAUDE.md", ":!mkdocs.yml", ":!docs/installation/testbed.md", ":!docs/installation/staging.md", ":!docs/how-to-guides/deploy-to-forgejo.md", ":!cli/internal/guard/", ":!cli/internal/cluster/inventory.go", ":!scripts/render-chart", ":!test/offline/release_paths_test.go", ":!test/offline/values_layers_test.go", ":!test/testbed_test.go",
		":!cli/internal/env/").CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return // git grep exits 1 when nothing matches
	}
	require.NoError(t, err, string(out))
	assert.Empty(t, strings.TrimSpace(string(out)))
}
