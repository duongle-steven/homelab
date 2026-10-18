package test

import (
	"fmt"
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

// restartPod is what TestNodeRestart needs from a pod.
type restartPod struct {
	Namespace, Name, Phase, Redirection string
	OptedOut                            bool // labelled istio.io/dataplane-mode=none, or on the host network
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
		pods = append(pods, restartPod{
			Namespace:   p.Metadata.Namespace,
			Name:        p.Metadata.Name,
			Phase:       p.Status.Phase,
			Redirection: p.Metadata.Annotations["ambient.istio.io/redirection"],
			OptedOut:    p.Metadata.Labels["istio.io/dataplane-mode"] == "none" || p.Spec.HostNetwork,
		})
	}
	return pods, meshed, ztunnelReady, true
}

// TestNodeRestart restarts the sandbox's node and waits until the mesh and the apps are back as they were: the
// istio-cni watchdog (system/istio-system) must break the deadlock described in
// docs/superpowers/specs/2026-10-18-mesh-node-restart-design.md. make dev runs it alone, after the other tests.
func TestNodeRestart(t *testing.T) {
	meshSandboxOnly(t)
	hosts := map[string]string{}
	for _, app := range smokeApps("dev") {
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
	recovered := assert.Eventually(t, func() bool {
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
