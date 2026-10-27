package test

import (
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const kialiHost = "kiali.127-0-0-1.nip.io"

// renderKiali renders system/kiali for env ("" or dev) and runs a yq expression on it
func renderKiali(t *testing.T, env, expr string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", `scripts/render-chart system/kiali $1 | yq -N "$0"`, expr, env)
	cmd.Dir = ".."
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
				`select(.kind == "ClusterRole") | [.rules[] | select(.resources[] != "pods/portforward" and .resources[] != "tokenreviews") | .verbs[]] | unique | join(",")`),
				"read-only apart from config dumps and token reviews")
		})
	}
}

// An admin signs in through Kanidm and sees the mesh; Kiali refuses to change anything; nobody else gets in
func TestSSOKiali(t *testing.T) {
	skipUnlessSandbox(t)
	base := "https://" + kialiHost + "/kiali"
	waitForApp(t, base+"/healthz")
	start := base + "/api/auth/openid_redirect"

	t.Run("dev-admin", func(t *testing.T) {
		cookies := appLogin(t, "dev-admin", ensureCredentials(t, "dev-admin"), start, kialiHost)
		var namespaces []struct {
			Name string `json:"name"`
		}
		require.Equal(t, http.StatusOK, getJSON(t, "GET", base+"/api/namespaces", cookies, "", "", nil, &namespaces))
		var names []string
		for _, n := range namespaces {
			names = append(names, n.Name)
		}
		assert.Contains(t, names, "kiali")

		// The graph has edges: ztunnel's metrics (system/istio-system's PodMonitors) reach Kiali through Prometheus
		app := smokeApps("dev")[0]
		host := routeHost(t, app.namespace, app.name)
		assert.Eventually(t, func() bool {
			appCodes(map[string]string{app.name: host})
			var graph struct {
				Elements struct {
					Edges []any `json:"edges"`
				} `json:"elements"`
			}
			status, err := getJSONE("GET", base+"/api/namespaces/graph?graphType=workload&duration=600s&namespaces="+app.namespace+",istio-system",
				cookies, "", "", nil, &graph)
			return err == nil && status == http.StatusOK && len(graph.Elements.Edges) > 0
		}, 5*time.Minute, 15*time.Second, "Kiali's graph has no edge")

		// view_only_mode: a change to an Istio object is refused
		status, _ := getJSONE("PATCH", base+"/api/namespaces/istio-system/istio/security.istio.io/v1/AuthorizationPolicy/deny-all",
			cookies, "", "", map[string]any{"metadata": map[string]any{"labels": map[string]string{"kiali-test": "x"}}}, nil)
		assert.NotEqual(t, http.StatusOK, status, "Kiali changed an AuthorizationPolicy")
		label, err := exec.Command("kubectl", "-n", "istio-system", "get", "authorizationpolicies.security.istio.io", "deny-all",
			"-o", "jsonpath={.metadata.labels.kiali-test}").CombinedOutput()
		require.NoError(t, err, string(label))
		assert.Empty(t, strings.TrimSpace(string(label)), "the AuthorizationPolicy got Kiali's label")
	})
	for _, account := range []string{"dev-editor", "dev-viewer", "dev-outsider"} {
		t.Run(account, func(t *testing.T) {
			assert.Contains(t, browserDenied(t, account, ensureCredentials(t, account), start), "Access Denied")
		})
	}
}
