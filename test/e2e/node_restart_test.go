package e2e

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/duongle-steven/homelab/test/internal/restart"
	"github.com/duongle-steven/homelab/test/internal/testenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// appCodes is each sandbox app's status code through the Gateway (0 when unreachable).
func appCodes(hosts map[string]string) map[string]int {
	codes := map[string]int{}
	for app, host := range hosts {
		resp, err := insecureClient.Get("https://" + host)
		if err != nil {
			codes[app] = 0
			continue
		}
		codes[app] = resp.StatusCode
		_ = resp.Body.Close()
	}
	return codes
}

// clusterState reads the pods, the meshed namespaces and the ztunnel pods' readiness; ok is false while the API
// server does not answer.
func clusterState(t *testing.T) (pods []restart.Pod, meshed map[string]bool, ztunnelReady []bool, ok bool) {
	var list struct {
		Items []struct {
			Metadata struct {
				Namespace   string            `json:"namespace"`
				Name        string            `json:"name"`
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Spec struct {
				HostNetwork bool `json:"hostNetwork"`
			} `json:"spec"`
			Status struct {
				Phase             string `json:"phase"`
				ContainerStatuses []struct {
					Ready bool `json:"ready"`
					State struct {
						Running *struct {
							StartedAt time.Time `json:"startedAt"`
						} `json:"running"`
					} `json:"state"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if !legacyGetResource(t, "default", &list, "pods", "--all-namespaces") {
		return nil, nil, nil, false
	}
	var namespaces struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if !legacyGetResource(t, "default", &namespaces, "namespaces", "--selector=istio.io/dataplane-mode=ambient") {
		return nil, nil, nil, false
	}
	meshed = map[string]bool{}
	for _, n := range namespaces.Items {
		meshed[n.Metadata.Name] = true
	}
	for _, p := range list.Items {
		if p.Metadata.Namespace == "istio-system" && p.Metadata.Labels["app"] == "ztunnel" {
			ztunnelReady = append(ztunnelReady, len(p.Status.ContainerStatuses) > 0 && p.Status.ContainerStatuses[0].Ready)
		}
		pod := restart.Pod{
			Namespace:   p.Metadata.Namespace,
			Name:        p.Metadata.Name,
			Phase:       p.Status.Phase,
			Redirection: p.Metadata.Annotations["ambient.istio.io/redirection"],
			OptedOut:    p.Metadata.Labels["istio.io/dataplane-mode"] == "none" || p.Spec.HostNetwork,
			Ready:       len(p.Status.ContainerStatuses) > 0,
		}
		for _, c := range p.Status.ContainerStatuses {
			pod.Ready = pod.Ready && c.Ready
			if r := c.State.Running; r != nil && (pod.Started.IsZero() || r.StartedAt.Before(pod.Started)) {
				pod.Started = r.StartedAt
			}
		}
		pods = append(pods, pod)
	}
	return pods, meshed, ztunnelReady, true
}

// watchdogJobs reads the watchdog's Jobs and logs, once, what each finished Job printed (it prints only when it
// acts or skips a stuck node).
func watchdogJobs(t *testing.T, logged map[string]bool) []restart.WatchdogJob {
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				StartTime  time.Time `json:"startTime"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if !legacyGetResource(t, "istio-system", &list, "jobs") {
		return nil
	}
	var jobs []restart.WatchdogJob
	for _, item := range list.Items {
		if !strings.HasPrefix(item.Metadata.Name, "istio-cni-watchdog-") {
			continue
		}
		job := restart.WatchdogJob{Name: item.Metadata.Name, Started: item.Status.StartTime}
		finished := false
		for _, c := range item.Status.Conditions {
			if c.Status == "True" && (c.Type == "Complete" || c.Type == "Failed") {
				finished = true
				job.Failed = job.Failed || c.Type == "Failed"
			}
		}
		if finished && !logged[job.Name] {
			logged[job.Name] = true
			if out, err := exec.Command("kubectl", "--namespace", "istio-system", "logs", "job/"+job.Name).CombinedOutput(); err == nil && strings.TrimSpace(string(out)) != "" {
				t.Logf("%s: %s", job.Name, strings.TrimSpace(string(out)))
			}
		}
		jobs = append(jobs, job)
	}
	return jobs
}

// TestNodeRestart restarts the sandbox's node and waits until the mesh and the apps are back as they were: the
// istio-cni watchdog (system/istio-system) must break the deadlock described in
// docs/superpowers/specs/2026-10-18-mesh-node-restart-design.md. just dev runs it alone, after the other tests.
func TestNodeRestart(t *testing.T) {
	sandboxOnly(t, "it restarts or kills the k3d node's container (testbed: TestTestbed* and 12a-3)")
	hosts := map[string]string{}
	for _, app := range legacyDeployedSmokeApps(t, "dev") {
		hosts[app.Name] = legacyRouteHost(t, app.Namespace, app.Name)
	}
	var want map[string]int
	require.Eventually(t, func() bool {
		want = appCodes(hosts)
		pods, meshed, ztunnel, ok := clusterState(t)
		return ok && len(restart.Blockers(pods, meshed, ztunnel, want, want)) == 0 && !hasServerError(want)
	}, 10*time.Minute, 15*time.Second, "the sandbox is not healthy before the restart")
	t.Logf("before the restart: %v", want)

	out, err := exec.Command("docker", "restart", testenv.K3dNode).CombinedOutput()
	require.NoError(t, err, "docker restart %s: %s", testenv.K3dNode, out)
	restarted := time.Now()

	var blockers []string
	seen := map[string]restart.WatchdogJob{}
	logged := map[string]bool{}
	recovered := assert.Eventually(t, func() bool {
		// Polled with the rest: a finished Job is deleted after ttlSecondsAfterFinished
		for _, j := range watchdogJobs(t, logged) {
			seen[j.Name] = j
		}
		pods, meshed, ztunnel, ok := clusterState(t)
		if !ok {
			blockers = []string{"API server not answering"}
			return false
		}
		blockers = restart.Blockers(pods, meshed, ztunnel, want, appCodes(hosts))
		return len(blockers) == 0
	}, 20*time.Minute, 15*time.Second, "the sandbox did not recover from a node restart")
	if recovered {
		t.Logf("recovered %s after the restart", time.Since(restarted).Round(time.Second))
	} else {
		t.Logf("still blocking:\n%s", strings.Join(blockers, "\n"))
	}
	var jobs []restart.WatchdogJob
	for _, j := range seen {
		jobs = append(jobs, j)
	}
	assert.Empty(t, restart.FailedWatchdogJobs(jobs, restarted), "istio-cni-watchdog Jobs failed during the recovery")
}

// hasServerError reports a 5xx or unreachable app: the codes recorded before the restart must be the apps'
// healthy answers.
func hasServerError(codes map[string]int) bool {
	for _, code := range codes {
		if code == 0 || code >= 500 {
			return true
		}
	}
	return false
}

// nodeState reads the sandbox node's Ready condition and its last heartbeat; ok is false while the API server does
// not answer
func nodeState(t *testing.T) (ready bool, heartbeat time.Time, ok bool) {
	var node struct {
		Status struct {
			Conditions []struct {
				Type              string    `json:"type"`
				Status            string    `json:"status"`
				LastHeartbeatTime time.Time `json:"lastHeartbeatTime"`
			} `json:"conditions"`
		} `json:"status"`
	}
	if !legacyGetResource(t, "default", &node, "node", testenv.K3dNode) {
		return false, time.Time{}, false
	}
	for _, c := range node.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status == "True", c.LastHeartbeatTime, true
		}
	}
	return false, time.Time{}, true
}

// TestNodePowerCut cuts the sandbox node's power (docker kill: SIGKILL, nothing flushed, no graceful stop), starts it
// again, and waits until it is really back: the node Ready after the cut, every pod restarted and ready, the mesh as
// TestNodeRestart wants it and the apps answering as before. Data acknowledged before the cut must be there: an issue
// in Forgejo, a message in ntfy, a secret in OpenBao (docs/superpowers/specs/2026-10-25-power-cut-design.md). just
// dev runs it alone, after TestNodeRestart.
func TestNodePowerCut(t *testing.T) {
	sandboxOnly(t, "it restarts or kills the k3d node's container (testbed: TestTestbed* and 12a-3)")
	hosts := map[string]string{}
	for _, app := range legacyDeployedSmokeApps(t, "dev") {
		hosts[app.Name] = legacyRouteHost(t, app.Namespace, app.Name)
	}
	var want map[string]int
	// As strict as the recovery below (every pod ready, the node Ready), so a pod already failing is not blamed on the
	// cut: before it, any start time and heartbeat count
	require.Eventually(t, func() bool {
		want = appCodes(hosts)
		pods, meshed, ztunnel, ok := clusterState(t)
		ready, heartbeat, nodeOK := nodeState(t)
		return ok && nodeOK && len(restart.Blockers(pods, meshed, ztunnel, want, want)) == 0 &&
			len(restart.PowerCutBlockers(pods, ready, heartbeat, time.Time{})) == 0 && !hasServerError(want)
	}, 10*time.Minute, 15*time.Second, "the sandbox is not healthy before the cut")
	t.Logf("before the cut: %v", want)

	// Markers are test strings, never secrets
	marker := fmt.Sprint("power-cut-", time.Now().UnixNano())
	repo := "/repos/" + secretValue(context.TODO(), "forgejo", "forgejo-admin", "username") + "/power-cut"
	forgejoAs(t, "", "DELETE", repo, nil, nil)
	require.Equal(t, http.StatusCreated, forgejoAs(t, "", "POST", "/user/repos", map[string]any{"name": "power-cut"}, nil))
	defer forgejoAs(t, "", "DELETE", repo, nil, nil)
	var issue struct {
		Number int `json:"number"`
	}
	require.Equal(t, http.StatusCreated, forgejoAs(t, "", "POST", repo+"/issues", map[string]any{"title": marker}, &issue))
	status, body := ntfyRequest(t, http.MethodPost, "/alerts", ntfyToken(t, "alerts"), marker)
	require.Equal(t, http.StatusOK, status, body)
	admin(t, "kv", "put", "secret/smoke/power-cut", "value="+marker)
	defer admin(t, "kv", "metadata", "delete", "secret/smoke/power-cut")
	time.Sleep(5 * time.Second)

	out, err := exec.Command("docker", "kill", testenv.K3dNode).CombinedOutput()
	require.NoError(t, err, "docker kill %s: %s", testenv.K3dNode, out)
	cut := time.Now()
	out, err = exec.Command("docker", "start", testenv.K3dNode).CombinedOutput()
	require.NoError(t, err, "docker start %s: %s", testenv.K3dNode, out)

	var blockers []string
	recovered := assert.Eventually(t, func() bool {
		pods, meshed, ztunnel, ok := clusterState(t)
		ready, heartbeat, nodeOK := nodeState(t)
		if !ok || !nodeOK {
			blockers = []string{"API server not answering"}
			return false
		}
		blockers = append(restart.PowerCutBlockers(pods, ready, heartbeat, cut), restart.Blockers(pods, meshed, ztunnel, want, appCodes(hosts))...)
		return len(blockers) == 0
	}, 20*time.Minute, 15*time.Second, "the sandbox did not recover from a power cut")
	if !recovered {
		t.Fatalf("still blocking:\n%s", strings.Join(blockers, "\n"))
	}
	t.Logf("recovered %s after the cut", time.Since(cut).Round(time.Second))

	var got struct {
		Title string `json:"title"`
	}
	assert.Equal(t, http.StatusOK, forgejoAs(t, "", "GET", fmt.Sprintf("%s/issues/%d", repo, issue.Number), nil, &got))
	assert.Equal(t, marker, got.Title, "Forgejo's issue")
	status, body = ntfyRequest(t, http.MethodGet, "/alerts/json?poll=1&since=1h", ntfyToken(t, "phone"), "")
	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, marker, "ntfy's message")
	waitForUnsealed(t)
	assert.Equal(t, marker, admin(t, "kv", "get", "-field=value", "secret/smoke/power-cut"), "OpenBao's secret")
}
