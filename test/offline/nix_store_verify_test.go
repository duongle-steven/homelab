package offline

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The nodes' weekly store check (G11, docs/superpowers/specs/2026-11-27-node-patches-design.md): its result for
// node-exporter, never half-written, and a modified path a failure whatever nix-store's exit status
const storeVerifyScript = "infra/_modules/metal/flake/profiles/nix-store-verify.sh"

// runStoreVerify runs the script with a nix-store that prints output and exits with status, and returns the metric
// file's text, the script's error and the directory's entries
func runStoreVerify(t *testing.T, output string, status int) (string, error, []string) {
	t.Helper()
	bin, out := t.TempDir(), t.TempDir()
	fake := "#!/usr/bin/env bash\nprintf '%s' \"$FAKE_OUTPUT\"\nexit " + strconv.Itoa(status) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "nix-store"), []byte(fake), 0o755))
	cmd := exec.Command("bash", filepath.Join(repo.Root(), storeVerifyScript), out)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "FAKE_OUTPUT="+output)
	_, err := cmd.CombinedOutput()
	text, readErr := os.ReadFile(filepath.Join(out, "nix-store-verify.prom"))
	require.NoError(t, readErr)
	entries, dirErr := os.ReadDir(out)
	require.NoError(t, dirErr)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return string(text), err, names
}

func storeVerifyValue(t *testing.T, text, metric string) int64 {
	t.Helper()
	m := regexp.MustCompile(`(?m)^` + metric + ` (\d+)$`).FindStringSubmatch(text)
	require.NotNil(t, m, "%s missing from:\n%s", metric, text)
	v, err := strconv.ParseInt(m[1], 10, 64)
	require.NoError(t, err)
	return v
}

func TestNixStoreVerifyClean(t *testing.T) {
	text, err, _ := runStoreVerify(t, "", 0)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), storeVerifyValue(t, text, "node_nix_store_verify_success"))
	assert.InDelta(t, time.Now().Unix(), storeVerifyValue(t, text, "node_nix_store_verify_last_run_seconds"), 5)
	assert.Contains(t, text, "# TYPE node_nix_store_verify_success gauge")
}

func TestNixStoreVerifyFails(t *testing.T) {
	text, err, _ := runStoreVerify(t, "error: something\n", 1)
	assert.Error(t, err, "systemd shows the failure too")
	assert.Equal(t, int64(0), storeVerifyValue(t, text, "node_nix_store_verify_success"))
}

// Some Nix versions exit 0 after reporting a modified path
func TestNixStoreVerifyModified(t *testing.T) {
	text, err, _ := runStoreVerify(t, "path '/nix/store/x' was modified! expected hash 'a', got 'b'\n", 0)
	assert.Error(t, err)
	assert.Equal(t, int64(0), storeVerifyValue(t, text, "node_nix_store_verify_success"))
}

// A run cut off (a reboot, a power cut) never leaves a half-written file that reads as a success
func TestNixStoreVerifyAtomic(t *testing.T) {
	_, _, names := runStoreVerify(t, "", 0)
	assert.Equal(t, []string{"nix-store-verify.prom"}, names)
}
