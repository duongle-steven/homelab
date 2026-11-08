package argocd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnsynced(t *testing.T) {
	raw := []byte(`{"items":[
		{"metadata":{"name":"loki"},"status":{"sync":{"status":"Synced"},"health":{"status":"Healthy"}}},
		{"metadata":{"name":"kanidm"},"status":{"sync":{"status":"OutOfSync"},"health":{"status":"Healthy"}}},
		{"metadata":{"name":"zot"},"status":{"sync":{"status":"Synced"},"health":{"status":"Progressing"}}},
		{"metadata":{"name":"grafana"},"status":{"sync":{"status":"Synced"},"health":{"status":"Degraded"}}},
		{"metadata":{"name":"new"},"status":{}}]}`)
	waiting, err := Unsynced(raw)
	require.NoError(t, err)
	// Degraded is settled (a broken app fails its own specs); Progressing and OutOfSync are still coming
	assert.Equal(t, []string{"kanidm OutOfSync/Healthy", "new /", "zot Synced/Progressing"}, waiting)

	waiting, err = Unsynced([]byte(`{"items":[]}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"no Application yet"}, waiting)

	_, err = Unsynced([]byte(`not json`))
	assert.Error(t, err)
}
