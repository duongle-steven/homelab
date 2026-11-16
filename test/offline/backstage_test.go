package offline

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// platform/backstage (Backstage-1, docs/superpowers/specs/2026-11-16-backstage-design.md): the LAN Gateway only, a
// Kubernetes reader without Secrets, logs or exec, one PostgreSQL instance never backed up, the catalog read from
// the repository Argo CD syncs, and in the sandbox only when asked
func TestBackstageRender(t *testing.T) {
	const chart = "platform/backstage"
	for _, env := range []string{"", "dev"} {
		route := `select(.kind == "HTTPRoute" and .metadata.name == "backstage") | .spec`
		assert.Equal(t, "istio-system/gateway", renderVelero(t, chart, env, route+` | [.parentRefs[] | .namespace + "/" + .name] | join(",")`), "env %q", env)

		rules := renderVelero(t, chart, env, `select(.kind == "ClusterRole" and .metadata.name == "backstage-read") | .rules | @json`)
		assert.NotEmpty(t, rules, "env %q: no ClusterRole backstage-read", env)
		for _, forbidden := range []string{`"secrets"`, `"pods/log"`, `"pods/exec"`, `"*"`} {
			assert.NotContains(t, rules, forbidden, "env %q", env)
		}
		for _, verb := range []string{`"create"`, `"update"`, `"patch"`, `"delete"`} {
			assert.NotContains(t, rules, verb, "env %q", env)
		}

		db := `select(.kind == "Cluster" and .metadata.name == "backstage-db") | .spec`
		assert.Equal(t, "1", renderVelero(t, chart, env, db+` | .instances`), "env %q", env)
		assert.Equal(t, "none", renderVelero(t, chart, env, db+` | .inheritedMetadata.labels."homelab/backup"`), "env %q", env)
	}

	env := func(e, name string) string {
		return renderVelero(t, chart, e, `select(.kind == "Deployment" and .metadata.name == "backstage") | .spec.template.spec.containers[0].env[] | select(.name == "`+name+`") | .value`)
	}
	// What Argo CD syncs: production's ops/homelab on Forgejo, the sandbox's GitHub branch
	assert.Equal(t, "http://forgejo-http.forgejo:3000/ops/homelab/src/branch/master/catalog-info.yaml", env("", "CATALOG_LOCATION"))
	assert.Equal(t, "https://github.com/duongle-steven/homelab/blob/sandbox/catalog-info.yaml", env("dev", "CATALOG_LOCATION"))
	assert.Equal(t, "https://github.com/duongle-steven/homelab/blob/sandbox/backstage/templates/ci-repository/template.yaml", env("dev", "TEMPLATES_LOCATION"))
	assert.Equal(t, "https://backstage.127-0-0-1.nip.io", env("dev", "BACKSTAGE_BASE_URL"))

	// The sandbox syncs it only when the cluster Secret asks (just dev --apps backstage)
	root := renderVelero(t, "system/argocd", "dev", `select(.kind == "ApplicationSet" and .metadata.name == "root") | .spec.generators | @json`)
	i := strings.Index(root, `"path":"platform/backstage"`)
	if assert.GreaterOrEqual(t, i, 0, "the sandbox's root ApplicationSet never selects platform/backstage") {
		assert.Contains(t, root[max(0, i-400):i], `"homelab/app-backstage":"true"`, "platform/backstage without its label")
	}
}
