package offline

import (
	"testing"

	"github.com/duongle-steven/homelab/test/internal/render"
	"github.com/stretchr/testify/assert"
)

// k3s's own workloads in kube-system break restricted and k3s rewrites their manifests at every start, so each has a
// PolicyException with its reason, applied after Kyverno's own workloads: Kyverno's exception webhook fails closed, so
// an exception before them would hold Kyverno's own sync while it is down
// (docs/superpowers/specs/2026-11-27-policy-report-alert-design.md)
func TestKubeSystemExceptions(t *testing.T) {
	exceptions := func(env, expr string) string {
		return render.Yq(t, "system/kyverno", env,
			`select(.kind == "PolicyException" and .metadata.namespace == "kube-system") | `+expr)
	}
	assert.Equal(t, "coredns\nkube-vip\nmetrics-server", exceptions("", ".metadata.name"))
	assert.Equal(t, "coredns\nkube-vip\nmetrics-server\nsvclb", exceptions("dev", ".metadata.name"), "ServiceLB runs only on k3d")
	assert.Equal(t, "1\n1\n1\n1", exceptions("dev", `.metadata.annotations["argocd.argoproj.io/sync-wave"]`))
	assert.NotContains(t, exceptions("dev", `.metadata.annotations["homelab/reason"] // "missing"`), "missing")
	assert.Equal(t, "restrict-seccomp-strict", exceptions("", `select(.metadata.name == "metrics-server") | .spec.policyRefs[].name`))
	assert.Equal(t, "object.metadata.namespace == 'kube-system' && (object.metadata.name.startsWith('coredns'))",
		exceptions("", `select(.metadata.name == "coredns") | .spec.matchConditions[0].expression`))
}
