package offline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The one-node sandbox under its whole suite starves etcd now and then (requests of a second): with the default 15 s
// leases the controller manager lost its lease and k3s restarted every 70 to 80 minutes, restarting the CI's EventSource
// and Sensor and losing the webhooks sent meanwhile. Its controllers take the testbed's longer leases (k3s-server.nix's
// slow_control_plane); etcd's heartbeat does not matter on one member
func TestSandboxControllersLongLeases(t *testing.T) {
	k3d, err := os.ReadFile(filepath.Join(repo.Root(), "infra", "_modules", "k3d", "k3d.yaml"))
	require.NoError(t, err)
	for _, component := range []string{"kube-controller-manager", "kube-scheduler", "kube-cloud-controller-manager"} {
		assert.Contains(t, string(k3d), "- arg: --"+component+"-arg=leader-elect-lease-duration=60s\n", component)
		assert.Contains(t, string(k3d), "- arg: --"+component+"-arg=leader-elect-renew-deadline=40s\n", component)
	}
}
