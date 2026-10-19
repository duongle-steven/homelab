package test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// render renders system/argocd for the sandbox the way the bootstrap does, through set-revision.sh
func renderArgoCD(t *testing.T, revision string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", `scripts/render-chart system/argocd dev | sh infra/_modules/argocd/set-revision.sh "$0"`, revision)
	cmd.Dir = ".."
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return string(out)
}

// The bootstrap points every source and generator of the homelab repository at the chosen commit, and nothing else
func TestArgoCDRevision(t *testing.T) {
	plain := renderArgoCD(t, "")
	require.Contains(t, plain, "revision: sandbox")
	pinned := renderArgoCD(t, "abc123")
	assert.NotContains(t, pinned, "revision: sandbox")
	assert.NotContains(t, pinned, "targetRevision: sandbox")
	assert.Contains(t, pinned, "revision: abc123")
	assert.Contains(t, pinned, "targetRevision: abc123")
	// app-template's chart version is another repository's revision
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "targetRevision:") && !strings.Contains(line, "sandbox") {
			assert.Contains(t, pinned, line, "a revision of another repository changed")
		}
	}
	assert.Equal(t, strings.Count(plain, "sandbox"), strings.Count(pinned, "abc123")+strings.Count(pinned, "sandbox"),
		"every homelab revision was rewritten, and only those")
}

// Removing a directory from root must never delete its resources: when Argo CD stopped managing itself, root dropped
// Application argocd, and its resources finalizer deleted part of Argo CD before the bootstrap could release it
func TestArgoCDRootPreservesResources(t *testing.T) {
	for _, env := range []string{"", "dev"} {
		cmd := exec.Command("sh", "-c", `scripts/render-chart system/argocd "$0" | yq 'select(.kind == "ApplicationSet" and .metadata.name == "root") | .spec.syncPolicy.preserveResourcesOnDeletion'`, env)
		cmd.Dir = ".."
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		assert.Equal(t, "true", strings.TrimSpace(string(out)), "env %q", env)
	}
}
