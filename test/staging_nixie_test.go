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
