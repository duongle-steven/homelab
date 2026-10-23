package test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every PVC declares its backup group (docs/superpowers/specs/2026-10-22-backups-design.md, decision 4); the basic
// checks run the script on the repository itself
func TestCheckBackupGroups(t *testing.T) {
	t.Run("a PVC without a group fails", func(t *testing.T) {
		cmd := exec.Command("scripts/check-backup-groups", "test/fixtures/backup-groups/unlabelled")
		cmd.Dir = ".."
		var stderr strings.Builder
		cmd.Stderr = &stderr
		err := cmd.Run()
		require.Error(t, err)
		var exit *exec.ExitError
		require.ErrorAs(t, err, &exit)
		assert.Equal(t, 1, exit.ExitCode())
		assert.Contains(t, stderr.String(), "forgotten")
	})
}
