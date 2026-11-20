package offline

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// docs/superpowers/specs/2026-11-20-sandbox-groups-design.md: every CRD comes from system/crds, generated from each
// component's pinned chart; no component installs its own (Argo CD's stay with the bootstrap's system/argocd)
func TestNoComponentShipsCRDs(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join(repo.Root(), "system", "*", "Chart.yaml"))
	require.NoError(t, err)
	more, err := filepath.Glob(filepath.Join(repo.Root(), "platform", "*", "Chart.yaml"))
	require.NoError(t, err)
	for _, chart := range append(dirs, more...) {
		dir, _ := filepath.Rel(repo.Root(), filepath.Dir(chart))
		if dir == "system/argocd" || dir == "system/crds" {
			continue
		}
		// rook-ceph's ceph-csi-operator templates its csi.ceph.io CRDs with no switch (rook-ceph never runs in the sandbox)
		skip := map[string]string{"system/rook-ceph": ` and .spec.group != "csi.ceph.io"`}[dir]
		for _, env := range []string{"", "dev"} {
			assert.Empty(t, renderVelero(t, dir, env, `select(.kind == "CustomResourceDefinition"`+skip+`) | .metadata.name`), "%s (%s) installs CRDs", dir, env)
		}
	}
}

func TestCRDsInOnePlace(t *testing.T) {
	names := strings.Fields(renderVelero(t, "system/crds", "", `select(.kind == "CustomResourceDefinition") | .metadata.name`))
	for _, want := range []string{"certificates.cert-manager.io", "externalsecrets.external-secrets.io", "policyexceptions.kyverno.io",
		"servicemonitors.monitoring.coreos.com", "authorizationpolicies.security.istio.io", "kanidmoauth2clients.kaniop.rs",
		"clusters.postgresql.cnpg.io", "workflows.argoproj.io", "backups.velero.io", "volumesnapshots.snapshot.storage.k8s.io"} {
		assert.Contains(t, names, want)
	}
	seen := map[string]bool{}
	for _, n := range names {
		assert.False(t, seen[n], "%s twice", n)
		seen[n] = true
	}
	// Never pruned: deleting a CRD deletes every object of its kind in the cluster
	options := strings.Fields(renderVelero(t, "system/crds", "", `select(.kind == "CustomResourceDefinition") | .metadata.annotations["argocd.argoproj.io/sync-options"]`))
	assert.Len(t, options, len(names))
	for _, o := range options {
		assert.Contains(t, o, "Prune=false")
	}
}

// A chart bumped without its CRDs regenerated fails the basic check, naming the command
func TestVendorCRDsCheck(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("git", "worktree", "add", "--detach", dir)
	cmd.Dir = repo.Root()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	t.Cleanup(func() {
		c := exec.Command("git", "worktree", "remove", "--force", dir)
		c.Dir = repo.Root()
		_ = c.Run()
	})
	// The working tree's state, not HEAD's
	for _, p := range []string{"scripts/vendor-crds", "system/crds"} {
		require.NoError(t, exec.Command("cp", "-r", filepath.Join(repo.Root(), p), filepath.Dir(filepath.Join(dir, p))).Run())
	}
	file := filepath.Join(dir, "system", "crds", "files", "kyverno", "policyexceptions.kyverno.io.yaml")
	raw, err := os.ReadFile(file)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, []byte(strings.Replace(string(raw), "kind: CustomResourceDefinition", "kind: CustomResourceDefinitionX", 1)), 0o644))
	check := exec.Command("scripts/vendor-crds", "--check")
	check.Dir = dir
	out, err = check.CombinedOutput()
	assert.Error(t, err)
	assert.Contains(t, string(out), "scripts/vendor-crds kyverno")
}
