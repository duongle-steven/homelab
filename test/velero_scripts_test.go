package test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeVeleroKubectl answers the prune and snapshot scripts' queries from files and records what they create and delete
const fakeVeleroKubectl = `#!/bin/sh
case "$*" in
  *"get backupstoragelocations.velero.io default"*) cat "$FIXTURES/mode" ;;
  *"get backups.velero.io"*) cat "$FIXTURES/backups" ;;
  *"get persistentvolumeclaims"*) cat "$FIXTURES/pvcs" ;;
  *"get volumesnapshots.snapshot.storage.k8s.io"*) cat "$FIXTURES/snapshots" ;;
  *"delete volumesnapshots.snapshot.storage.k8s.io"*) echo "$*" | sed 's/.*-n \([^ ]*\) delete volumesnapshots.snapshot.storage.k8s.io \([^ ]*\).*/deleted \1\/\2/' >> "$FIXTURES/actions" ;;
  *"create -f -"*)
    doc=$(cat)
    case "$doc" in
      *DeleteBackupRequest*) echo "$doc" | sed -n 's/^ *backupName: //p' | sed 's/^/delete /' >> "$FIXTURES/actions" ;;
      *VolumeSnapshot*) echo "created $(echo "$doc" | sed -n 's/^ *namespace: //p')/$(echo "$doc" | sed -n 's/^ *persistentVolumeClaimName: //p')" >> "$FIXTURES/actions" ;;
    esac ;;
esac
`

func runVeleroScript(t *testing.T, script string, fixtures map[string]string) []string {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kubectl"), []byte(fakeVeleroKubectl), 0o755))
	for name, content := range fixtures {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	cmd := exec.Command("sh", "../system/velero/files/"+script)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "FIXTURES="+dir, "KEEP=30", "SNAPSHOT_HOURS=48", "NAMESPACE=velero")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s failed:\n%s", script, out)
	actions, _ := os.ReadFile(filepath.Join(dir, "actions"))
	if s := strings.TrimSpace(string(actions)); s != "" {
		return strings.Split(s, "\n")
	}
	return nil
}

// backups lists name|schedule|app|phase, oldest first (the script sorts by creationTimestamp)
func backups(schedule string, n int, phase string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%s-%02d|%s||%s\n", schedule, i, schedule, phase)
	}
	return b.String()
}

// Retention by count, only while the location is writable (spec decision 8)
func TestVeleroPrune(t *testing.T) {
	manual := "homelab-ntfy-1||ntfy|Completed\nhomelab-ntfy-2||ntfy|Completed\nhomelab-ntfy-3||ntfy|Completed\n"
	t.Run("read-only deletes nothing", func(t *testing.T) {
		assert.Empty(t, runVeleroScript(t, "prune.sh", map[string]string{"mode": "ReadOnly", "backups": backups("daily", 40, "Completed")}))
	})
	t.Run("keeps the newest 30 of each group", func(t *testing.T) {
		assert.Equal(t, []string{"delete daily-01", "delete daily-02"},
			runVeleroScript(t, "prune.sh", map[string]string{"mode": "ReadWrite", "backups": backups("daily", 32, "Completed") + manual}))
	})
	t.Run("a failed backup is not counted and goes once a newer one completed", func(t *testing.T) {
		list := "daily-00|daily||Failed\n" + backups("daily", 30, "Completed") + "daily-31|daily||Failed\ndaily-32|daily||InProgress\n"
		assert.Equal(t, []string{"delete daily-00"}, runVeleroScript(t, "prune.sh", map[string]string{"mode": "ReadWrite", "backups": list}))
	})
	t.Run("a group with only failed backups keeps them", func(t *testing.T) {
		assert.Empty(t, runVeleroScript(t, "prune.sh", map[string]string{"mode": "ReadWrite", "backups": backups("daily", 3, "Failed")}))
	})
}

// Hourly local snapshots of the labelled volumes, kept 48 hours (spec decision 3)
func TestVeleroSnapshots(t *testing.T) {
	// A claim not yet bound (WaitForFirstConsumer, app stopped) has nothing to snapshot
	pvcs := "forgejo forgejo-data Bound\nforgejo forgejo-db-1 Bound\nntfy ntfy Bound\npaperless paperless-data Pending\n"
	snapshots := fmt.Sprintf("forgejo forgejo-data-old %s\nforgejo forgejo-data-new %s\n", ago(49*3600), ago(3600))
	actions := runVeleroScript(t, "snapshots.sh", map[string]string{"pvcs": pvcs, "snapshots": snapshots})
	assert.Equal(t, []string{
		"created forgejo/forgejo-data",
		"created forgejo/forgejo-db-1",
		"created ntfy/ntfy",
		"deleted forgejo/forgejo-data-old",
	}, actions)
}
