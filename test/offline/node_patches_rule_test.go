package offline

import (
	"testing"

	"github.com/duongle-steven/homelab/test/internal/render"
	"github.com/stretchr/testify/assert"
)

// node-exporter reads the nodes' metric files (G11, docs/superpowers/specs/2026-11-27-node-patches-design.md) under
// the host's root, with the chart's own arguments kept (Helm replaces the list)
func TestNodeExporterTextfile(t *testing.T) {
	args := render.Yq(t, "system/monitoring-system", "",
		`select(.kind == "DaemonSet" and .metadata.name == "monitoring-system-prometheus-node-exporter") | .spec.template.spec.containers[0].args[]`)
	assert.Contains(t, args, "--collector.textfile.directory=/host/root/var/lib/node-exporter")
	assert.Contains(t, args, "--collector.filesystem.mount-points-exclude=^/(dev|proc|sys|run/containerd/.+|var/lib/docker/.+|var/lib/kubelet/.+)($|/)")
	assert.Contains(t, args, "--collector.filesystem.fs-types-exclude=")
}

// The nodes' packages and store check alert in production only: the sandbox's k3d node is no NixOS node, and the
// testbed runs no monitoring
func TestNodePatchesRules(t *testing.T) {
	rules := func(env, expr string) string {
		return render.Yq(t, "system/monitoring-system", env,
			`select(.kind == "PrometheusRule" and .metadata.name == "node-patches") | .spec.groups[].rules[] | `+expr)
	}
	assert.Empty(t, rules("dev", ".alert"), "no node alerts in the sandbox")
	assert.Equal(t, "NodePackagesStale\nNodeStoreVerifyFailed\nNodeStoreVerifyMissing", rules("", ".alert"))
	assert.Equal(t, "warning\nwarning\nwarning", rules("", ".labels.severity"))
	assert.Equal(t, `time() - (node_nixpkgs_last_modified_seconds * on(instance) group_left(nodename) node_uname_info) > 14 * 86400
or on(nodename)
(label_replace(kube_node_info, "nodename", "$1", "node", "(.*)")
  unless on(nodename) (node_nixpkgs_last_modified_seconds * on(instance) group_left(nodename) node_uname_info))`,
		rules("", `select(.alert == "NodePackagesStale") | .expr | trim`))
	assert.Equal(t, `node_nix_store_verify_success * on(instance) group_left(nodename) node_uname_info == 0`,
		rules("", `select(.alert == "NodeStoreVerifyFailed") | .expr | trim`))
	assert.Equal(t, `time() - (node_nix_store_verify_last_run_seconds * on(instance) group_left(nodename) node_uname_info) > 15 * 86400
or on(nodename)
(label_replace(time() - kube_node_created > 15 * 86400, "nodename", "$1", "node", "(.*)")
  unless on(nodename) (node_nix_store_verify_last_run_seconds * on(instance) group_left(nodename) node_uname_info))`,
		rules("", `select(.alert == "NodeStoreVerifyMissing") | .expr | trim`))
	assert.Equal(t, "1h", rules("", `select(.alert == "NodePackagesStale") | .for`))
	assert.Equal(t, "1h", rules("", `select(.alert == "NodeStoreVerifyMissing") | .for`))
	stale := rules("", `select(.alert == "NodePackagesStale") | .annotations.description`)
	assert.Contains(t, stale, "status.nixos.org")
	assert.Contains(t, stale, "lock-file-maintenance-nodes")
	failed := rules("", `select(.alert == "NodeStoreVerifyFailed") | .annotations.description`)
	assert.Contains(t, failed, "journalctl -u nix-store-verify")
	assert.Contains(t, failed, "nix-store --repair-path")
}
