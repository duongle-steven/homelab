package nodes

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The script finds a process named k3s and succeeds whatever /proc holds after it: a for loop ends with the status of
// its last command, which a false test would make 1 (the final review, 2026-11-25)
func TestK3sNameUIDs(t *testing.T) {
	bash, err := exec.LookPath("bash")
	require.NoError(t, err)
	bash, err = filepath.EvalSymlinks(bash)
	require.NoError(t, err)
	named := filepath.Join(t.TempDir(), "k3s")
	require.NoError(t, os.Symlink(bash, named))
	sleep, err := exec.LookPath("sleep")
	require.NoError(t, err)
	proc := exec.Command(named, "-c", sleep+" infinity & wait")
	require.NoError(t, proc.Start())
	t.Cleanup(func() { _ = proc.Process.Kill(); _ = proc.Wait() })

	var out []byte
	for i := 0; i < 50; i++ {
		// The process may still be starting: its comm is k3s from its exec on
		if out, err = exec.Command("sh", "-c", K3sNameUIDs).Output(); err == nil && len(out) > 0 {
			break
		}
	}
	require.NoError(t, err, "the script fails: %s", out)
	assert.Contains(t, strings.Fields(string(out)), strconv.Itoa(os.Getuid()))
}
