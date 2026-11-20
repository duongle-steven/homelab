package offline

import (
	"testing"

	"github.com/duongle-steven/homelab/test/internal/scopes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The specs read which groups they need from test/scopes.yaml, the same map just dev deploys from
func TestScopesGroups(t *testing.T) {
	m, err := scopes.Load()
	require.NoError(t, err)
	assert.Equal(t, []string{"observability"}, m.GroupsOfLabels([]string{"sandbox", "testbed", "logs"}))
	assert.Equal(t, []string{"backup", "observability"}, m.GroupsOfLabels([]string{"sandbox", "rotation", "alone"}))
	assert.Empty(t, m.GroupsOfLabels([]string{"sandbox", "smoke"}))
	assert.Equal(t, "paperless", m.GroupOfNamespace("paperless"))
	assert.Equal(t, "observability", m.GroupOfNamespace("grafana"))
	assert.Equal(t, "ci", m.GroupOfNamespace("zot"))
	assert.Equal(t, "", m.GroupOfNamespace("kanidm"))
	assert.Equal(t, "", m.GroupOfNamespace("argocd"))
}
