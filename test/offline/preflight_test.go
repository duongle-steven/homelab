package offline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Long commands check their preconditions first and fail fast with the fix, instead of timing out on a stale
// environment: a stopped sandbox, a key missing from the agent, leftovers on the testbed (2026-11-12's lesson)

func TestSandboxStopped(t *testing.T) {
	for state, stopped := range map[string]bool{"exited": true, "running": false, "": false} {
		body := `echo ` + state
		if state == "" {
			body = "echo 'Error: No such object' >&2; exit 1"
		}
		out, err := runScript(t, env(t, map[string]string{"docker": body}), "sandbox-stopped")
		assert.Equal(t, stopped, err == nil, "state %q: %s", state, out)
	}
}

func TestSSHKeyLoaded(t *testing.T) {
	keys, err := os.ReadFile(filepath.Join(repo.Root(), "infra", "testbed", "metal", "authorized_keys"))
	require.NoError(t, err)
	var key string
	for _, line := range lines(strings.TrimSpace(string(keys))) {
		if line != "" && line[0] != '#' {
			key = line
		}
	}
	require.NotEmpty(t, key)
	out, err := runScript(t, env(t, map[string]string{"ssh-add": "echo '" + key + "'"}), "ssh-key-loaded", "testbed")
	assert.NoError(t, err, out)
	out, err = runScript(t, env(t, map[string]string{"ssh-add": "echo 'The agent has no identities.'; exit 1"}), "ssh-key-loaded", "testbed")
	assert.Error(t, err)
	assert.Contains(t, out, "ssh-add")
}

func TestTestbedLeftovers(t *testing.T) {
	ns := func(names string) string { return "printf '" + names + "'" }
	out, err := runScript(t, env(t, map[string]string{"kubectl": ns(`argocd\ncert-manager\ndefault\nistio-system\nkube-node-lease\nkube-public\nkube-system\nrook-ceph\n`)}), "testbed-leftovers")
	assert.NoError(t, err, out)
	out, err = runScript(t, env(t, map[string]string{"kubectl": ns(`argocd\nfalco\nistio-system\nkube-system\nrook-ceph\n`)}), "testbed-leftovers")
	assert.Error(t, err)
	assert.Contains(t, out, "falco")
	assert.Contains(t, out, "kubectl delete namespace falco")
}

// env is fakeBin's environment alone
func env(t *testing.T, scripts map[string]string) []string {
	t.Helper()
	_, e := fakeBin(t, scripts)
	return e
}
