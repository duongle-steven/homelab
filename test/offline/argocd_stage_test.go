package offline

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/render"
	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Step 12b-2: a new cluster's first deploy may stop at the platform. A label homelab/stage on Argo CD's cluster Secret
// gates whatever deploys an app; it only moves forward, and a deploy that does not name it keeps the cluster's

func TestResolveStage(t *testing.T) {
	script := filepath.Join(repo.Root(), "infra", "_modules", "argocd", "resolve-stage.sh")
	for _, tc := range []struct{ requested, current, want string }{
		{"", "", "all"},
		{"", "platform", "platform"},
		{"", "all", "all"},
		{"platform", "", "platform"},
		{"all", "platform", "all"},
		{"platform", "platform", "platform"},
		{"all", "all", "all"},
		{"platform", "all", ""},
		{"bogus", "", ""},
	} {
		out, err := exec.Command("sh", script, tc.requested, tc.current).CombinedOutput()
		if tc.want == "" {
			assert.Error(t, err, "requested %q on %q: %s", tc.requested, tc.current, out)
			continue
		}
		require.NoError(t, err, "requested %q on %q: %s", tc.requested, tc.current, out)
		assert.Equal(t, tc.want, strings.TrimSpace(string(out)), "requested %q on %q", tc.requested, tc.current)
	}
	out, _ := exec.Command("sh", script, "platform", "all").CombinedOutput()
	assert.Contains(t, string(out), "only moves forward")
}

func TestClusterSecretStage(t *testing.T) {
	script := filepath.Join(repo.Root(), "infra", "_modules", "argocd", "cluster-secret.sh")
	run := func(env ...string) (string, error) {
		cmd := exec.Command("sh", script)
		cmd.Env = append(os.Environ(), append([]string{"REPOSITORY=https://example.test/homelab", "BRANCH=master"}, env...)...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := run("STAGE=platform")
	require.NoError(t, err, out)
	assert.Contains(t, out, "homelab/stage: platform")
	_, err = run()
	assert.Error(t, err, "the bootstrap always resolves a stage")
}

// Every generator that deploys an app selects only a cluster at stage all; the others are unchanged
func TestStageGatesApps(t *testing.T) {
	// What each environment's root deploys behind the stage: production (and its seed) Matrix and the public scan, the
	// sandbox the public scan, the testbed neither (it runs no app)
	gatedByEnv := map[string][]string{"": {"apps/*", "system/public-scan"}, "seed": {"apps/*", "system/public-scan"},
		"dev": {"system/public-scan"}, "testbed": nil}
	for _, env := range []string{"", "dev", "testbed", "seed"} {
		yq := func(expr string) []string {
			return lines(render.Yq(t, "system/argocd", env, `select(.kind == "ApplicationSet") | `+expr))
		}
		apps := yq(`select(.metadata.name == "apps") | .. | select(has("clusters")) | .clusters.selector.matchLabels["homelab/stage"] // "missing"`)
		for _, stage := range apps {
			assert.Equal(t, "all", stage, "env=%q: the apps ApplicationSet's cluster generator", env)
		}
		// root's generators: each matrix's own cluster stage and the directories it lists
		gated := yq(`select(.metadata.name == "root") | .spec.generators[] | select(.matrix.generators[0].clusters.selector.matchLabels["homelab/stage"] == "all") | .. | select(has("directories")) | .directories[] | select(.exclude != true) | .path`)
		assert.ElementsMatch(t, gatedByEnv[env], gated, "env=%q: root's gated generator", env)
		ungated := yq(`select(.metadata.name == "root") | .spec.generators[] | select(.matrix.generators[0].clusters.selector.matchLabels["homelab/stage"] != "all") | .. | select(has("directories")) | .directories[] | select(.exclude != true) | .path`)
		for _, path := range []string{"apps/*", "apps/matrix", "system/public-scan"} {
			assert.NotContains(t, ungated, path, "env=%q: an ungated generator deploys %s", env, path)
		}
		if slices.Contains(ungated, "system/*") {
			excluded := yq(`select(.metadata.name == "root") | .spec.generators[] | select(.matrix.generators[0].clusters.selector.matchLabels["homelab/stage"] != "all") | .. | select(has("directories")) | .directories[] | select(.exclude == true) | .path`)
			assert.Contains(t, excluded, "system/public-scan", "env=%q: system/* must exclude the public scan", env)
		}
	}
}

// The cluster's current stage is read or the bootstrap stops: a cluster that cannot be asked must never look like a new
// one, which would move a platform cluster to all (12b-2's review)
func TestCurrentStage(t *testing.T) {
	script := filepath.Join(repo.Root(), "infra", "_modules", "argocd", "current-stage.sh")
	run := func(kubectl string) (string, error) {
		_, env := fakeBin(t, map[string]string{"kubectl": kubectl})
		cmd := exec.Command("sh", script)
		cmd.Env = env
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	out, err := run(`echo '{"metadata":{"labels":{"homelab/stage":"platform"}}}'`)
	require.NoError(t, err)
	assert.Equal(t, "platform", out)
	out, err = run(`true`) // --ignore-not-found: no Secret yet, a new cluster
	require.NoError(t, err)
	assert.Equal(t, "", out)
	_, err = run(`echo 'Unable to connect to the server' >&2; exit 1`)
	assert.Error(t, err, "an API error must stop the bootstrap, not read as a new cluster")
	refused, err := exec.Command("sh", filepath.Join(repo.Root(), "infra", "_modules", "argocd", "resolve-stage.sh"), "", "bogus").CombinedOutput()
	assert.Error(t, err)
	assert.Contains(t, string(refused), "unknown", "a label resolve-stage does not know is refused")
}
