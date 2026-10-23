package test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func renderVelero(t *testing.T, target, env, expr string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", `scripts/render-chart "$1" $2 | yq -N "$0"`, expr, target, env)
	cmd.Dir = ".."
	// stdout only: helm may report a chart it pulled on stderr
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())
	return strings.TrimSpace(string(out))
}

// docs/superpowers/specs/2026-10-22-backups-design.md, decisions 2, 3, 6, 8 and 9
func TestVeleroRender(t *testing.T) {
	for _, env := range []string{"", "dev"} {
		t.Run("env="+env, func(t *testing.T) {
			get := func(expr string) string { return renderVelero(t, "system/velero", env, expr) }
			// Velero's data mover (Kopia) takes the endpoint only from s3Url; without it, it dials AWS
			if env == "dev" {
				assert.Equal(t, "http://s3.velero:7070", get(`select(.kind == "BackupStorageLocation" and .metadata.name == "default") | .spec.config.s3Url`))
			}
			// Every new cluster starts unable to write or delete backups; lab backup enable switches it
			assert.Equal(t, "ReadOnly", get(`select(.kind == "BackupStorageLocation" and .metadata.name == "default") | .spec.accessMode`))
			schedule := `select(.kind == "Schedule" and .metadata.name == "daily") | `
			assert.Equal(t, "true", get(schedule+`.spec.paused`))
			assert.Equal(t, "87600h0m0s", get(schedule+`.spec.template.ttl`))
			assert.Equal(t, "true", get(schedule+`.spec.template.snapshotMoveData`))
			assert.Equal(t, "daily", get(schedule+`.spec.template.labelSelector.matchLabels["homelab/backup"]`))
			// Volumes only: git rebuilds every other resource
			assert.Equal(t, "persistentvolumeclaims,persistentvolumes", get(schedule+`.spec.template.includedResources | join(",")`))
			assert.Regexp(t, `^CRON_TZ=\S+ 0 2 \* \* \*$`, get(schedule+`.spec.schedule`))
			// Restores with the app stopped: WaitForFirstConsumer volumes must not wait for a consumer pod
			assert.Contains(t, get(`select(.kind == "ConfigMap" and .metadata.name == "velero-node-agent-config") | .data["node-agent-config.json"]`), `"ignoreDelayBinding":true`)
			assert.Contains(t, get(`select(.kind == "DaemonSet" and .metadata.name == "node-agent") | .spec.template.spec.containers[0].args | join(" ")`), "--node-agent-configmap=velero-node-agent-config")
			// Velero creates a default repository password when its Secret is missing: the pod waits for OpenBao's
			assert.Equal(t, "velero-repo-credentials", get(`select(.kind == "Deployment" and .metadata.name == "velero") | .spec.template.spec.volumes[] | select(.name == "repo-credentials") | .secret.secretName`))
			for _, job := range []string{"prune", "snapshots"} {
				assert.Equal(t, "CronJob", get(`select(.kind == "CronJob" and .metadata.name == "`+job+`") | .kind`), job)
			}
			assert.Equal(t, "VeleroNoRecentBackup", get(`select(.kind == "PrometheusRule") | .spec.groups[].rules[] | select(.alert == "VeleroNoRecentBackup") | .alert`))
			s3 := get(`select(.kind == "Deployment" and .metadata.name == "s3") | .metadata.name`)
			if env == "dev" {
				assert.Equal(t, "s3", s3, "the sandbox's S3 server")
			} else {
				assert.Empty(t, s3, "production uses R2")
			}
		})
	}
	t.Run("egress to R2", func(t *testing.T) {
		out, err := exec.Command("yq", `.internet.velero[].port`, "../system/egress/values.yaml").Output()
		require.NoError(t, err)
		assert.Equal(t, "443", strings.TrimSpace(string(out)))
	})
}

// OpenBao's hourly raft snapshot lands in a volume of its own, the one Velero copies (spec decision 5)
func TestOpenBaoSnapshotJob(t *testing.T) {
	get := func(expr string) string { return renderVelero(t, "platform/openbao", "", expr) }
	assert.Equal(t, "55 * * * *", get(`select(.kind == "CronJob" and .metadata.name == "openbao-snapshot") | .spec.schedule`))
	assert.Equal(t, "daily", get(`select(.kind == "PersistentVolumeClaim" and .metadata.name == "openbao-snapshots") | .metadata.labels["homelab/backup"]`))
	assert.Contains(t, get(`select(.kind == "CronJob" and .metadata.name == "openbao-snapshot") | .spec.jobTemplate.spec.template.spec.containers[0].args | join(" ")`), "raft snapshot save")
}

// lab backup restore pauses one Application, and lab backup enable's two fields survive syncs (spec decision 8)
func TestArgoCDBackupSync(t *testing.T) {
	for _, env := range []string{"", "dev"} {
		for _, appSet := range []string{"root", "apps"} {
			assert.Equal(t, "/spec/syncPolicy/automated", renderVelero(t, "system/argocd", env,
				`select(.kind == "ApplicationSet" and .metadata.name == "`+appSet+`") | .spec.ignoreApplicationDifferences[].jsonPointers[]`), env+" "+appSet)
		}
		patch := renderVelero(t, "system/argocd", env, `select(.kind == "ApplicationSet" and .metadata.name == "root") | .spec.templatePatch`)
		assert.Regexp(t, `(?s)eq \.path\.basename "velero".*kind: BackupStorageLocation\s+jsonPointers: \[/spec/accessMode\].*kind: Schedule\s+jsonPointers: \[/spec/paused\]`, patch)
	}
}

// csi-hostpath keeps volumes where k3d mounts the host's disk: the node container's own overlayfs refuses the
// idmapped mounts of pods in user namespaces (Woodpecker's steps failed with MOUNT_ATTR_IDMAP)
func TestCSIHostPathDataOnNodeDisk(t *testing.T) {
	cmd := exec.Command("sh", "-c", `scripts/render-chart system/csi-hostpath dev | yq -N 'select(.kind == "StatefulSet" or .kind == "DaemonSet") | .spec.template.spec.volumes[] | select(.name == "csi-data-dir") | .hostPath.path'`)
	cmd.Dir = ".."
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())
	assert.Equal(t, "/var/lib/rancher/k3s/storage/csi-hostpath", strings.TrimSpace(string(out)))
}
