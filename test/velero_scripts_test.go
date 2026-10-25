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
  *"get volumesnapshots.snapshot.storage.k8s.io -A -l velero.io/backup-name"*) cat "$FIXTURES/velerosnapshots" 2>/dev/null || true ;;
  *"get volumesnapshots.snapshot.storage.k8s.io"*) cat "$FIXTURES/snapshots" ;;
  *"delete volumesnapshots.snapshot.storage.k8s.io"*) echo "$*" | sed 's/.*-n \([^ ]*\) delete volumesnapshots.snapshot.storage.k8s.io \([^ ]*\).*/deleted \1\/\2/' >> "$FIXTURES/actions" ;;
  *"create -f -"*)
    doc=$(cat)
    case "$doc" in
      *DeleteBackupRequest*) echo "$doc" | sed -n 's/^ *backupName: //p' | sed 's/^/delete /' >> "$FIXTURES/actions" ;;
      *VolumeSnapshot*)
        pvc=$(echo "$doc" | sed -n 's/^ *persistentVolumeClaimName: //p')
        if grep -qx "$pvc" "$FIXTURES/existing" 2>/dev/null; then echo "Error from server (AlreadyExists): volumesnapshots.snapshot.storage.k8s.io \"$pvc-x\" already exists" >&2; exit 1; fi
        if grep -qx "$pvc" "$FIXTURES/broken" 2>/dev/null; then echo "Error from server: the server is unavailable" >&2; exit 1; fi
        echo "created $(echo "$doc" | sed -n 's/^ *namespace: //p')/$(echo "$doc" | sed -n 's/^ *persistentVolumeClaimName: //p')" >> "$FIXTURES/actions" ;;
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
	if fixtures["broken"] != "" {
		require.Error(t, err, "%s must fail when a snapshot could not be taken:\n%s", script, out)
	} else {
		require.NoError(t, err, "%s failed:\n%s", script, out)
	}
	actions, _ := os.ReadFile(filepath.Join(dir, "actions"))
	if s := strings.TrimSpace(string(actions)); s != "" {
		return strings.Split(s, "\n")
	}
	return nil
}

// backups lists start|name|schedule|app|phase, started one day apart, oldest first
func backups(schedule string, n int, phase string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "2026-%02d-%02dT02:00:00Z|%s-%02d|%s||%s\n", 1+i/28, 1+i%28, schedule, i, schedule, phase)
	}
	return b.String()
}

// reversed lists the same lines newest first: a new cluster creates the backups it reads from the bucket in any order
func reversed(list string) string {
	lines := strings.Split(strings.TrimSpace(list), "\n")
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return strings.Join(lines, "\n") + "\n"
}

// Retention by count, only while the location is writable (spec decision 8)
func TestVeleroPrune(t *testing.T) {
	manual := "2026-02-01T10:00:00Z|homelab-ntfy-1||ntfy|Completed\n2026-02-02T10:00:00Z|homelab-ntfy-2||ntfy|Completed\n2026-02-03T10:00:00Z|homelab-ntfy-3||ntfy|Completed\n"
	t.Run("read-only deletes nothing", func(t *testing.T) {
		assert.Empty(t, runVeleroScript(t, "prune.sh", map[string]string{"mode": "ReadOnly", "backups": backups("daily", 40, "Completed")}))
	})
	t.Run("keeps the newest 30 of each group", func(t *testing.T) {
		assert.Equal(t, []string{"delete daily-01", "delete daily-02"},
			runVeleroScript(t, "prune.sh", map[string]string{"mode": "ReadWrite", "backups": backups("daily", 32, "Completed") + manual}))
	})
	t.Run("by when the backup was taken, not when this cluster created it", func(t *testing.T) {
		assert.Equal(t, []string{"delete daily-01", "delete daily-02"},
			runVeleroScript(t, "prune.sh", map[string]string{"mode": "ReadWrite", "backups": reversed(backups("daily", 32, "Completed"))}))
	})
	t.Run("a failed backup is not counted and goes once a newer one completed", func(t *testing.T) {
		list := "|daily-00|daily||Failed\n" + backups("daily", 30, "Completed") + "2026-12-30T02:00:00Z|daily-31|daily||Failed\n2026-12-31T02:00:00Z|daily-32|daily||InProgress\n"
		assert.Equal(t, []string{"delete daily-00"}, runVeleroScript(t, "prune.sh", map[string]string{"mode": "ReadWrite", "backups": list}))
	})
	t.Run("a group with only failed backups keeps them", func(t *testing.T) {
		assert.Empty(t, runVeleroScript(t, "prune.sh", map[string]string{"mode": "ReadWrite", "backups": backups("daily", 3, "Failed")}))
	})
}

// A backup cut short (a power cut while its data movers copied) can leave a VolumeSnapshot Velero made for it: prune
// deletes the snapshots whose backup is gone, whatever the location's mode, and never one whose backup still exists
func TestVeleroPruneOrphanSnapshots(t *testing.T) {
	list := "2026-10-24T02:00:00Z|daily-1|daily||Completed\n2026-10-25T02:00:00Z|daily-2|daily||InProgress\n"
	// namespace name backup; a label of 63 characters may be Velero's shortened form of a longer name
	long := strings.Repeat("x", 63)
	snapshots := "kanidm velero-kanidm-data-a daily-1\nforgejo velero-forgejo-data-b daily-2\nkanidm velero-kanidm-data-c gone-3\nntfy velero-ntfy-d " + long + "\n"
	for _, mode := range []string{"ReadWrite", "ReadOnly"} {
		t.Run(mode, func(t *testing.T) {
			assert.Equal(t, []string{"deleted kanidm/velero-kanidm-data-c"},
				runVeleroScript(t, "prune.sh", map[string]string{"mode": mode, "backups": list, "velerosnapshots": snapshots}))
		})
	}
}

// Hourly local snapshots of the labelled volumes, kept 48 hours (spec decision 3)
func TestVeleroSnapshots(t *testing.T) {
	// A claim not yet bound (WaitForFirstConsumer, app stopped) has nothing to snapshot
	pvcs := "forgejo forgejo-data Bound\nforgejo forgejo-db-1 Bound\nntfy ntfy Bound\npaperless paperless-data Pending\n"
	// namespace name created readyToUse claim
	snapshots := fmt.Sprintf("forgejo forgejo-data-old %s true forgejo-data\nforgejo forgejo-data-new %s true forgejo-data\n", ago(49*3600), ago(3600))
	actions := runVeleroScript(t, "snapshots.sh", map[string]string{"pvcs": pvcs, "snapshots": snapshots})
	assert.Equal(t, []string{
		"created forgejo/forgejo-data",
		"created forgejo/forgejo-db-1",
		"created ntfy/ntfy",
		"deleted forgejo/forgejo-data-old",
	}, actions)
	// Run again within the hour (by hand, or a test): the hour's snapshot is there already, and nothing else is skipped
	actions = runVeleroScript(t, "snapshots.sh", map[string]string{"pvcs": pvcs, "snapshots": snapshots, "existing": "forgejo-data\n"})
	assert.Equal(t, []string{"created forgejo/forgejo-db-1", "created ntfy/ntfy", "deleted forgejo/forgejo-data-old"}, actions)
	// One claim failing still snapshots the others and cleans up, then fails the Job
	actions = runVeleroScript(t, "snapshots.sh", map[string]string{"pvcs": pvcs, "snapshots": snapshots, "broken": "forgejo-db-1\n"})
	assert.Equal(t, []string{"created forgejo/forgejo-data", "created ntfy/ntfy", "deleted forgejo/forgejo-data-old"}, actions)
	// Snapshotting broken for two days: a claim keeps its newest ready snapshot, however old
	stale := fmt.Sprintf("ntfy ntfy-a %s true ntfy\nntfy ntfy-b %s true ntfy\nntfy ntfy-c %s false ntfy\n", ago(80*3600), ago(60*3600), ago(50*3600))
	actions = runVeleroScript(t, "snapshots.sh", map[string]string{"pvcs": "", "snapshots": stale})
	assert.Equal(t, []string{"deleted ntfy/ntfy-a", "deleted ntfy/ntfy-c"}, actions)
}
