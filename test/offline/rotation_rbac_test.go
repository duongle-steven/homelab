package offline

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The scheduled rotation's access to a reader's namespace is rendered by that namespace's own system/secret-store, so
// it exists exactly where the namespace does: rendered by platform/secrets-rotation into every reader's namespace, a
// reader that an environment never runs (production's delivery-test) or a sandbox run leaves out failed its whole sync
// (the app delivery review)
func TestRotationAccessLivesWithTheReader(t *testing.T) {
	readers := strings.Fields(runIn(t, `yq '.rotationReaders | keys | .[]' system/secret-store/values.yaml`))
	require.Contains(t, readers, "ntfy")
	role := `select(.kind == "Role" and .metadata.name == "secrets-rotation") | .metadata.namespace`
	binding := `select(.kind == "RoleBinding" and .metadata.name == "secrets-rotation") | .subjects[0].namespace + "/" + .subjects[0].name`
	for _, ns := range readers {
		assert.Equal(t, ns, renderSecretStoreFor(t, ns, role), "%s: no Role for the rotation", ns)
		assert.Equal(t, "secrets-rotation/secrets-rotation", renderSecretStoreFor(t, ns, binding), ns)
	}
	assert.Empty(t, renderSecretStoreFor(t, "grafana-not-a-reader", role), "a namespace that is no reader")
	// ntfy's Role names its ExternalSecret, Deployment and exec
	assert.Equal(t, "ntfy-auth", renderSecretStoreFor(t, "ntfy", `select(.kind == "Role") | .rules[] | select(.resources[0] == "externalsecrets") | .resourceNames[]`))
	assert.Contains(t, renderSecretStoreFor(t, "ntfy", `select(.kind == "Role") | .rules[].resources[]`), "pods/exec")

	// platform/secrets-rotation itself renders only into its own namespace, OpenBao's and Velero's (always there with it)
	for _, env := range []string{"", "dev"} {
		namespaces := strings.Fields(renderVelero(t, "platform/secrets-rotation", env, `select(.kind == "Role" or .kind == "RoleBinding") | .metadata.namespace`))
		for _, ns := range namespaces {
			assert.Contains(t, []string{"secrets-rotation", "openbao", "velero"}, ns, "env %q: platform/secrets-rotation renders into %s", env, ns)
		}
	}

	// It leaves out readers whose namespace is gone (lab lists the namespaces): the one cluster-wide read it has
	for _, env := range []string{"", "dev"} {
		assert.Equal(t, "namespaces list", renderVelero(t, "platform/secrets-rotation", env,
			`select(.kind == "ClusterRole") | .rules[] | .resources[0] + " " + (.verbs | join(","))`), env)
		assert.Equal(t, "secrets-rotation/secrets-rotation", renderVelero(t, "platform/secrets-rotation", env,
			`select(.kind == "ClusterRoleBinding") | .subjects[0].namespace + "/" + .subjects[0].name`), env)
	}
}
