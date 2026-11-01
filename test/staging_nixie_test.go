package test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scripts/staging-nixie installs staging's VMs one at a time (three parallel installs ran WSL out of memory): one
// nixie run per host not yet recorded, each with only that host, and every install recorded in .state/
func TestStagingNixieOneAtATime(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, exec.Command("git", "init", "-q", repo).Run())
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "scripts"), 0o755))
	script, err := os.ReadFile("../scripts/staging-nixie")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "scripts", "staging-nixie"), script, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "scripts", "staging-wake"), []byte("import time\ntime.sleep(60)\n"), 0o644))
	hosts := `{"h1": {"mac_address": "52:54:00:00:00:01", "ip": "10.0.0.11"},
		"h2": {"mac_address": "52:54:00:00:00:02", "ip": "10.0.0.12"},
		"h3": {"mac_address": "52:54:00:00:00:03", "ip": "10.0.0.13"}}`
	require.NoError(t, os.WriteFile(filepath.Join(repo, "hosts.json"), []byte(hosts), 0o644))
	state := filepath.Join(repo, ".state", "staging", "metal", "nixie-hosts.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(state), 0o755))
	// h1 was installed before
	require.NoError(t, os.WriteFile(state, []byte(`{"h1": {"mac_address": "52:54:00:00:00:01", "ip": "10.0.0.11", "machine_id_hash": "old"}}`), 0o644))

	dir, env := fakeBin(t, map[string]string{
		"sudo": `[ "$1" = -v ] && exit 0; exec "$@"`,
		// nixie records each host of its file, as the real one does once it installed it
		"nixie": `while [ $# -gt 0 ]; do [ "$1" = --hosts ] && file=$2; shift; done
jq -c 'keys' "$file" >> "$FIXTURES/runs"
jq 'map_values(. + {machine_id_hash: "new"})' "$file" > "$file.tmp" && mv "$file.tmp" "$file"`,
	})
	cmd := exec.Command("sh", "scripts/staging-nixie", "hosts.json", "--address", "10.0.0.1")
	cmd.Dir, cmd.Env = repo, env
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	runs, err := os.ReadFile(filepath.Join(dir, "runs"))
	require.NoError(t, err)
	assert.Equal(t, `["h2"]`+"\n"+`["h3"]`+"\n", string(runs), "one nixie run per host not yet installed, alone")
	raw, err := os.ReadFile(state)
	require.NoError(t, err)
	var recorded map[string]struct {
		Hash string `json:"machine_id_hash"`
	}
	require.NoError(t, json.Unmarshal(raw, &recorded))
	assert.Equal(t, "old", recorded["h1"].Hash)
	assert.Equal(t, "new", recorded["h2"].Hash)
	assert.Equal(t, "new", recorded["h3"].Hash)
	assert.False(t, strings.Contains(string(raw), "tmp"))
}

// scripts/staging-trust waits for each node's SSH (VMs just started are still booting) and fails only after its wait
func TestStagingTrustWaitsForBoot(t *testing.T) {
	hosts := filepath.Join(t.TempDir(), "hosts.json")
	require.NoError(t, os.WriteFile(hosts, []byte(`{"h1": {"ip": "10.0.0.11"}, "h2": {"ip": "10.0.0.12"}}`), 0o644))
	dir, env := fakeBin(t, map[string]string{
		// The first two tries are refused, as sshd is not up yet
		"ssh": `n=$(cat "$FIXTURES/tries" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" > "$FIXTURES/tries"
[ "$n" -gt 2 ] || exit 255
echo "$*" >> "$FIXTURES/calls"`,
	})
	cmd := exec.Command("sh", "../scripts/staging-trust", hosts)
	cmd.Env = append(env, "STAGING_TRUST_WAIT=20", "STAGING_TRUST_INTERVAL=0")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	require.NoError(t, err)
	assert.Contains(t, string(calls), "StrictHostKeyChecking=accept-new")
	assert.Contains(t, string(calls), "admin@10.0.0.11")
	assert.Contains(t, string(calls), "admin@10.0.0.12")

	// A node that never answers fails, naming it
	_, env = fakeBin(t, map[string]string{"ssh": `exit 255`})
	cmd = exec.Command("sh", "../scripts/staging-trust", hosts)
	cmd.Env = append(env, "STAGING_TRUST_WAIT=0", "STAGING_TRUST_INTERVAL=0")
	out, err = cmd.CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(out), "10.0.0.11")
}
