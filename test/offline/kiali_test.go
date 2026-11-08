package offline

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renderKiali renders system/kiali for env ("" or dev) and runs a yq expression on it
func renderKiali(t *testing.T, env, expr string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", `scripts/render-chart system/kiali $1 | yq -N "$0"`, expr, env)
	cmd.Dir = repo.Root()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())
	return strings.TrimSpace(string(out))
}

// docs/superpowers/specs/2026-10-27-kiali-design.md: read-only, admins only, on the LAN Gateway only, no volume
func TestKialiRender(t *testing.T) {
	for _, e := range []string{"", "dev"} {
		t.Run("env="+e, func(t *testing.T) {
			assert.Equal(t, "gateway", renderKiali(t, e, `[select(.kind == "HTTPRoute")] | map(.spec.parentRefs[].name) | join(",")`),
				"exactly one route, on the LAN Gateway")
			config := `select(.kind == "ConfigMap" and .metadata.name == "kiali") | .data["config.yaml"] | from_yaml`
			assert.Equal(t, "true", renderKiali(t, e, config+` | .deployment.view_only_mode`))
			assert.Equal(t, "openid", renderKiali(t, e, config+` | .auth.strategy`))
			assert.Equal(t, "true", renderKiali(t, e, config+` | .auth.openid.disable_rbac`))
			assert.Equal(t, "homelab-admins", renderKiali(t, e, `select(.kind == "KanidmOAuth2Client") | [.spec.scopeMap[].group] | join(",")`))
			assert.Empty(t, renderKiali(t, e, `select(.kind == "PersistentVolumeClaim") | .metadata.name`))
			assert.Equal(t, "get,list,watch", renderKiali(t, e,
				`select(.kind == "ClusterRole") | [.rules[] | select(.resources[] != "tokenreviews") | .verbs[]] | unique | join(",")`),
				"cluster-wide: read-only apart from token reviews")
			// A port-forward reaches a pod past every AuthorizationPolicy and NetworkPolicy: only istio-system's, for
			// Envoy's and ztunnel's config dumps
			assert.Equal(t, "istio-system", renderKiali(t, e,
				`[select(.rules[]?.resources[]? == "pods/portforward")] | map(.kind + ":" + (.metadata.namespace // "")) | join(",") | sub("Role:", "")`))
			assert.Equal(t, "Role:kiali-portforward", renderKiali(t, e,
				`select(.kind == "RoleBinding" and .metadata.namespace == "istio-system") | .roleRef.kind + ":" + .roleRef.name`))
			assert.Empty(t, renderKiali(t, e, `select(.kind == "AuthorizationPolicy") | .spec.rules[] | select(.to[]?.operation.ports[]? == "9090") | .to`),
				"nothing scrapes Kiali")
		})
	}
}
