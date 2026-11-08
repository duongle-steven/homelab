package e2e

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	"github.com/duongle-steven/homelab/test/internal/restart"
	"github.com/duongle-steven/homelab/test/internal/testenv"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// clusterState reads the pods, the meshed namespaces and the ztunnel pods' readiness; ok is false while the API
// server does not answer.
func clusterState(ctx context.Context) (pods []restart.Pod, meshed map[string]bool, ztunnelReady []bool, ok bool) {
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
	if !getResource(ctx, "default", &list, "pods", "--all-namespaces") {
		return nil, nil, nil, false
	}
	var namespaces struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if !getResource(ctx, "default", &namespaces, "namespaces", "--selector=istio.io/dataplane-mode=ambient") {
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
func watchdogJobs(ctx context.Context, logged map[string]bool) []restart.WatchdogJob {
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
	if !getResource(ctx, "istio-system", &list, "jobs") {
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
			if out, err := kube.Kubectl(ctx, "istio-system", "logs", "job/"+job.Name); err == nil && strings.TrimSpace(out) != "" {
				GinkgoWriter.Printf("%s: %s\n", job.Name, strings.TrimSpace(out))
			}
		}
		jobs = append(jobs, job)
	}
	return jobs
}

// healthyHosts waits until every deployed smoke app's route is ready and returns their hosts
func healthyHosts(ctx context.Context) map[string]string {
	hosts := map[string]string{}
	for _, app := range deployedSmokeApps(ctx, "dev") {
		hosts[app.Name] = routeHost(ctx, app.Namespace, app.Name)
	}
	return hosts
}

// docker runs the docker CLI on the sandbox's node (restart, kill, start) and fails the spec on an error
func docker(ctx context.Context, verb string) {
	GinkgoHelper()
	out, err := exec.CommandContext(ctx, "docker", verb, testenv.K3dNode).CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "docker %s %s: %s", verb, testenv.K3dNode, out)
}

// NodeRestart restarts the sandbox's node and waits until the mesh and the apps are back as they were: the istio-cni
// watchdog (system/istio-system) must break the deadlock described in
// docs/superpowers/specs/2026-10-18-mesh-node-restart-design.md. just dev runs it alone, after the other specs. It
// restarts the k3d node's container: the sandbox only (the testbed: Testbed* and 12a-3)
var _ = Describe("NodeRestart", Serial, Label("sandbox", "node-restart", "alone"), func() {
	It("recovers from a node restart", func(ctx SpecContext) {
		hosts := healthyHosts(ctx)
		var want map[string]int
		Eventually(ctx, func() bool {
			want = appCodes(ctx, hosts)
			pods, meshed, ztunnel, ok := clusterState(ctx)
			return ok && len(restart.Blockers(pods, meshed, ztunnel, want, want)) == 0 && !hasServerError(want)
		}).WithTimeout(10*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "the sandbox is not healthy before the restart")
		GinkgoWriter.Printf("before the restart: %v\n", want)

		docker(ctx, "restart")
		restarted := time.Now()

		var blockers []string
		seen := map[string]restart.WatchdogJob{}
		logged := map[string]bool{}
		watchdogFailures := func() []string {
			var jobs []restart.WatchdogJob
			for _, j := range seen {
				jobs = append(jobs, j)
			}
			return restart.FailedWatchdogJobs(jobs, restarted)
		}
		Eventually(ctx, func() bool {
			// Polled with the rest: a finished Job is deleted after ttlSecondsAfterFinished
			for _, j := range watchdogJobs(ctx, logged) {
				seen[j.Name] = j
			}
			pods, meshed, ztunnel, ok := clusterState(ctx)
			if !ok {
				blockers = []string{"API server not answering"}
				return false
			}
			blockers = restart.Blockers(pods, meshed, ztunnel, want, appCodes(ctx, hosts))
			return len(blockers) == 0
		}).WithTimeout(20*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), func() string {
			return fmt.Sprintf("the sandbox did not recover from a node restart; still blocking:\n%s\nfailed watchdog Jobs: %v",
				strings.Join(blockers, "\n"), watchdogFailures())
		})
		GinkgoWriter.Printf("recovered %s after the restart\n", time.Since(restarted).Round(time.Second))
		Expect(watchdogFailures()).To(BeEmpty(), "istio-cni-watchdog Jobs failed during the recovery")
	}, SpecTimeout(65*time.Minute))
})

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
func nodeState(ctx context.Context) (ready bool, heartbeat time.Time, ok bool) {
	var node struct {
		Status struct {
			Conditions []struct {
				Type              string    `json:"type"`
				Status            string    `json:"status"`
				LastHeartbeatTime time.Time `json:"lastHeartbeatTime"`
			} `json:"conditions"`
		} `json:"status"`
	}
	if !getResource(ctx, "default", &node, "node", testenv.K3dNode) {
		return false, time.Time{}, false
	}
	for _, c := range node.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status == "True", c.LastHeartbeatTime, true
		}
	}
	return false, time.Time{}, true
}

// NodePowerCut cuts the sandbox node's power (docker kill: SIGKILL, nothing flushed, no graceful stop), starts it
// again, and waits until it is really back: the node Ready after the cut, every pod restarted and ready, the mesh as
// NodeRestart wants it and the apps answering as before. Data acknowledged before the cut must be there: an issue in
// Forgejo, a message in ntfy, a secret in OpenBao (docs/superpowers/specs/2026-10-25-power-cut-design.md). just dev
// runs it alone, after NodeRestart. The sandbox only, as NodeRestart
var _ = Describe("NodePowerCut", Serial, Label("sandbox", "node-power-cut", "alone"), func() {
	It("recovers from a power cut with its data", func(ctx SpecContext) {
		hosts := healthyHosts(ctx)
		var want map[string]int
		// As strict as the recovery below (every pod ready, the node Ready), so a pod already failing is not blamed on
		// the cut: before it, any start time and heartbeat count
		Eventually(ctx, func() bool {
			want = appCodes(ctx, hosts)
			pods, meshed, ztunnel, ok := clusterState(ctx)
			ready, heartbeat, nodeOK := nodeState(ctx)
			return ok && nodeOK && len(restart.Blockers(pods, meshed, ztunnel, want, want)) == 0 &&
				len(restart.PowerCutBlockers(pods, ready, heartbeat, time.Time{})) == 0 && !hasServerError(want)
		}).WithTimeout(10*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "the sandbox is not healthy before the cut")
		GinkgoWriter.Printf("before the cut: %v\n", want)

		// Markers are test strings, never secrets
		marker := fmt.Sprint("power-cut-", time.Now().UnixNano())
		repo := "/repos/" + secretValue(ctx, "forgejo", "forgejo-admin", "username") + "/power-cut"
		forgejoAs(ctx, "", "DELETE", repo, nil, nil)
		Expect(forgejoAs(ctx, "", "POST", "/user/repos", map[string]any{"name": "power-cut"}, nil)).To(Equal(http.StatusCreated))
		DeferCleanup(func(ctx SpecContext) { forgejoAs(ctx, "", "DELETE", repo, nil, nil) }, NodeTimeout(5*time.Minute))
		var issue struct {
			Number int `json:"number"`
		}
		Expect(forgejoAs(ctx, "", "POST", repo+"/issues", map[string]any{"title": marker}, &issue)).To(Equal(http.StatusCreated))
		status, body := ntfyRequest(ctx, http.MethodPost, "/alerts", ntfyToken(ctx, "alerts"), marker)
		Expect(status).To(Equal(http.StatusOK), body)
		admin(ctx, "kv", "put", "secret/smoke/power-cut", "value="+marker)
		deferAdmin("kv", "metadata", "delete", "secret/smoke/power-cut")
		sleep(ctx, 5*time.Second)

		docker(ctx, "kill")
		cut := time.Now()
		docker(ctx, "start")

		var blockers []string
		Eventually(ctx, func() bool {
			pods, meshed, ztunnel, ok := clusterState(ctx)
			ready, heartbeat, nodeOK := nodeState(ctx)
			if !ok || !nodeOK {
				blockers = []string{"API server not answering"}
				return false
			}
			blockers = append(restart.PowerCutBlockers(pods, ready, heartbeat, cut), restart.Blockers(pods, meshed, ztunnel, want, appCodes(ctx, hosts))...)
			return len(blockers) == 0
		}).WithTimeout(20*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), func() string {
			return fmt.Sprintf("the sandbox did not recover from a power cut; still blocking:\n%s", strings.Join(blockers, "\n"))
		})
		GinkgoWriter.Printf("recovered %s after the cut\n", time.Since(cut).Round(time.Second))

		var got struct {
			Title string `json:"title"`
		}
		Expect(forgejoAs(ctx, "", "GET", fmt.Sprintf("%s/issues/%d", repo, issue.Number), nil, &got)).To(Equal(http.StatusOK))
		Expect(got.Title).To(Equal(marker), "Forgejo's issue")
		status, body = ntfyRequest(ctx, http.MethodGet, "/alerts/json?poll=1&since=1h", ntfyToken(ctx, "phone"), "")
		Expect(status).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring(marker), "ntfy's message")
		waitForUnsealed(ctx)
		Expect(admin(ctx, "kv", "get", "-field=value", "secret/smoke/power-cut")).To(Equal(marker), "OpenBao's secret")
	}, SpecTimeout(75*time.Minute))
})
