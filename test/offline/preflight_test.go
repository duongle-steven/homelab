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
	// A testbed that was bootstrapped (infra/_modules/argocd's state)
	state := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(state, "argocd"), 0o755))
	bootstrapped := func(e []string) []string { return append(e, "TESTBED_STATE="+state) }
	out, err := runScript(t, bootstrapped(env(t, map[string]string{"kubectl": ns(`argocd\ncert-manager\ndefault\nistio-system\nkube-node-lease\nkube-public\nkube-system\nrook-ceph\n`)})), "testbed-leftovers")
	assert.NoError(t, err, out)
	out, err = runScript(t, bootstrapped(env(t, map[string]string{"kubectl": ns(`argocd\nfalco\nistio-system\nkube-system\nrook-ceph\n`)})), "testbed-leftovers")
	assert.Error(t, err)
	assert.Contains(t, out, "falco")
	assert.Contains(t, out, "kubectl delete namespace falco")
	// It runs before enroll (G2: the upgrade comes first). On a fresh install (testbed-clean removed the bootstrap's
	// state) no cluster answers yet: nothing can be left over
	refused := map[string]string{"kubectl": "echo 'The connection to the server was refused' >&2; exit 1"}
	out, err = runScript(t, append(env(t, refused), "TESTBED_STATE="+t.TempDir()), "testbed-leftovers")
	assert.NoError(t, err, out)
	assert.Contains(t, out, "fresh install")
	// A testbed that was bootstrapped and whose VMs just booted: it waits for the API rather than skip the guard
	out, err = runScript(t, append(bootstrapped(env(t, refused)), "LEFTOVERS_WAIT=2"), "testbed-leftovers")
	assert.Error(t, err)
	assert.Contains(t, out, "no cluster answered")
}

// env is fakeBin's environment alone
func env(t *testing.T, scripts map[string]string) []string {
	t.Helper()
	_, e := fakeBin(t, scripts)
	return e
}
