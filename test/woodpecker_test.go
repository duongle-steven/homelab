package test

import (
	"encoding/json"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func renderWoodpecker(t *testing.T, env, expr string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", `scripts/render-chart platform/woodpecker $1 | yq -N "$0"`, expr, env)
	cmd.Dir = ".."
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return strings.TrimSpace(string(out))
}

// docs/superpowers/specs/2026-10-21-woodpecker-design.md, decisions 1, 3 and 4
func TestWoodpeckerRender(t *testing.T) {
	statefulSet := func(name string) string {
		return `select(.kind == "StatefulSet" and .metadata.name == "` + name + `") | .spec.template.spec.containers[0]`
	}
	server, agent := statefulSet("woodpecker-server"), statefulSet("woodpecker-agent")
	for _, e := range []string{"", "dev"} {
		t.Run("env="+e, func(t *testing.T) {
			env := func(container, name string) string {
				return renderWoodpecker(t, e, container+` | .env[] | select(.name == "`+name+`") | .value`)
			}
			// The names, not the values: an unset variable and an empty one differ
			has := func(container, name string) bool {
				return renderWoodpecker(t, e, container+` | .env[] | select(.name == "`+name+`") | .name`) != ""
			}
			assert.Equal(t, "ci", env(server, "WOODPECKER_ORGS"))
			assert.Equal(t, "true", env(server, "WOODPECKER_FORGEJO"))
			assert.Equal(t, "http://woodpecker-server.woodpecker", env(server, "WOODPECKER_EXPERT_WEBHOOK_HOST"))
			assert.False(t, has(server, "WOODPECKER_WEBHOOK_HOST"), "Woodpecker 2's name")
			assert.False(t, has(server, "WOODPECKER_PLUGINS_PRIVILEGED"))
			// env wins over envFrom: the chart's placeholder host would hide woodpecker-environment's
			assert.False(t, has(server, "WOODPECKER_HOST"))
			assert.Equal(t, "woodpecker-environment,woodpecker-secret",
				renderWoodpecker(t, e, server+` | [.envFrom[].secretRef.name] | sort | join(",")`))
			assert.Equal(t, "woodpecker-agent", renderWoodpecker(t, e, agent+` | [.envFrom[].secretRef.name] | join(",")`))
			assert.Equal(t, "true", env(agent, "WOODPECKER_BACKEND_K8S_USER_NAMESPACES"))
			for _, allow := range []string{"SERVICE_ACCOUNT_NAME", "POD_NODE_SELECTOR", "POD_LABELS", "POD_ANNOTATIONS",
				"POD_AFFINITY", "RUNTIME_CLASS", "POD_TOLERATIONS"} {
				assert.Equal(t, "false", env(agent, "WOODPECKER_BACKEND_K8S_"+allow+"_ALLOW_FROM_STEP"), allow)
			}
			assert.Equal(t, "false", env(agent, "WOODPECKER_BACKEND_K8S_ALLOW_NATIVE_SECRETS"))
			assert.Equal(t, "false", renderWoodpecker(t, e, `select(.kind == "ServiceAccount" and .metadata.name == "default") | .automountServiceAccountToken`))
			// The chart's own agent Secret is not rendered: the value comes from OpenBao
			assert.Empty(t, renderWoodpecker(t, e, `select(.kind == "Secret" and .metadata.name == "woodpecker-default-agent-secret") | .metadata.name`))
			assert.Equal(t, "woodpecker/generated/agent", renderWoodpecker(t, e,
				`select(.kind == "ExternalSecret" and .metadata.name == "woodpecker-agent") | .spec.data[0].remoteRef.key`))
		})
	}
	assert.Empty(t, renderWoodpecker(t, "", server+` | .env[] | select(.name | test("SKIP_VERIFY")) | .name`),
		"production verifies certificates")
	assert.Equal(t, "1", renderWoodpecker(t, "dev", `select(.kind == "StatefulSet" and .metadata.name == "woodpecker-agent") | .spec.replicas`))
}

// Forgejo's Kanidm login source makes homelab-admins owners and homelab-editors developers of org ci, and drops a
// membership the claim no longer grants (spec decision 1)
func TestWoodpeckerForgejoTeams(t *testing.T) {
	cmd := exec.Command("sh", "-c", `scripts/render-chart platform/forgejo | grep -E "gitea admin auth (add|update)-oauth"`)
	cmd.Dir = ".."
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	require.Len(t, lines, 2, "the init script adds or updates the login source")
	mapping := regexp.MustCompile(`--group-team-map '([^']*)'`)
	for _, line := range lines {
		m := mapping.FindStringSubmatch(line)
		require.NotNil(t, m, "no --group-team-map in %s", line)
		var teams map[string]map[string][]string
		require.NoError(t, json.Unmarshal([]byte(m[1]), &teams))
		assert.Equal(t, map[string]map[string][]string{
			"homelab-admins":  {"ci": {"Owners"}},
			"homelab-editors": {"ci": {"developers"}},
		}, teams)
		assert.Contains(t, line, "--group-team-map-removal")
	}
}
