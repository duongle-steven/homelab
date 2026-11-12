package offline

import (
	"testing"

	"github.com/duongle-steven/homelab/test/internal/render"
	"github.com/stretchr/testify/assert"
)

// etcd's disk (12b-1's finding: homelab-1's disk, accepted and watched): an alert says when it must be fixed
func TestEtcdRules(t *testing.T) {
	for _, env := range []string{"", "dev"} {
		rules := func(expr string) string {
			return render.Yq(t, "system/monitoring-system", env,
				`select(.kind == "PrometheusRule" and .metadata.name == "etcd") | .spec.groups[].rules[] | `+expr)
		}
		assert.Equal(t, "EtcdSlowDisk\nEtcdLeaderChanges", rules(".alert"), "env=%q", env)
		assert.Equal(t, `histogram_quantile(0.99, rate(etcd_disk_wal_fsync_duration_seconds_bucket{job="kube-etcd"}[10m])) > 0.010`,
			rules(`select(.alert == "EtcdSlowDisk") | .expr | trim`))
		assert.Equal(t, "1h", rules(`select(.alert == "EtcdSlowDisk") | .for`))
		assert.Equal(t, `increase(etcd_server_leader_changes_seen_total{job="kube-etcd"}[1h]) > 3`,
			rules(`select(.alert == "EtcdLeaderChanges") | .expr | trim`))
		assert.Equal(t, "warning\nwarning", rules(".labels.severity"))
		assert.Contains(t, rules(`select(.alert == "EtcdSlowDisk") | .annotations.description`), "homelab-1")
	}
}
