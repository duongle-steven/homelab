package offline

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Forgejo's webhook into a Workflow (step 10f): the EventSource refuses a request without org ci's bearer secret, the
// Sensor admits a branch push that is not a deletion and a pull request opened, updated or reopened, nothing else, and
// creates only Workflows of the golden path's template, through a ServiceAccount that may do nothing else
func TestCIEventsRender(t *testing.T) {
	for env, replicas := range map[string]string{"": "3", "dev": "1"} {
		render := func(expr string) string { return renderVelero(t, "platform/ci", env, expr) }
		assert.Equal(t, "webhook/token", render(`select(.kind == "EventSource") | .spec.webhook.forgejo.authSecret | .name + "/" + .key`), env)
		assert.Equal(t, "/forgejo 12000", render(`select(.kind == "EventSource") | .spec.webhook.forgejo | .endpoint + " " + .port`), env)
		assert.Equal(t, replicas+" 2.15.3", render(`select(.kind == "EventBus") | .spec.jetstream | (.replicas | tostring) + " " + .version`), env)
		assert.Equal(t, "null", render(`select(.kind == "EventBus") | .spec.jetstream.persistence`), env, "an event lost in a restart is a push to repeat")
		// The stream's replicas follow the bus's: the default 3 cannot be created on one replica (the sandbox)
		assert.Equal(t, "replicas: "+replicas, render(`select(.kind == "EventBus") | .spec.jetstream.streamConfig | trim`), env)

		// Every filter of every dependency, as path=values (comparator)
		filters := render(`select(.kind == "Sensor") | .spec.dependencies[] | .name + ": " + ([.filters.data[] | .path + "=" + (.value | join("|")) + " " + (.comparator // "=")] | join(", "))`)
		assert.ElementsMatch(t, []string{
			"push: header.X-Forgejo-Event.0=push =, body.ref=refs/heads/.+ =, body.after=0000000000000000000000000000000000000000 !=",
			"pull-request: header.X-Forgejo-Event.0=pull_request =, body.action=opened|synchronized|reopened =",
		}, strings.Split(filters, "\n"), env)
		assert.Equal(t, "pipeline\npipeline", render(`select(.kind == "Sensor") | .spec.triggers[].template.argoWorkflow.source.resource.spec.workflowTemplateRef.name`), env)
		assert.Equal(t, "ci-sensor", render(`select(.kind == "Sensor") | .spec.template.serviceAccountName`), env)
		// Argo Events' image runs as root unless told otherwise
		assert.Equal(t, "65532\n65532", render(`select(.kind == "Sensor" or .kind == "EventSource") | .spec.template.securityContext.runAsUser`), env)
		assert.Equal(t, `[{"apiGroups":["argoproj.io"],"resources":["workflows"],"verbs":["create"]},{"apiGroups":["argoproj.io"],"resources":["workflowtemplates"],"verbs":["get"]}]`, render(`select(.kind == "Role" and .metadata.name == "ci-sensor") | .rules | to_json(0)`), env)

		secrets := render(`select(.kind == "ExternalSecret") | .metadata.name + "=" + ([.spec.data[].remoteRef.key] | join(","))`)
		assert.ElementsMatch(t, []string{"webhook=ci/generated/webhook", "forgejo=ci/forgejo-token", "registry=ci/generated/registry"}, strings.Split(secrets, "\n"), env)
	}
	// zot's machine account ci: one value, written to both readers' branches by the same push
	assert.Equal(t, "zot/generated/ci,ci/generated/registry", renderVelero(t, "platform/generated-secrets", "", `select(.kind == "PushSecret" and .metadata.name == "registry.ci") | [.spec.data[].match.remoteRef.remoteKey] | join(",")`))
}
