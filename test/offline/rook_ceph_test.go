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
