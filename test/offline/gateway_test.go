package offline

import (
	"encoding/json"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/routes"
	"github.com/duongle-steven/homelab/test/internal/testenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatewayRouteReady(t *testing.T) {
	both := `{"type":"Accepted","status":"True"},{"type":"ResolvedRefs","status":"True"}`
	cases := map[string]struct {
		route string
		want  bool
	}{
		"accepted with resolved backends": {`{"spec":{"hostnames":["a.example"]},"status":{"parents":[{"conditions":[` + both + `]}]}}`, true},
		"not reconciled yet":              {`{"spec":{"hostnames":["a.example"]}}`, false},
		"backend missing":                 {`{"spec":{"hostnames":["a.example"]},"status":{"parents":[{"conditions":[{"type":"Accepted","status":"True"},{"type":"ResolvedRefs","status":"False"}]}]}}`, false},
		"one parent refused":              {`{"spec":{"hostnames":["a.example"]},"status":{"parents":[{"conditions":[` + both + `]},{"conditions":[{"type":"Accepted","status":"False"},{"type":"ResolvedRefs","status":"True"}]}]}}`, false},
		"no hostname":                     {`{"status":{"parents":[{"conditions":[` + both + `]}]}}`, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var r routes.HTTPRoute
			require.NoError(t, json.Unmarshal([]byte(c.route), &r))
			assert.Equal(t, c.want, r.Ready())
		})
	}
}

func TestGatewayNodeAddressFromInspect(t *testing.T) {
	addr, err := testenv.NodeAddressFromInspect("172.18.0.2\n")
	require.NoError(t, err)
	assert.Equal(t, "172.18.0.2", addr)
	_, err = testenv.NodeAddressFromInspect("\n")
	assert.Error(t, err, "no address: the k3d node is not running")
	_, err = testenv.NodeAddressFromInspect("not-an-ip")
	assert.Error(t, err)
}
