package test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func renderRenovate(t *testing.T, expr string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", `scripts/render-chart platform/renovate | yq -N "$0"`, expr)
	cmd.Dir = ".."
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return strings.TrimSpace(string(out))
}

// Renovate's token comes from our GitHub App, scoped to homelab and minted again every 30 minutes (never created
// once: it expires after an hour); its CronJob runs at 05:00 local time
func TestRenovateGitHubApp(t *testing.T) {
	assert.Equal(t, `{"checks":"read","contents":"write","issues":"write","pull_requests":"write","statuses":"read","workflows":"write"}`,
		renderRenovate(t, `select(.kind == "GithubAccessToken") | .spec.permissions | to_entries | sort_by(.key) | from_entries | to_json(0)`))
	assert.Equal(t, `["homelab"]`, renderRenovate(t, `select(.kind == "GithubAccessToken") | .spec.repositories | to_json(0)`))
	token := `select(.kind == "ExternalSecret" and .spec.target.name == "renovate-secret")`
	assert.Equal(t, "30m", renderRenovate(t, token+` | .spec.refreshInterval`))
	assert.Contains(t, []string{"Periodic", "null"}, renderRenovate(t, token+` | .spec.refreshPolicy`))
	assert.Equal(t, "GithubAccessToken", renderRenovate(t, token+` | .spec.dataFrom[0].sourceRef.generatorRef.kind`))
	assert.Equal(t, "0 5 * * *", renderRenovate(t, `select(.kind == "CronJob") | .spec.schedule`))
	assert.Equal(t, "Asia/Ho_Chi_Minh", renderRenovate(t, `select(.kind == "CronJob") | .spec.timeZone`))
	all := renderRenovate(t, `.`)
	assert.NotContains(t, all, "forgejo")
	assert.NotContains(t, all, "renovate/forgejo")
}

// Renovate runs in production only
func TestRenovateNotInTheSandbox(t *testing.T) {
	cmd := exec.Command("yq", `.. | select(has("path")) | .path`, "system/argocd/values-dev.yaml")
	cmd.Dir = ".."
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.NotContains(t, string(out), "platform/renovate")
}
