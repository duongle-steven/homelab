package offline

import (
	"testing"

	"github.com/duongle-steven/homelab/test/internal/render"
	"github.com/stretchr/testify/assert"
)

// CephX keys are AES256K (CVE-2025-30156): only that cipher is allowed, the CSI clients' keys are rotated to it (the
// nodes' kernel is 7.0 or later, TestMetalKernel), and no environment mutes Ceph's AUTH_INSECURE_* warnings
func TestRookCephAES256K(t *testing.T) {
	const cluster = `select(.kind == "CephCluster") | .spec`
	for _, env := range []string{"", "testbed"} {
		assert.Equal(t, "- aes256k", render.Yq(t, "system/rook-ceph", env, cluster+".security.cephx.allowedCiphers"), env)
		assert.Equal(t, "aes256k", render.Yq(t, "system/rook-ceph", env, cluster+".security.cephx.csi.keyType"), env)
		assert.Equal(t, "KeyGeneration", render.Yq(t, "system/rook-ceph", env, cluster+".security.cephx.csi.keyRotationPolicy"), env)
		assert.Equal(t, "KeyGeneration", render.Yq(t, "system/rook-ceph", env, cluster+".security.cephx.daemon.keyRotationPolicy"), env)
		assert.Empty(t, render.Yq(t, "system/rook-ceph", env,
			cluster+`.healthCheck.muteHealthWarning // {} | keys | .[] | select(test("^AUTH_INSECURE"))`), env)
	}
}

// The testbed's Ceph does without what nothing there reads (docs/installation/testbed.md, 2026-11-26): settled, Ceph
// took 0.9 of a core across the VMs, more than anything else. No dashboard, no device health (SMART on virtual disks),
// and the daemons' liveness probes, each a Python ceph call, every 30 s instead of 10; production keeps them all
func TestTestbedCephLighter(t *testing.T) {
	cluster := func(env, expr string) string {
		return renderVelero(t, "system/rook-ceph", env, `select(.kind == "CephCluster") | `+expr)
	}
	for env, want := range map[string][]string{
		"":        {"true", "", ""},
		"testbed": {"false", "false", "30 30 30"},
	} {
		assert.Equal(t, want[0], cluster(env, `.spec.dashboard.enabled | tostring`), "dashboard, env %q", env)
		assert.Equal(t, want[1], cluster(env, `.spec.cephConfig.mgr["mgr/devicehealth/enable_monitoring"] // ""`), "devicehealth, env %q", env)
		assert.Equal(t, want[2], cluster(env, `[.spec.healthCheck.livenessProbe.mon.probe.periodSeconds, .spec.healthCheck.livenessProbe.mgr.probe.periodSeconds, .spec.healthCheck.livenessProbe.osd.probe.periodSeconds] | map(select(. != null)) | join(" ")`), "probes, env %q", env)
	}
}
