package e2e

import (
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/duongle-steven/homelab/test/internal/smoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var kialiHost = "kiali." + suiteDomain

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
		app := smoke.Apps("dev")[0]
		host := routeHost(t, app.Namespace, app.Name)
		assert.Eventually(t, func() bool {
			appCodes(map[string]string{app.Name: host})
			var graph struct {
				Elements struct {
					Edges []any `json:"edges"`
				} `json:"elements"`
			}
			status, err := getJSONE("GET", base+"/api/namespaces/graph?graphType=workload&duration=600s&namespaces="+app.Namespace+",istio-system",
				cookies, "", "", nil, &graph)
			return err == nil && status == http.StatusOK && len(graph.Elements.Edges) > 0
		}, 5*time.Minute, 15*time.Second, "Kiali's graph has no edge")

		// A change to an Istio object is refused: Kiali's role only reads (the API server refuses it), and view_only_mode
		// hides the UI's write actions
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
