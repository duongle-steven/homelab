// Package restart decides when a node restart or a power cut has recovered
package restart

import (
	"fmt"
	"time"
)

// Pod is what TestNodeRestart needs from a pod.
type Pod struct {
	Namespace, Name, Phase, Redirection string
	OptedOut                            bool // labelled istio.io/dataplane-mode=none, or on the host network
	// Ready: every container ready; Started: when the earliest running container started (TestNodePowerCut)
	Ready   bool
	Started time.Time
}

// Blockers lists what still keeps the cluster from being recovered after a node restart: ztunnel not Ready,
// pods not running (finished Job pods aside), meshed pods not redirected to ztunnel, and apps that do not answer
// with the code they gave before the restart (0: unreachable).
func Blockers(pods []Pod, meshed map[string]bool, ztunnelReady []bool, want, got map[string]int) []string {
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

// WatchdogJob is a Job of the CronJob istio-system/istio-cni-watchdog.
type WatchdogJob struct {
	Name    string
	Started time.Time
	Failed  bool
}

// FailedWatchdogJobs names the watchdog Jobs that failed after the restart: ipv6 off alone can recover the node, so
// without this TestNodeRestart would stay green with a broken watchdog (image, RBAC, BusyBox date).
func FailedWatchdogJobs(jobs []WatchdogJob, since time.Time) []string {
	var failed []string
	for _, j := range jobs {
		if j.Failed && !j.Started.Before(since) {
			failed = append(failed, j.Name)
		}
	}
	return failed
}

// PowerCutBlockers lists what Blockers cannot see after a power cut, while the API server still reports what it
// knew before: the node not Ready or its last heartbeat from before the cut, and pods (finished ones aside) whose
// containers are not all ready or started before the cut (the cut killed every container on the node).
func PowerCutBlockers(pods []Pod, nodeReady bool, heartbeat, cut time.Time) []string {
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
