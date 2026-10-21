package test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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

// ESO's controller mints the token (GithubAccessToken calls api.github.com from namespace external-secrets), so that
// namespace reaches GitHub, not only Renovate's
func TestRenovateTokenEgress(t *testing.T) {
	cmd := exec.Command("yq", `.internet."external-secrets"[].port`, "system/egress/values.yaml")
	cmd.Dir = ".."
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Contains(t, strings.Fields(string(out)), "443")
}

// Review findings: the token covers every repository the App is installed on (adding one is a change on GitHub
// only), Renovate's image follows its chart (no frozen tag override), and Renovate reads its schedules in the
// environment's time zone, so lockFileMaintenance's "before 5am on monday" matches the 05:00 run
func TestRenovateConfig(t *testing.T) {
	assert.Equal(t, "null", renderRenovate(t, `select(.kind == "GithubAccessToken") | .spec.repositories`))
	values, err := exec.Command("yq", ".renovate.image", "../platform/renovate/values.yaml").CombinedOutput()
	require.NoError(t, err, string(values))
	assert.Equal(t, "null", strings.TrimSpace(string(values)), "an image override freezes Renovate's image when its chart moves")

	global := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(renderRenovate(t, `select(.kind == "ConfigMap" and .metadata.name == "renovate-config") | .data["config.json"]`)), &global))
	timezone, err := exec.Command("jq", "-r", ".timezone", "../infra/production/cluster/config.json").CombinedOutput()
	require.NoError(t, err, string(timezone))
	assert.Equal(t, strings.TrimSpace(string(timezone)), global["timezone"], "Renovate's schedules run in UTC otherwise")
	assert.Equal(t, strings.TrimSpace(string(timezone)), renderRenovate(t, `select(.kind == "CronJob") | .spec.timeZone`))
}

// Renovate finds what this repository pins: every app file's images (but our own registry's), the app-template chart in each of the root
// values files, and the flake's inputs (renovate --platform=local, extraction only: no network, no token)
func TestRenovateManagers(t *testing.T) {
	report := filepath.Join(t.TempDir(), "extract.json")
	cmd := exec.Command("renovate", "--report-type=file", "--report-path="+report)
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(), "LOG_LEVEL=warn", "RENOVATE_PLATFORM=local", "RENOVATE_DRY_RUN=extract",
		"RENOVATE_ONBOARDING=false", "RENOVATE_REQUIRE_CONFIG=optional")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	raw, err := os.ReadFile(report)
	require.NoError(t, err)
	var extract struct {
		Repositories map[string]struct {
			PackageFiles map[string][]struct {
				PackageFile string `json:"packageFile"`
				Deps        []struct {
					DepName      string `json:"depName"`
					CurrentValue string `json:"currentValue"`
				} `json:"deps"`
			} `json:"packageFiles"`
		} `json:"repositories"`
	}
	require.NoError(t, json.Unmarshal(raw, &extract))
	deps := map[string]map[string]string{} // package file → dependency → version
	managers := map[string]bool{}
	for _, repo := range extract.Repositories {
		for manager, files := range repo.PackageFiles {
			managers[manager] = true
			for _, f := range files {
				if deps[f.PackageFile] == nil {
					deps[f.PackageFile] = map[string]string{}
				}
				for _, d := range f.Deps {
					deps[f.PackageFile][d.DepName] = d.CurrentValue
				}
			}
		}
	}

	apps, err := filepath.Glob("../apps/*.yaml")
	require.NoError(t, err)
	for _, app := range apps {
		// A templated repository is our own registry (blog), which Renovate cannot look up
		images, err := exec.Command("yq", `[.. | select(has("repository") and has("tag")) | .repository | select(test("[{]") | not)] | .[]`, app).CombinedOutput()
		require.NoError(t, err, string(images))
		file := strings.TrimPrefix(app, "../")
		for _, image := range strings.Fields(string(images)) {
			assert.Contains(t, deps[file], image, "%s: image %s is not managed", file, image)
		}
	}
	for _, file := range []string{"system/argocd/values.yaml", "system/argocd/values-dev.yaml", "system/argocd/values-seed.yaml"} {
		assert.Contains(t, deps[file], "app-template", "%s: the app-template pin is not managed", file)
	}
	assert.True(t, managers["nix"], "flake.lock is not managed")
	assert.Contains(t, deps["infra/_modules/k3d/k3d.yaml"], "rancher/k3s", "the sandbox's k3s is not managed")
	assert.Contains(t, deps["infra/_modules/k3d/main.tf"], "kubernetes-sigs/gateway-api", "the Gateway API CRDs are not managed")
}

// A Renovate pull request labelled sandbox runs it again when Renovate pushes to it (a rebase, a newer version), so
// its result is never stale; anyone else's new commit still needs the label again. The bot is Renovate's gitAuthor.
func TestRenovateLabelRerun(t *testing.T) {
	types, err := exec.Command("yq", `.on.pull_request.types | join(",")`, "../.github/workflows/sandbox-label.yaml").CombinedOutput()
	require.NoError(t, err, string(types))
	assert.Equal(t, "labeled,synchronize", strings.TrimSpace(string(types)))
	global := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(renderRenovate(t, `select(.kind == "ConfigMap" and .metadata.name == "renovate-config") | .data["config.json"]`)), &global))
	bot := strings.Fields(global["gitAuthor"].(string))[0]
	workflow, err := os.ReadFile("../.github/workflows/sandbox-label.yaml")
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(string(workflow), "'"+bot+"'"), "the job's if and its concurrency group name the bot %s", bot)
}
