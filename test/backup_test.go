package test

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// labE runs lab like lab() but returns its error, for the refusals
func labE(args ...string) (string, error) {
	cmd := exec.Command("go", append([]string{"run", "./cmd/lab"}, args...)...)
	cmd.Dir = "../cli"
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// baoPut writes secret/<path> value=<value> the documented way (an admin login inside OpenBao's pod); value is a test
// marker, never a secret
func baoPut(t *testing.T, path, value string, del bool) {
	t.Helper()
	op := "bao kv put secret/" + path + " value=" + value + " >/dev/null"
	if del {
		op = "bao kv metadata delete secret/" + path + " >/dev/null"
	}
	script := "kubectl -n openbao create token openbao-admin --audience=openbao --duration=10m | kubectl -n openbao exec -i openbao-0 -c openbao -- " +
		"sh -c 'export BAO_TOKEN=$(bao write -field=token auth/kubernetes/login role=admin jwt=-) && " + op + "; s=$?; bao token revoke -self >/dev/null 2>&1; exit $s'"
	out, err := exec.Command("sh", "-c", script).CombinedOutput()
	require.NoError(t, err, "secret/%s: %s", path, out)
}

func kubectlJSON(t *testing.T, out any, args ...string) {
	t.Helper()
	raw, err := k8s.RunKubectlAndGetOutputE(t, k8s.NewKubectlOptions("", "", ""), append(args, "-o", "json")...)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(raw), out))
}

// runCronJob starts a Job from the CronJob and waits for it to succeed
func runCronJob(t *testing.T, namespace, cronJob string) {
	t.Helper()
	name := fmt.Sprintf("%s-test-%d", cronJob, time.Now().Unix())
	opts := k8s.NewKubectlOptions("", "", namespace)
	k8s.RunKubectl(t, opts, "create", "job", name, "--from=cronjob/"+cronJob)
	k8s.RunKubectl(t, opts, "wait", "--for=condition=Complete", "--timeout=5m", "job/"+name)
}

func waitBackup(t *testing.T, name string) {
	t.Helper()
	require.Eventually(t, func() bool {
		var b struct {
			Status struct{ Phase string } `json:"status"`
		}
		kubectlJSON(t, &b, "-n", "velero", "get", "backups.velero.io", name)
		require.NotContains(t, []string{"Failed", "PartiallyFailed", "FailedValidation"}, b.Status.Phase, "backup %s", name)
		return b.Status.Phase == "Completed"
	}, 20*time.Minute, 10*time.Second, "backup %s did not complete", name)
}

func allSyncedHealthy(t *testing.T) []string {
	t.Helper()
	var apps struct {
		Items []struct {
			Metadata struct{ Name string } `json:"metadata"`
			Status   struct {
				Sync   struct{ Status string } `json:"sync"`
				Health struct{ Status string } `json:"health"`
			} `json:"status"`
		} `json:"items"`
	}
	kubectlJSON(t, &apps, "-n", "argocd", "get", "applications.argoproj.io")
	var bad []string
	for _, a := range apps.Items {
		if a.Status.Sync.Status != "Synced" || a.Status.Health.Status != "Healthy" {
			bad = append(bad, fmt.Sprintf("%s %s/%s", a.Metadata.Name, a.Status.Sync.Status, a.Status.Health.Status))
		}
	}
	return bad
}

// TestBackupRestore backs up the sandbox off-site and locally, loses a repository, its issue and an OpenBao key, and
// gets them back with lab backup restore (docs/superpowers/specs/2026-10-22-backups-design.md, decision 9). It runs
// alone: a restore rolls back all of OpenBao and restarts every reader.
func TestBackupRestore(t *testing.T) {
	meshSandboxOnly(t)
	env := []string{"--env", "sandbox"}
	velero := k8s.NewKubectlOptions("", "", "velero")
	t.Cleanup(func() { _, _ = labE(append([]string{"backup", "disable"}, env...)...) })

	t.Run("a fresh cluster is read-only", func(t *testing.T) {
		mode, err := k8s.RunKubectlAndGetOutputE(t, velero, "get", "backupstoragelocations.velero.io", "default", "-o", "jsonpath={.spec.accessMode}")
		require.NoError(t, err)
		assert.Equal(t, "ReadOnly", mode)
		paused, err := k8s.RunKubectlAndGetOutputE(t, velero, "get", "schedules.velero.io", "daily", "-o", "jsonpath={.spec.paused}")
		require.NoError(t, err)
		assert.Equal(t, "true", paused)
		runCronJob(t, "velero", "prune")
		logs, err := k8s.RunKubectlAndGetOutputE(t, velero, "logs", "-l", "job-name", "--tail=-1", "--prefix")
		require.NoError(t, err)
		assert.Contains(t, logs, "no backup pruned")
	})

	repo := fmt.Sprintf("backup-probe-%d", time.Now().Unix())
	var created struct {
		FullName string `json:"full_name"`
	}
	require.Equal(t, 201, forgejoAs(t, "", "POST", "/user/repos", map[string]any{"name": repo, "auto_init": true, "private": true}, &created))
	owner := strings.Split(created.FullName, "/")[0]
	t.Cleanup(func() { forgejoAs(t, "", "DELETE", "/repos/"+owner+"/"+repo, nil, nil) })
	var commits []struct{ SHA string }
	require.Equal(t, 200, forgejoAs(t, "", "GET", "/repos/"+owner+"/"+repo+"/commits", nil, &commits))
	require.NotEmpty(t, commits)
	commit := commits[0].SHA
	var issue struct{ Number int }
	require.Equal(t, 201, forgejoAs(t, "", "POST", "/repos/"+owner+"/"+repo+"/issues", map[string]any{"title": "kept by the backup"}, &issue))
	marker := fmt.Sprintf("marker-%d", time.Now().UnixNano())
	baoPut(t, "smoke/backup-marker", marker, false)
	t.Cleanup(func() { baoPut(t, "smoke/backup-marker", "", true) })

	// The off-site copy, as the daily schedule takes it (OpenBao's snapshot first), and one round of local snapshots
	lab(t, append([]string{"backup", "enable", "--yes"}, env...)...)
	runCronJob(t, "openbao", "openbao-snapshot")
	var schedule struct {
		Spec struct {
			Template map[string]any `json:"template"`
		} `json:"spec"`
	}
	kubectlJSON(t, &schedule, "-n", "velero", "get", "schedules.velero.io", "daily")
	backup := fmt.Sprintf("daily-test-%d", time.Now().Unix())
	manifest, err := json.Marshal(map[string]any{
		"apiVersion": "velero.io/v1", "kind": "Backup",
		"metadata": map[string]any{"name": backup, "namespace": "velero", "labels": map[string]string{"velero.io/schedule-name": "daily"}},
		"spec":     schedule.Spec.Template,
	})
	require.NoError(t, err)
	k8s.KubectlApplyFromString(t, velero, string(manifest))
	waitBackup(t, backup)
	// Snapshots are one per claim and hour (snapshots.sh skips an hour's existing one): this hour's, taken before the
	// test's data, would be what the local restore reads
	for _, claim := range []string{"forgejo-data", "forgejo-db-1"} {
		_, _ = k8s.RunKubectlAndGetOutputE(t, k8s.NewKubectlOptions("", "", "forgejo"), "delete", "volumesnapshots.snapshot.storage.k8s.io",
			claim+"-"+time.Now().UTC().Format("2006010215"), "--ignore-not-found")
	}
	// This run's snapshots, not an older hour's (already ready): csi-hostpath copies a volume with tar while it is
	// written, so changing the data before its snapshot is ready puts the change in it (the restore then brought the
	// repository back deleted)
	started := time.Now().Truncate(time.Second)
	runCronJob(t, "velero", "snapshots")
	for _, claim := range []string{"forgejo-data", "forgejo-db-1"} {
		require.Eventually(t, func() bool {
			out, err := k8s.RunKubectlAndGetOutputE(t, k8s.NewKubectlOptions("", "", "forgejo"), "get", "volumesnapshots.snapshot.storage.k8s.io",
				"-l", "homelab/snapshot=hourly,homelab/pvc="+claim, "-o", `jsonpath={range .items[*]}{.metadata.creationTimestamp} {.status.readyToUse}{"\n"}{end}`)
			if err != nil {
				return false
			}
			for _, line := range strings.Split(out, "\n") {
				created, ready, _ := strings.Cut(line, " ")
				at, err := time.Parse(time.RFC3339, created)
				if err == nil && !at.Before(started) && ready == "true" {
					return true
				}
			}
			return false
		}, 5*time.Minute, 5*time.Second, "no ready snapshot of %s from this run", claim)
	}

	// Lost: the repository with its issue, and the key
	require.Equal(t, 204, forgejoAs(t, "", "DELETE", "/repos/"+owner+"/"+repo, nil, nil))
	baoPut(t, "smoke/backup-marker", "", true)

	t.Run("a whole-cluster restore brings them back", func(t *testing.T) {
		lab(t, append([]string{"backup", "disable", "--yes"}, env...)...)
		// Every backup here was taken after this cluster was created, of its own volumes: the default refuses them
		out, err := labE(append([]string{"backup", "restore", "--yes"}, env...)...)
		require.Error(t, err, out)
		assert.Contains(t, out, "--backup")
		lab(t, append([]string{"backup", "restore", "--backup", backup, "--yes"}, env...)...)
		assert.Equal(t, marker, baoValue(t, "smoke/backup-marker", "value"))
		var got []struct{ SHA string }
		require.Equal(t, 200, forgejoAs(t, "", "GET", "/repos/"+owner+"/"+repo+"/commits", nil, &got))
		require.NotEmpty(t, got)
		assert.Equal(t, commit, got[0].SHA)
		var back struct{ Title string }
		require.Equal(t, 200, forgejoAs(t, "", "GET", fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, issue.Number), nil, &back))
		assert.Equal(t, "kept by the backup", back.Title)
		var bad []string
		require.Eventually(t, func() bool { bad = allSyncedHealthy(t); return len(bad) == 0 }, 20*time.Minute, 15*time.Second, "after the restore: %v", bad)
	})

	t.Run("one app from its local snapshot", func(t *testing.T) {
		require.Equal(t, 204, forgejoAs(t, "", "DELETE", "/repos/"+owner+"/"+repo, nil, nil))
		lab(t, append([]string{"backup", "restore", "forgejo", "--yes"}, env...)...)
		var back struct{ Title string }
		require.Equal(t, 200, forgejoAs(t, "", "GET", fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, issue.Number), nil, &back))
		assert.Equal(t, "kept by the backup", back.Title)
	})

	t.Run("push asks first", func(t *testing.T) {
		lab(t, append([]string{"backup", "enable", "--yes"}, env...)...)
		out, err := labE(append([]string{"backup", "push", "ntfy"}, env...)...)
		require.Error(t, err, out)
		assert.Contains(t, out, "--yes")
		assert.Contains(t, out, "of 10 GB free")
		out = lab(t, append([]string{"backup", "push", "ntfy", "--yes"}, env...)...)
		assert.Contains(t, out, "completed")
	})
}
