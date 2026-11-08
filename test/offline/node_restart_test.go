package offline

import (
	"strings"
	"testing"
	"time"

	"github.com/duongle-steven/homelab/test/internal/restart"
	"github.com/stretchr/testify/assert"
)

func TestNodeRestartBlockers(t *testing.T) {
	ok := map[string]int{"excalidraw": 200}
	running := restart.Pod{Namespace: "excalidraw", Name: "excalidraw-1", Phase: "Running", Redirection: "enabled"}
	for _, c := range []struct {
		name    string
		pods    []restart.Pod
		ztunnel []bool
		got     map[string]int
		want    string
	}{
		{"recovered", []restart.Pod{running}, []bool{true}, ok, ""},
		{"ztunnel not ready", []restart.Pod{running}, []bool{false}, ok, "ztunnel"},
		{"no ztunnel pod", []restart.Pod{running}, nil, ok, "ztunnel"},
		{"pod pending", []restart.Pod{{Namespace: "excalidraw", Name: "excalidraw-1", Phase: "Pending", Redirection: "pending"}}, []bool{true}, ok, "excalidraw/excalidraw-1"},
		{"not redirected", []restart.Pod{{Namespace: "excalidraw", Name: "excalidraw-1", Phase: "Running", Redirection: "pending"}}, []bool{true}, ok, "not redirected"},
		{"opted out", []restart.Pod{{Namespace: "cert-manager", Name: "webhook-1", Phase: "Running", OptedOut: true}}, []bool{true}, ok, ""},
		{"outside the mesh", []restart.Pod{{Namespace: "kube-system", Name: "coredns-1", Phase: "Running"}}, []bool{true}, ok, ""},
		{"finished job ignored", []restart.Pod{running, {Namespace: "istio-system", Name: "istio-cni-watchdog-1", Phase: "Succeeded"}, {Namespace: "forgejo", Name: "seed-1", Phase: "Failed"}}, []bool{true}, ok, ""},
		{"app code differs", []restart.Pod{running}, []bool{true}, map[string]int{"excalidraw": 503}, "excalidraw answers 503"},
		{"app unreachable", []restart.Pod{running}, []bool{true}, map[string]int{}, "excalidraw answers 0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			meshed := map[string]bool{"excalidraw": true, "cert-manager": true}
			blockers := strings.Join(restart.Blockers(c.pods, meshed, c.ztunnel, ok, c.got), "\n")
			if c.want == "" {
				assert.Empty(t, blockers)
			} else {
				assert.Contains(t, blockers, c.want)
			}
		})
	}
}

func TestNodeRestartWatchdogJobs(t *testing.T) {
	restarted := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	jobs := []restart.WatchdogJob{
		{Name: "istio-cni-watchdog-1", Started: restarted.Add(-time.Minute), Failed: true},
		{Name: "istio-cni-watchdog-2", Started: restarted.Add(time.Minute)},
		{Name: "istio-cni-watchdog-3", Started: restarted.Add(2 * time.Minute), Failed: true},
	}
	assert.Equal(t, []string{"istio-cni-watchdog-3"}, restart.FailedWatchdogJobs(jobs, restarted),
		"only a Job that failed after the restart counts")
	assert.Empty(t, restart.FailedWatchdogJobs(jobs[:2], restarted))
}

func TestNodePowerCutBlockers(t *testing.T) {
	cut := time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC)
	after, before := cut.Add(3*time.Minute), cut.Add(-time.Hour)
	fresh := restart.Pod{Namespace: "ntfy", Name: "ntfy-1", Phase: "Running", Ready: true, Started: after}
	for _, c := range []struct {
		name      string
		pods      []restart.Pod
		heartbeat time.Time
		nodeReady bool
		want      string
	}{
		{"recovered", []restart.Pod{fresh}, after, true, ""},
		{"finished pods ignored", []restart.Pod{fresh, {Namespace: "velero", Name: "prune-1", Phase: "Succeeded", Started: before}}, after, true, ""},
		// Right after the restart the API server still reports what it knew before the cut
		{"pod state from before the cut", []restart.Pod{{Namespace: "ntfy", Name: "ntfy-1", Phase: "Running", Ready: true, Started: before}}, after, true, "not restarted since the cut"},
		{"node heartbeat from before the cut", []restart.Pod{fresh}, before, true, "node"},
		{"node not Ready", []restart.Pod{fresh}, after, false, "node"},
		{"container not ready", []restart.Pod{{Namespace: "ntfy", Name: "ntfy-1", Phase: "Running", Started: after}}, after, true, "not ready"},
		{"pod Unknown", []restart.Pod{{Namespace: "ntfy", Name: "ntfy-1", Phase: "Unknown"}}, after, true, "ntfy/ntfy-1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			blockers := strings.Join(restart.PowerCutBlockers(c.pods, c.nodeReady, c.heartbeat, cut), "\n")
			if c.want == "" {
				assert.Empty(t, blockers)
			} else {
				assert.Contains(t, blockers, c.want)
			}
		})
	}
	// Before the cut (a zero cut time) only readiness counts
	settled := restart.Pod{Namespace: "ntfy", Name: "ntfy-1", Phase: "Running", Ready: true, Started: before}
	assert.Empty(t, restart.PowerCutBlockers([]restart.Pod{settled}, true, before, time.Time{}))
	settled.Ready = false
	assert.NotEmpty(t, restart.PowerCutBlockers([]restart.Pod{settled}, true, before, time.Time{}))
}
