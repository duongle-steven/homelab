package offline

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Argo Workflows (step 10f): people sign in with Kanidm only, a Workflow must reference the golden path's templates
// (an inline template is refused), and the UI is on the LAN Gateway at ci.<domain>
func TestArgoWorkflowsRender(t *testing.T) {
	for env, domain := range map[string]string{"": "hoangduong.dev", "dev": "127-0-0-1.nip.io"} {
		render := func(expr string) string { return renderVelero(t, "platform/argo-workflows", env, expr) }
		config := `select(.kind == "ConfigMap" and .metadata.name == "workflow-controller-configmap") | .data.config | from_yaml`
		assert.Equal(t, "Strict", render(config+` | .workflowRestrictions.templateReferencing`), env)
		assert.Equal(t, "https://auth."+domain+"/oauth2/openid/argo-workflows", render(config+` | .sso.issuer`), env)
		assert.Equal(t, "https://ci."+domain+"/oauth2/callback", render(config+` | .sso.redirectUrl`), env)
		assert.Equal(t, "homelab_groups", render(config+` | .sso.customGroupClaimName`), env)
		assert.Equal(t, "true", render(config+` | .sso.rbac.enabled`), env)
		// Argo's own containers in every Workflow pod (init and wait): non-root, no escalation; no seccomp profile of
		// their own, which would keep a build from getting homelab/buildah.json (platform/ci's seccomp policies)
		assert.Equal(t, `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"runAsNonRoot":true,"runAsUser":8737}`, render(config+` | .executor.securityContext | sort_keys(..) | to_json(0)`), env)
		assert.Equal(t, "workflow-controller-configmap", render(`select(.kind == "Deployment" and .metadata.name == "argo-workflows-workflow-controller") | .spec.template.spec.containers[0].args as $a | $a | to_entries | .[] | select(.value == "--configmap") | $a[.key + 1]`), env)
		assert.Equal(t, "--auth-mode=sso", render(`select(.kind == "Deployment" and .metadata.name == "argo-workflows-server") | .spec.template.spec.containers[0].args[] | select(test("^--auth-mode"))`), env, "SSO and nothing else")
		assert.Equal(t, "ci."+domain, render(`select(.kind == "HTTPRoute") | .spec.hostnames[0]`), env)
		assert.Equal(t, "istio-system/gateway", render(`select(.kind == "HTTPRoute") | .spec.parentRefs[0] | .namespace + "/" + .name`), env)
		rules := render(`select(.kind == "ServiceAccount" and .metadata.annotations."workflows.argoproj.io/rbac-rule" != null) | .metadata.name + "=" + .metadata.annotations."workflows.argoproj.io/rbac-rule"`)
		assert.ElementsMatch(t, []string{
			"ci-admin='homelab-admins' in groups",
			"ci-editor='homelab-editors' in groups",
			"ci-viewer='homelab-users' in groups",
		}, strings.Split(rules, "\n"), env)
		assert.NotContains(t, strings.ToLower(render(`.`)), "woodpecker", env)
	}
}
