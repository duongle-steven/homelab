package test

import (
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNodeRestartBlockers(t *testing.T) {
	ok := map[string]int{"excalidraw": 200}
	running := restartPod{Namespace: "excalidraw", Name: "excalidraw-1", Phase: "Running", Redirection: "enabled"}
	for _, c := range []struct {
		name    string
		pods    []restartPod
		ztunnel []bool
		got     map[string]int
		want    string
	}{
		{"recovered", []restartPod{running}, []bool{true}, ok, ""},
		{"ztunnel not ready", []restartPod{running}, []bool{false}, ok, "ztunnel"},
		{"no ztunnel pod", []restartPod{running}, nil, ok, "ztunnel"},
		{"pod pending", []restartPod{{Namespace: "excalidraw", Name: "excalidraw-1", Phase: "Pending", Redirection: "pending"}}, []bool{true}, ok, "excalidraw/excalidraw-1"},
		{"not redirected", []restartPod{{Namespace: "excalidraw", Name: "excalidraw-1", Phase: "Running", Redirection: "pending"}}, []bool{true}, ok, "not redirected"},
		{"opted out", []restartPod{{Namespace: "cert-manager", Name: "webhook-1", Phase: "Running", OptedOut: true}}, []bool{true}, ok, ""},
		{"outside the mesh", []restartPod{{Namespace: "kube-system", Name: "coredns-1", Phase: "Running"}}, []bool{true}, ok, ""},
		{"finished job ignored", []restartPod{running, {Namespace: "istio-system", Name: "istio-cni-watchdog-1", Phase: "Succeeded"}, {Namespace: "forgejo", Name: "seed-1", Phase: "Failed"}}, []bool{true}, ok, ""},
		{"app code differs", []restartPod{running}, []bool{true}, map[string]int{"excalidraw": 503}, "excalidraw answers 503"},
		{"app unreachable", []restartPod{running}, []bool{true}, map[string]int{}, "excalidraw answers 0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			meshed := map[string]bool{"excalidraw": true, "cert-manager": true}
			blockers := strings.Join(restartBlockers(c.pods, meshed, c.ztunnel, ok, c.got), "\n")
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
	jobs := []watchdogJob{
		{Name: "istio-cni-watchdog-1", Started: restarted.Add(-time.Minute), Failed: true},
		{Name: "istio-cni-watchdog-2", Started: restarted.Add(time.Minute)},
		{Name: "istio-cni-watchdog-3", Started: restarted.Add(2 * time.Minute), Failed: true},
	}
	assert.Equal(t, []string{"istio-cni-watchdog-3"}, failedWatchdogJobs(jobs, restarted),
		"only a Job that failed after the restart counts")
	assert.Empty(t, failedWatchdogJobs(jobs[:2], restarted))
}

// restartPod is what TestNodeRestart needs from a pod.
type restartPod struct {
	Namespace, Name, Phase, Redirection string
	OptedOut                            bool // labelled istio.io/dataplane-mode=none, or on the host network
	// Ready: every container ready; Started: when the earliest running container started (TestNodePowerCut)
	Ready   bool
	Started time.Time
}

// restartBlockers lists what still keeps the cluster from being recovered after a node restart: ztunnel not Ready,
// pods not running (finished Job pods aside), meshed pods not redirected to ztunnel, and apps that do not answer
// with the code they gave before the restart (0: unreachable).
func restartBlockers(pods []restartPod, meshed map[string]bool, ztunnelReady []bool, want, got map[string]int) []string {
	var blockers []string
	if len(ztunnelReady) == 0 {
		blockers = append(blockers, "no ztunnel pod")
	}
	for i, ready := range ztunnelReady {
		if !ready {
			blockers = append(blockers, fmt.Sprintf("ztunnel %d not Ready", i))
		}
	}
	for _, p := range pods {
		switch {
		case p.Phase == "Succeeded" || p.Phase == "Failed":
		case p.Phase != "Running":
			blockers = append(blockers, fmt.Sprintf("%s/%s is %s", p.Namespace, p.Name, p.Phase))
		case meshed[p.Namespace] && !p.OptedOut && p.Redirection != "enabled":
			blockers = append(blockers, fmt.Sprintf("%s/%s not redirected to ztunnel (%q)", p.Namespace, p.Name, p.Redirection))
		}
	}
	for app, code := range want {
		if got[app] != code {
			blockers = append(blockers, fmt.Sprintf("%s answers %d, %d before the restart", app, got[app], code))
		}
	}
	return blockers
}

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
func clusterState(t *testing.T) (pods []restartPod, meshed map[string]bool, ztunnelReady []bool, ok bool) {
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
	if !getResource(t, "default", &list, "pods", "--all-namespaces") {
		return nil, nil, nil, false
	}
	var namespaces struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if !getResource(t, "default", &namespaces, "namespaces", "--selector=istio.io/dataplane-mode=ambient") {
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
		pod := restartPod{
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

// watchdogJob is a Job of the CronJob istio-system/istio-cni-watchdog.
type watchdogJob struct {
	Name    string
	Started time.Time
	Failed  bool
}

// failedWatchdogJobs names the watchdog Jobs that failed after the restart: ipv6 off alone can recover the node, so
// without this TestNodeRestart would stay green with a broken watchdog (image, RBAC, BusyBox date).
func failedWatchdogJobs(jobs []watchdogJob, since time.Time) []string {
	var failed []string
	for _, j := range jobs {
		if j.Failed && !j.Started.Before(since) {
			failed = append(failed, j.Name)
		}
	}
	return failed
}

// watchdogJobs reads the watchdog's Jobs and logs, once, what each finished Job printed (it prints only when it
// acts or skips a stuck node).
func watchdogJobs(t *testing.T, logged map[string]bool) []watchdogJob {
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
	if !getResource(t, "istio-system", &list, "jobs") {
		return nil
	}
	var jobs []watchdogJob
	for _, item := range list.Items {
		if !strings.HasPrefix(item.Metadata.Name, "istio-cni-watchdog-") {
			continue
		}
		job := watchdogJob{Name: item.Metadata.Name, Started: item.Status.StartTime}
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
// docs/superpowers/specs/2026-10-18-mesh-node-restart-design.md. make dev runs it alone, after the other tests.
func TestNodeRestart(t *testing.T) {
	meshSandboxOnly(t)
	hosts := map[string]string{}
	for _, app := range deployedSmokeApps(t, "dev") {
		hosts[app.name] = routeHost(t, app.namespace, app.name)
	}
	var want map[string]int
	require.Eventually(t, func() bool {
		want = appCodes(hosts)
		pods, meshed, ztunnel, ok := clusterState(t)
		return ok && len(restartBlockers(pods, meshed, ztunnel, want, want)) == 0 && !hasServerError(want)
	}, 10*time.Minute, 15*time.Second, "the sandbox is not healthy before the restart")
	t.Logf("before the restart: %v", want)

	out, err := exec.Command("docker", "restart", k3dNode).CombinedOutput()
	require.NoError(t, err, "docker restart %s: %s", k3dNode, out)
	restarted := time.Now()

	var blockers []string
	seen := map[string]watchdogJob{}
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
		blockers = restartBlockers(pods, meshed, ztunnel, want, appCodes(hosts))
		return len(blockers) == 0
	}, 20*time.Minute, 15*time.Second, "the sandbox did not recover from a node restart")
	if recovered {
		t.Logf("recovered %s after the restart", time.Since(restarted).Round(time.Second))
	} else {
		t.Logf("still blocking:\n%s", strings.Join(blockers, "\n"))
	}
	var jobs []watchdogJob
	for _, j := range seen {
		jobs = append(jobs, j)
	}
	assert.Empty(t, failedWatchdogJobs(jobs, restarted), "istio-cni-watchdog Jobs failed during the recovery")
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

func TestNodePowerCutBlockers(t *testing.T) {
	cut := time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC)
	after, before := cut.Add(3*time.Minute), cut.Add(-time.Hour)
	fresh := restartPod{Namespace: "ntfy", Name: "ntfy-1", Phase: "Running", Ready: true, Started: after}
	for _, c := range []struct {
		name      string
		pods      []restartPod
		heartbeat time.Time
		nodeReady bool
		want      string
	}{
		{"recovered", []restartPod{fresh}, after, true, ""},
		{"finished pods ignored", []restartPod{fresh, {Namespace: "velero", Name: "prune-1", Phase: "Succeeded", Started: before}}, after, true, ""},
		// Right after the restart the API server still reports what it knew before the cut
		{"pod state from before the cut", []restartPod{{Namespace: "ntfy", Name: "ntfy-1", Phase: "Running", Ready: true, Started: before}}, after, true, "not restarted since the cut"},
		{"node heartbeat from before the cut", []restartPod{fresh}, before, true, "node"},
		{"node not Ready", []restartPod{fresh}, after, false, "node"},
		{"container not ready", []restartPod{{Namespace: "ntfy", Name: "ntfy-1", Phase: "Running", Started: after}}, after, true, "not ready"},
		{"pod Unknown", []restartPod{{Namespace: "ntfy", Name: "ntfy-1", Phase: "Unknown"}}, after, true, "ntfy/ntfy-1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			blockers := strings.Join(powerCutBlockers(c.pods, c.nodeReady, c.heartbeat, cut), "\n")
			if c.want == "" {
				assert.Empty(t, blockers)
			} else {
				assert.Contains(t, blockers, c.want)
			}
		})
	}
	// Before the cut (a zero cut time) only readiness counts
	settled := restartPod{Namespace: "ntfy", Name: "ntfy-1", Phase: "Running", Ready: true, Started: before}
	assert.Empty(t, powerCutBlockers([]restartPod{settled}, true, before, time.Time{}))
	settled.Ready = false
	assert.NotEmpty(t, powerCutBlockers([]restartPod{settled}, true, before, time.Time{}))
}

// powerCutBlockers lists what restartBlockers cannot see after a power cut, while the API server still reports what it
// knew before: the node not Ready or its last heartbeat from before the cut, and pods (finished ones aside) whose
// containers are not all ready or started before the cut (the cut killed every container on the node).
func powerCutBlockers(pods []restartPod, nodeReady bool, heartbeat, cut time.Time) []string {
	var blockers []string
	if !nodeReady || !heartbeat.After(cut) {
		blockers = append(blockers, fmt.Sprintf("node: Ready %t, last heartbeat %s", nodeReady, heartbeat.Format(time.TimeOnly)))
	}
	for _, p := range pods {
		switch {
		case p.Phase == "Succeeded" || p.Phase == "Failed":
		case p.Phase != "Running":
			blockers = append(blockers, fmt.Sprintf("%s/%s is %s", p.Namespace, p.Name, p.Phase))
		case !p.Started.After(cut):
			blockers = append(blockers, fmt.Sprintf("%s/%s not restarted since the cut", p.Namespace, p.Name))
		case !p.Ready:
			blockers = append(blockers, fmt.Sprintf("%s/%s not ready", p.Namespace, p.Name))
		}
	}
	return blockers
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
	if !getResource(t, "default", &node, "node", k3dNode) {
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
// in Forgejo, a message in ntfy, a secret in OpenBao (docs/superpowers/specs/2026-10-25-power-cut-design.md). make
// dev runs it alone, after TestNodeRestart.
func TestNodePowerCut(t *testing.T) {
	meshSandboxOnly(t)
	hosts := map[string]string{}
	for _, app := range deployedSmokeApps(t, "dev") {
		hosts[app.name] = routeHost(t, app.namespace, app.name)
	}
	var want map[string]int
	// As strict as the recovery below (every pod ready, the node Ready), so a pod already failing is not blamed on the
	// cut: before it, any start time and heartbeat count
	require.Eventually(t, func() bool {
		want = appCodes(hosts)
		pods, meshed, ztunnel, ok := clusterState(t)
		ready, heartbeat, nodeOK := nodeState(t)
		return ok && nodeOK && len(restartBlockers(pods, meshed, ztunnel, want, want)) == 0 &&
			len(powerCutBlockers(pods, ready, heartbeat, time.Time{})) == 0 && !hasServerError(want)
	}, 10*time.Minute, 15*time.Second, "the sandbox is not healthy before the cut")
	t.Logf("before the cut: %v", want)

	// Markers are test strings, never secrets
	marker := fmt.Sprint("power-cut-", time.Now().UnixNano())
	repo := "/repos/" + secretValue(t, "forgejo", "forgejo-admin", "username") + "/power-cut"
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

	out, err := exec.Command("docker", "kill", k3dNode).CombinedOutput()
	require.NoError(t, err, "docker kill %s: %s", k3dNode, out)
	cut := time.Now()
	out, err = exec.Command("docker", "start", k3dNode).CombinedOutput()
	require.NoError(t, err, "docker start %s: %s", k3dNode, out)

	var blockers []string
	recovered := assert.Eventually(t, func() bool {
		pods, meshed, ztunnel, ok := clusterState(t)
		ready, heartbeat, nodeOK := nodeState(t)
		if !ok || !nodeOK {
			blockers = []string{"API server not answering"}
			return false
		}
		blockers = append(powerCutBlockers(pods, ready, heartbeat, cut), restartBlockers(pods, meshed, ztunnel, want, appCodes(hosts))...)
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
