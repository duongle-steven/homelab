package offline

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renderKargo renders platform/kargo for env ("" or dev) and runs a yq expression on it
func renderKargo(t *testing.T, env, expr string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", `scripts/render-chart platform/kargo $1 | yq -N "$0"`, expr, env)
	cmd.Dir = repo.Root()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())
	return strings.TrimSpace(string(out))
}

// docs/superpowers/specs/2026-11-18-app-delivery-design.md: Kargo on the LAN only, signing in with Kanidm (admins
// administer, editors only see), its credentials for ops/releases and zot only, an alert on failed promotions
func TestKargoRender(t *testing.T) {
	for _, e := range []string{"", "dev"} {
		t.Run("env="+e, func(t *testing.T) {
			domain := map[string]string{"": "hoangduong.dev", "dev": "127-0-0-1.nip.io"}[e]
			assert.Equal(t, "gateway", renderKargo(t, e, `[select(.kind == "HTTPRoute")] | map(.spec.parentRefs[].name) | join(",")`))
			assert.Equal(t, "kargo."+domain, renderKargo(t, e, `select(.kind == "HTTPRoute") | .spec.hostnames[0]`))

			api := `select(.kind == "ConfigMap" and .metadata.name == "kargo-api") | .data`
			assert.Equal(t, "true", renderKargo(t, e, api+` | .OIDC_ENABLED`))
			assert.Equal(t, "kargo", renderKargo(t, e, api+` | .OIDC_CLIENT_ID`))
			assert.Equal(t, "https://auth."+domain+"/oauth2/openid/kargo",
				renderKargo(t, e, `select(.kind == "ConfigMap" and .metadata.name == "kargo-homelab") | .data.issuer`))
			assert.Equal(t, "kargo-homelab", renderKargo(t, e,
				`select(.kind == "Deployment" and .metadata.name == "kargo-api") | .spec.template.spec.containers[0].env[] | select(.name == "OIDC_ISSUER_URL") | .valueFrom.configMapKeyRef.name`))
			assert.Empty(t, renderKargo(t, e, `select(.kind == "Secret" and .metadata.name == "kargo-api") | .data.ADMIN_ACCOUNT_PASSWORD_HASH // ""`), "no local admin")

			client := `select(.kind == "KanidmOAuth2Client" and .metadata.name == "kargo")`
			assert.Equal(t, "true", renderKargo(t, e, client+` | .spec.public`))
			assert.Equal(t, "homelab-admins,homelab-editors", renderKargo(t, e, client+` | [.spec.scopeMap[].group] | join(",")`))

			assert.Equal(t, "none", renderKargo(t, e,
				`select(.kind == "Deployment" and .metadata.name == "kargo-webhooks-server") | .spec.template.metadata.labels["istio.io/dataplane-mode"]`))
			assert.Empty(t, renderKargo(t, e, `select(.kind == "Namespace") | .metadata.name`), "Kargo's own namespaces would have no SecretStore")
			assert.Empty(t, renderKargo(t, e, `select(.kind == "Deployment" and .metadata.name == "kargo-external-webhooks-server") | .metadata.name`))

			assert.Equal(t, "http://forgejo-http.forgejo:3000/ops/releases", renderKargo(t, e,
				`select(.kind == "ExternalSecret" and .spec.target.template.metadata.labels["kargo.akuity.io/cred-type"] == "git") | .spec.target.template.data.repoURL`))
			assert.Equal(t, "registry."+domain, renderKargo(t, e,
				`select(.kind == "ExternalSecret" and .spec.target.template.metadata.labels["kargo.akuity.io/cred-type"] == "image") | .spec.target.template.data.repoURL`))

			for _, d := range strings.Fields(renderKargo(t, e, `select(.kind == "Deployment") | .metadata.name`)) {
				assert.Equal(t, "RuntimeDefault false", renderKargo(t, e,
					`select(.kind == "Deployment" and .metadata.name == "`+d+`") | .spec.template.spec.containers[0].securityContext | .seccompProfile.type + " " + (.allowPrivilegeEscalation | tostring)`), d)
			}
			assert.Equal(t, "KargoPromotionFailed", renderKargo(t, e,
				`select(.kind == "PrometheusRule") | .spec.groups[].rules[] | select(.alert == "KargoPromotionFailed") | .alert`))
		})
	}
}
