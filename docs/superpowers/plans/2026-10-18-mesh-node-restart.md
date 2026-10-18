# Mesh Recovery After a Node Restart Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** After a node restarts, its pods rejoin the ambient mesh without anyone stepping in, and a sandbox test proves it on every `make dev`.

**Architecture:**
- `system/istio-system` keeps `istioOwnedCNIConfig` and drops `ambient.ipv6: false`.
- It gains a CronJob watchdog. When a node's ztunnel has been running but not Ready for more than 180s, the watchdog deletes that node's istio-cni pod, whose agent then rebuilds its snapshot without the dead netns.
- istiod runs 2 spread replicas in production.
- A Go test restarts the k3d node and waits for full recovery. It runs alone at the end of `make dev`.

**Tech Stack:** Istio 1.31.2 Helm charts (base, istiod, cni, ztunnel); a Kubernetes CronJob running a POSIX `sh` script on `alpine/kubectl:1.37.1`; Go tests (terratest helpers, `kubectl`, `docker`).

**Spec:** `docs/superpowers/specs/2026-10-18-mesh-node-restart-design.md`

## Global Constraints

- Use the latest stable release of every chart and image, pinned exactly: Istio charts `1.31.2`; watchdog image `alpine/kubectl:1.37.1@sha256:8c72f6f732ce7ef11cdb1bea25faa4f4a521872a12c89e3f40ba9edb52a4d4f2`.
- Watchdog rule: for each node, if the ztunnel container has been running for more than 180s and is not Ready, and that node's istio-cni container has been running for more than 180s, delete that node's istio-cni pod.
- Watchdog schedule: every minute; `concurrencyPolicy: Forbid`; `startingDeadlineSeconds: 50`; Job `activeDeadlineSeconds: 50`; `ttlSecondsAfterFinished: 300`; history limits 1.
- Watchdog pod: UID 65534, `runAsNonRoot`, read-only root filesystem, every capability dropped, small requests. RBAC is a Role in `istio-system` only: pods `get`, `list`, `delete`.
- istiod in production: `pilot.autoscaleMin: 2` and topology spread on `kubernetes.io/hostname` (`maxSkew: 1`, `ScheduleAnyway`). Sandbox: `autoscaleMin: 1`.
- The test is named `TestNodeRestart`. No test name may contain a word of `make dev`'s main filter (`Smoke|Identity|SSO|Gateway|Forgejo|Mesh|Monitoring|Egress|Secrets|Ntfy|Logs`), because `go test -run` is not anchored.
- The node-restart test waits at most 12 minutes, and `make dev` runs it alone, after the main run.
- Charts and app files hold no IP address or domain. Comments say why, not what; match the surrounding comment style.
- Commits end with the session's attribution lines. Never commit to `dev` or `master`.

## Review Focus

1. **ztunnel crash-looping or waiting** (no `state.running.startedAt`): the watchdog must not act, because it cannot tell how long ztunnel has been up. Pinned by `TestIstioCNIWatchdog/ztunnel_not_running`.
2. **A node without an istio-cni pod** (deleted, or not yet recreated): the watchdog must skip that node and must not fail the run for the other nodes. Pinned by `TestIstioCNIWatchdog/no_istio-cni_pod`.
3. **BusyBox `date` in the image** cannot parse `2026-10-18T03:25:27Z` with `-d` alone. The script must work in the real image, not only with GNU date on the workstation. Pinned by Task 3, Step 6, which runs the script's date helper inside `alpine/kubectl:1.37.1`.
4. **A recovered node with an app still answering 503** while its pod warms up (Grafana took 45s): the test must keep polling until every app returns its recorded code, not fail on the first 503. Pinned by `TestNodeRestartBlockers/app_code_differs`, which reports it as a blocker so the loop retries.
5. **Completed Job pods** (Succeeded/Failed, such as the watchdog's own Jobs or seed Jobs) must not count as stuck. Pinned by `TestNodeRestartBlockers/finished_job_ignored`.

---

### Task 1: Istio 1.31.2, CNI settings and istiod replicas

**Files:**
- Modify: `system/istio-system/Chart.yaml` (4 dependency versions)
- Modify: `system/istio-system/values.yaml` (the `istiod:` and `cni:` blocks)
- Modify: `system/istio-system/values-dev.yaml`

**Interfaces:**
- Consumes: nothing.
- Produces: rendered istio-cni with `ISTIO_OWNED_CNI_CONFIG: "true"` and without `AMBIENT_IPV6: "false"`; an istiod HPA with `minReplicas: 2` in production and `1` in the sandbox.

- [ ] **Step 1: Record the current render, to compare later**

Run: `scripts/render-chart system/istio-system > /tmp/claude-1000/-home-nixos-homelab/44339b87-568a-46fd-9484-aed834c50ef8/scratchpad/istio-before.yaml && grep -nE 'AMBIENT_IPV6|ISTIO_OWNED_CNI_CONFIG|minReplicas' /tmp/claude-1000/-home-nixos-homelab/44339b87-568a-46fd-9484-aed834c50ef8/scratchpad/istio-before.yaml`
Expected: `AMBIENT_IPV6: "false"`, `ISTIO_OWNED_CNI_CONFIG: "true"`, `minReplicas: 1`.

- [ ] **Step 2: Bump the charts**

In `system/istio-system/Chart.yaml`, change all four `version: 1.31.1` to `version: 1.31.2`.

- [ ] **Step 3: Edit the `cni:` and `istiod:` blocks in `values.yaml`**

Replace the `istiod:` block with:

```yaml
istiod:
  <<: *ambient
  pilot:
    # Two replicas on different nodes: a rebooted node's ztunnel gets its config at once instead of waiting for
    # istiod to come back on that node (docs/reference/architecture/networking.md, node restarts)
    autoscaleMin: 2
    topologySpreadConstraints:
      - maxSkew: 1
        topologyKey: kubernetes.io/hostname
        whenUnsatisfiable: ScheduleAnyway
        labelSelector:
          matchLabels:
            app: istiod
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
```

Replace the `cni:` block with:

```yaml
cni:
  <<: *ambient
  # Istio writes its own 02-istio-cni.conflist instead of patching 10-flannel.conflist, which k3s rewrites at every
  # start: pods recreated before the agent patched it again would skip istio-cni and run outside the mesh
  # (istio/istio#55968)
  istioOwnedCNIConfig: true
  resources:
    requests:
      cpu: 10m
      memory: 32Mi
```

- [ ] **Step 4: Keep one istiod in the sandbox**

Append to `system/istio-system/values-dev.yaml`:

```yaml
# One node: a second istiod would only compete for it
istiod:
  pilot:
    autoscaleMin: 1
```

- [ ] **Step 5: Verify both renders**

Run: `for e in "" dev; do scripts/render-chart system/istio-system $e > /tmp/claude-1000/-home-nixos-homelab/44339b87-568a-46fd-9484-aed834c50ef8/scratchpad/istio-$e.yaml || echo FAIL; echo "== ${e:-prod}"; grep -nE 'AMBIENT_IPV6|ISTIO_OWNED_CNI_CONFIG|minReplicas|topologyKey|1\.31\.[12]' /tmp/claude-1000/-home-nixos-homelab/44339b87-568a-46fd-9484-aed834c50ef8/scratchpad/istio-$e.yaml | sort -u -t: -k3 | head; done`
Expected:
- prod: `ISTIO_OWNED_CNI_CONFIG: "true"`, `AMBIENT_IPV6: "true"` (the chart default) or no `AMBIENT_IPV6: "false"`, `minReplicas: 2`, `topologyKey: kubernetes.io/hostname`, images tagged `1.31.2`, none tagged `1.31.1`.
- dev: the same, except `minReplicas: 1`.

- [ ] **Step 6: Run the mesh and env checks**

Run: `scripts/check-mesh-policies > /tmp/claude-1000/-home-nixos-homelab/44339b87-568a-46fd-9484-aed834c50ef8/scratchpad/mesh.log 2>&1; echo mesh=$?; scripts/check-env-config | tail -1`
Expected: `mesh=0`; check-env-config ends with its success line.

- [ ] **Step 7: Commit**

```bash
git add system/istio-system/Chart.yaml system/istio-system/values.yaml system/istio-system/values-dev.yaml
git commit -m "fix(istio): drop ambient IPv6 off, explain the Istio-owned CNI config, Istio 1.31.2, two istiods in production"
```

---

### Task 2: `TestNodeRestart` (RED against a sandbox without the watchdog)

**Files:**
- Create: `test/node_restart_test.go`
- Modify: `Makefile` (target `dev`)

**Interfaces:**
- Consumes: from the existing tests: `k3dNode` (const, `identity_helpers_test.go`), `insecureClient` (`*http.Client`, does not follow redirects, dials the node IP), `routeHost(t, namespace, name string) string`, `getResource(t, namespace string, out any, args ...string) bool`, `smokeApps(env string) []smokeApp`, `meshSandboxOnly(t)`.
- Produces: `type restartPod struct`, `func restartBlockers(pods []restartPod, meshed map[string]bool, ztunnelReady []bool, want, got map[string]int) []string`, and `TestNodeRestart`, which `make dev` runs with `smoke_filter='NodeRestart'`.

- [ ] **Step 1: Write the pure function's unit test first**

Create `test/node_restart_test.go`:

```go
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
```

- [ ] **Step 2: Run it to see it fail**

Run: `cd test && go test -run 'TestNodeRestartBlockers' ./... 2>&1 | tail -5`
Expected: FAIL to compile with `undefined: restartPod` and `undefined: restartBlockers`.

- [ ] **Step 3: Add the type, the function and the cluster test**

Append to `test/node_restart_test.go`:

```go
// restartPod is what TestNodeRestart needs from a pod.
type restartPod struct {
	Namespace, Name, Phase, Redirection string
	OptedOut                            bool // labelled istio.io/dataplane-mode=none
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
func appCodes(t *testing.T, hosts map[string]string) map[string]int {
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
		want = appCodes(t, hosts)
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
		blockers = restartBlockers(pods, meshed, ztunnel, want, appCodes(t, hosts))
		return len(blockers) == 0
	}, 12*time.Minute, 15*time.Second, "the sandbox did not recover from a node restart")
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
```

- [ ] **Step 4: Run the unit test and vet**

Run: `cd test && gofmt -l . ; go vet ./... && go test -run 'TestNodeRestartBlockers' -v ./... 2>&1 | grep -E '^(=== RUN|--- |ok|FAIL|PASS)' | tail -14`
Expected: `gofmt` prints nothing; every `TestNodeRestartBlockers/...` subtest PASSes; `ok`.

- [ ] **Step 5: Run the test alone at the end of `make dev`**

In `Makefile`, the `dev` target becomes:

```make
dev: sandbox-push
	cd infra/sandbox && $(terragrunt_apply)
	$(MAKE) env=sandbox ENV=dev INSECURE_SKIP_VERIFY=1 smoke_filter='Smoke|Identity|SSO|Gateway|Forgejo|Mesh|Monitoring|Egress|Secrets|Ntfy|Logs' sandbox-refresh smoke-test
	# Restarts the node, so it runs alone, after the others
	$(MAKE) env=sandbox ENV=dev INSECURE_SKIP_VERIFY=1 smoke_filter='NodeRestart' smoke-test
```

- [ ] **Step 6: Commit**

```bash
git add test/node_restart_test.go Makefile
git commit -m "test: the sandbox recovers from a node restart (make dev runs it alone, last)"
```

- [ ] **Step 7: RED on the sandbox, without the watchdog**

Push this commit to the sandbox and wait for istio-system to sync (Istio 1.31.2 rolls istio-cni and ztunnel):

Run: `make sandbox-push sandbox-refresh && until [ "$(kubectl -n argocd get app istio-system -o jsonpath='{.status.sync.revision}')" = "$(git rev-parse HEAD)" ] && [ "$(kubectl -n argocd get app istio-system -o jsonpath='{.status.health.status}')" = Healthy ]; do sleep 15; done; kubectl -n istio-system get cm istio-cni-config -o jsonpath='{.data.AMBIENT_IPV6} {.data.ISTIO_OWNED_CNI_CONFIG}{"\n"}'; kubectl -n istio-system get ds -o wide | grep -E 'NAME|1\.31'`
Expected: `true true`; both DaemonSets on `1.31.2`, rolled out.

Then run: `make env=sandbox ENV=dev INSECURE_SKIP_VERIFY=1 smoke_filter='NodeRestart' smoke-test 2>&1 | tee .superpowers/sdd/2026-10-18-mesh-node-restart/red.log | tail -25`
Expected: FAIL after 12 minutes on `the sandbox did not recover from a node restart`, with `ztunnel 0 not Ready` and pods `Pending` among the blockers.

- [ ] **Step 8: Unstick the sandbox by hand (what the watchdog will do)**

Run: `kubectl -n istio-system delete pod -l k8s-app=istio-cni-node && until [ "$(kubectl -n istio-system get pods -l app=ztunnel -o jsonpath='{.items[0].status.containerStatuses[0].ready}')" = true ] && [ "$(kubectl get pods -A --no-headers | grep -vcE 'Running|Completed')" = 0 ]; do sleep 15; done; echo recovered`
Expected: `recovered` within about 5 minutes.

---

### Task 3: The istio-cni watchdog

**Files:**
- Create: `system/istio-system/files/cni-watchdog.sh`
- Create: `system/istio-system/templates/cni-watchdog.yaml`
- Modify: `system/istio-system/values.yaml` (new `cniWatchdog:` block)
- Create: `test/cni_watchdog_test.go`

**Interfaces:**
- Consumes: Task 2's `TestNodeRestart` (for GREEN).
- Produces: the script `cni-watchdog.sh`, which reads `NOT_READY_SECONDS` (default 180) and calls `kubectl` and `date` only; values `cniWatchdog.image` and `cniWatchdog.notReadySeconds`; the offline test `TestIstioCNIWatchdog`.

- [ ] **Step 1: Write the script's test, with a fake `kubectl`**

Create `test/cni_watchdog_test.go`:

```go
package test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeKubectl answers the watchdog's two queries from files and records deletions.
const fakeKubectl = `#!/bin/sh
case "$*" in
  *"delete pod"*) echo "$*" >> "$FIXTURES/deleted" ;;
  *app=ztunnel*) cat "$FIXTURES/ztunnel" ;;
  *istio-cni-node*) node=$(echo "$*" | sed -n 's/.*spec.nodeName=\([^ ]*\).*/\1/p'); cat "$FIXTURES/cni-$node" 2>/dev/null ;;
esac
`

// runWatchdog runs system/istio-system/files/cni-watchdog.sh against ztunnel lines ("node ready startedAt") and
// istio-cni lines per node ("pod startedAt"), and returns the kubectl deletions it made.
func runWatchdog(t *testing.T, ztunnel string, cni map[string]string) string {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kubectl"), []byte(fakeKubectl), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ztunnel"), []byte(ztunnel), 0o644))
	for node, lines := range cni {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "cni-"+node), []byte(lines), 0o644))
	}
	cmd := exec.Command("sh", "../system/istio-system/files/cni-watchdog.sh")
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "FIXTURES="+dir, "NOT_READY_SECONDS=180")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "watchdog failed:\n%s", out)
	deleted, _ := os.ReadFile(filepath.Join(dir, "deleted"))
	return strings.TrimSpace(string(deleted))
}

func ago(seconds int) string {
	return time.Now().UTC().Add(-time.Duration(seconds) * time.Second).Format(time.RFC3339)
}

func TestIstioCNIWatchdog(t *testing.T) {
	for _, c := range []struct {
		name    string
		ztunnel string
		cni     map[string]string
		deleted string
	}{
		{"stuck node", fmt.Sprintf("node-a false %s\n", ago(300)), map[string]string{"node-a": "istio-cni-a " + ago(300) + "\n"}, "-n istio-system delete pod istio-cni-a --wait=false"},
		{"ztunnel just started", fmt.Sprintf("node-a false %s\n", ago(60)), map[string]string{"node-a": "istio-cni-a " + ago(300) + "\n"}, ""},
		{"ztunnel ready", fmt.Sprintf("node-a true %s\n", ago(300)), map[string]string{"node-a": "istio-cni-a " + ago(300) + "\n"}, ""},
		{"istio-cni just restarted", fmt.Sprintf("node-a false %s\n", ago(300)), map[string]string{"node-a": "istio-cni-a " + ago(60) + "\n"}, ""},
		{"ztunnel not running", "node-a false \n", map[string]string{"node-a": "istio-cni-a " + ago(300) + "\n"}, ""},
		{"no istio-cni pod", fmt.Sprintf("node-a false %s\nnode-b false %s\n", ago(300), ago(300)), map[string]string{"node-b": "istio-cni-b " + ago(300) + "\n"}, "-n istio-system delete pod istio-cni-b --wait=false"},
		{"only the stuck node", fmt.Sprintf("node-a true %s\nnode-b false %s\n", ago(300), ago(300)), map[string]string{"node-a": "istio-cni-a " + ago(300) + "\n", "node-b": "istio-cni-b " + ago(300) + "\n"}, "-n istio-system delete pod istio-cni-b --wait=false"},
	} {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.deleted, runWatchdog(t, c.ztunnel, c.cni))
		})
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `cd test && go test -run 'TestIstioCNIWatchdog' ./... 2>&1 | tail -6`
Expected: FAIL, with `watchdog failed` and `sh: ../system/istio-system/files/cni-watchdog.sh: No such file or directory` (or `can't open`).

- [ ] **Step 3: Write the script**

Create `system/istio-system/files/cni-watchdog.sh`:

```sh
#!/bin/sh
# Restarts a node's istio-cni when that node's ztunnel has been running but not Ready for NOT_READY_SECONDS: after a
# node restart, istio-cni's snapshot can hold network namespaces of pod sandboxes that were torn down, and ztunnel
# then retries them forever; a new istio-cni builds its snapshot again (docs/reference/architecture/networking.md).
# ztunnel without a start time (waiting, crash-looping) is left alone: its age is unknown.
set -eu
limit=${NOT_READY_SECONDS:-180}
now=$(date -u +%s)

# GNU date parses RFC 3339 with -d; BusyBox (the image) needs the format
epoch() {
	date -u -d "$1" +%s 2>/dev/null || date -u -D '%Y-%m-%dT%H:%M:%SZ' -d "$1" +%s
}

kubectl -n istio-system get pods -l app=ztunnel -o jsonpath='{range .items[*]}{.spec.nodeName} {.status.containerStatuses[0].ready} {.status.containerStatuses[0].state.running.startedAt}{"\n"}{end}' |
	while read -r node ready started; do
		[ "$ready" = false ] && [ -n "${started:-}" ] || continue
		[ $((now - $(epoch "$started"))) -gt "$limit" ] || continue
		cni=$(kubectl -n istio-system get pods -l k8s-app=istio-cni-node --field-selector "spec.nodeName=$node" -o jsonpath='{range .items[*]}{.metadata.name} {.status.containerStatuses[0].state.running.startedAt}{"\n"}{end}')
		set -- $cni
		if [ $# -ne 2 ]; then
			echo "$node: ztunnel not Ready, no running istio-cni pod"
			continue
		fi
		if [ $((now - $(epoch "$2"))) -le "$limit" ]; then
			echo "$node: ztunnel not Ready, istio-cni $1 started less than ${limit}s ago"
			continue
		fi
		echo "$node: ztunnel not Ready for more than ${limit}s, restarting istio-cni $1"
		kubectl -n istio-system delete pod "$1" --wait=false
	done
```

- [ ] **Step 4: Run the test to see it pass**

Run: `cd test && go test -run 'TestIstioCNIWatchdog' -v ./... 2>&1 | grep -E '^(\s+--- |--- |ok|FAIL)' | tail -10`
Expected: all 7 subtests PASS; `ok`.

- [ ] **Step 5: Add the values and the template**

Append to `system/istio-system/values.yaml`:

```yaml
# Restarts a node's istio-cni when its ztunnel stays not Ready (files/cni-watchdog.sh): breaks the deadlock that
# follows a node restart
cniWatchdog:
  # The official kubectl image has no shell
  image: alpine/kubectl:1.37.1@sha256:8c72f6f732ce7ef11cdb1bea25faa4f4a521872a12c89e3f40ba9edb52a4d4f2
  notReadySeconds: 180
```

Create `system/istio-system/templates/cni-watchdog.yaml`:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: istio-cni-watchdog
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: istio-cni-watchdog
rules:
  - apiGroups: [""]
    resources: [pods]
    verbs: [get, list, delete]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: istio-cni-watchdog
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: istio-cni-watchdog
subjects:
  - kind: ServiceAccount
    name: istio-cni-watchdog
    namespace: {{ .Release.Namespace }}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: istio-cni-watchdog
data:
  cni-watchdog.sh: |
{{ .Files.Get "files/cni-watchdog.sh" | indent 4 }}
---
apiVersion: batch/v1
kind: CronJob
metadata:
  name: istio-cni-watchdog
spec:
  schedule: "* * * * *"
  concurrencyPolicy: Forbid
  startingDeadlineSeconds: 50
  successfulJobsHistoryLimit: 1
  failedJobsHistoryLimit: 1
  jobTemplate:
    spec:
      activeDeadlineSeconds: 50
      backoffLimit: 0
      ttlSecondsAfterFinished: 300
      template:
        spec:
          serviceAccountName: istio-cni-watchdog
          restartPolicy: Never
          securityContext:
            runAsNonRoot: true
            runAsUser: 65534
            runAsGroup: 65534
            seccompProfile:
              type: RuntimeDefault
          containers:
            - name: watchdog
              image: {{ .Values.cniWatchdog.image }}
              command: [sh, /scripts/cni-watchdog.sh]
              env:
                - name: NOT_READY_SECONDS
                  value: {{ .Values.cniWatchdog.notReadySeconds | quote }}
              securityContext:
                allowPrivilegeEscalation: false
                readOnlyRootFilesystem: true
                capabilities:
                  drop: [ALL]
              resources:
                requests:
                  cpu: 10m
                  memory: 32Mi
              volumeMounts:
                - name: scripts
                  mountPath: /scripts
          volumes:
            - name: scripts
              configMap:
                name: istio-cni-watchdog
```

- [ ] **Step 6: Check the date helper in the real image**

Run: `docker run --rm --user 65534 --read-only --entrypoint sh alpine/kubectl:1.37.1@sha256:8c72f6f732ce7ef11cdb1bea25faa4f4a521872a12c89e3f40ba9edb52a4d4f2 -c 'epoch() { date -u -d "$1" +%s 2>/dev/null || date -u -D "%Y-%m-%dT%H:%M:%SZ" -d "$1" +%s; }; epoch 2026-10-18T03:25:27Z'`
Expected: `1792293927`.

Then run the whole script inside the image against the sandbox, read-only, as a dry check of the queries: `docker run --rm --network host -v "$PWD/infra/sandbox/kubeconfig.yaml:/kube:ro" -e KUBECONFIG=/kube -v "$PWD/system/istio-system/files:/scripts:ro" --user 65534 --read-only --entrypoint sh alpine/kubectl:1.37.1@sha256:8c72f6f732ce7ef11cdb1bea25faa4f4a521872a12c89e3f40ba9edb52a4d4f2 -c 'sh -x /scripts/cni-watchdog.sh 2>&1 | grep -E "^\+ (kubectl|\[)" | head'`
Expected: the ztunnel query runs and, on a healthy sandbox, no `delete` appears.

- [ ] **Step 7: Render both environments and run the checks**

Run: `for e in "" dev; do scripts/render-chart system/istio-system $e > /tmp/claude-1000/-home-nixos-homelab/44339b87-568a-46fd-9484-aed834c50ef8/scratchpad/istio-$e.yaml || echo FAIL; python3 -c "import yaml,sys; d=[x for x in yaml.safe_load_all(open(sys.argv[1])) if x and x['metadata'].get('name')=='istio-cni-watchdog']; print(sorted(x['kind'] for x in d))" /tmp/claude-1000/-home-nixos-homelab/44339b87-568a-46fd-9484-aed834c50ef8/scratchpad/istio-$e.yaml; done; scripts/check-mesh-policies > /tmp/claude-1000/-home-nixos-homelab/44339b87-568a-46fd-9484-aed834c50ef8/scratchpad/mesh.log 2>&1; echo mesh=$?; scripts/check-env-config | tail -1`
Expected: twice `['ConfigMap', 'CronJob', 'Role', 'RoleBinding', 'ServiceAccount']`; `mesh=0`; check-env-config's success line.

- [ ] **Step 8: Commit**

```bash
git add system/istio-system/files/cni-watchdog.sh system/istio-system/templates/cni-watchdog.yaml system/istio-system/values.yaml test/cni_watchdog_test.go
git commit -m "fix(istio): a watchdog restarts a node's istio-cni when its ztunnel stays not Ready"
```

- [ ] **Step 9: GREEN on the sandbox**

Run: `make sandbox-push sandbox-refresh && until kubectl -n istio-system get cronjob istio-cni-watchdog >/dev/null 2>&1 && [ "$(kubectl -n argocd get app istio-system -o jsonpath='{.status.sync.revision}')" = "$(git rev-parse HEAD)" ]; do sleep 15; done; until kubectl -n istio-system get jobs -o name | grep -q istio-cni-watchdog; do sleep 10; done; kubectl -n istio-system logs job/$(kubectl -n istio-system get jobs -o jsonpath='{.items[-1:].metadata.name}') ; kubectl -n istio-system get jobs`
Expected: the CronJob exists; a Job ran and completed, with empty output (healthy sandbox).

Then run: `make env=sandbox ENV=dev INSECURE_SKIP_VERIFY=1 smoke_filter='NodeRestart' smoke-test 2>&1 | tee .superpowers/sdd/2026-10-18-mesh-node-restart/green.log | tail -8`
Expected: PASS; the log line `recovered 6m..s after the restart` (between about 4 and 9 minutes); afterwards `kubectl -n istio-system get events --field-selector involvedObject.kind=Pod | grep -i istio-cni` or a watchdog Job log shows the restart.

---

### Task 4: Documentation

**Files:**
- Modify: `CLAUDE.md` (Rules: the offline test list; Sandbox: watchdog and `make dev`; the production list)
- Modify: `docs/reference/architecture/networking.md` (new subsection under `## Service mesh`)
- Modify: `docs/superpowers/deferred-minors.md` (only if the final review defers minors)

**Interfaces:**
- Consumes: the names from Tasks 1–3 (`istio-cni-watchdog`, `TestNodeRestart`, `TestNodeRestartBlockers`, `TestIstioCNIWatchdog`).
- Produces: documentation only.

- [ ] **Step 1: CLAUDE.md, the offline tests**

In the Rules bullet on basic checks, replace `go test -run 'TestSmokeAppsForEnv|TestIdentity(TOTPWaitsForNextStep|PKCE)|TestGatewayRouteReady|TestGatewayNodeAddressFromInspect' ./...` with `go test -run 'TestSmokeAppsForEnv|TestIdentity(TOTPWaitsForNextStep|PKCE)|TestGatewayRouteReady|TestGatewayNodeAddressFromInspect|TestNodeRestartBlockers|TestIstioCNIWatchdog' ./...`.

- [ ] **Step 2: CLAUDE.md, Sandbox**

Change the `make dev` filter sentence in the Kanidm bullet from `(`make dev` runs `Smoke|…|Logs`)` to `(`make dev` runs `Smoke|…|Logs`, then `TestNodeRestart` alone: it restarts the node)`, and add after the Istio mesh bullet:

```markdown
- A node restart can leave the mesh deadlocked: pods recreated before the node's ztunnel connects fail istio-cni's 5-second `cmdAdd`, containerd tears their sandboxes down, istio-cni's snapshot keeps the dead network namespaces and ztunnel retries them forever (`docs/superpowers/specs/2026-10-18-mesh-node-restart-design.md`). The CronJob `istio-system/istio-cni-watchdog` (`files/cni-watchdog.sh`) deletes a node's istio-cni pod when that node's ztunnel has run 180 s without becoming Ready; `TestNodeRestart` proves the sandbox recovers (about 4 to 9 minutes). `cni.istioOwnedCNIConfig` stays on: k3s rewrites `10-flannel.conflist` at every start.
```

- [ ] **Step 3: CLAUDE.md, the production list**

Add to "Before the first production deploy":

```markdown
- Node-restart recovery (the istio-cni watchdog, `istioOwnedCNIConfig`) only ran on the one-node sandbox, where every node restart deadlocks the mesh without the watchdog; istiod's two replicas spread over nodes were only rendered. Give k3s's CoreDNS two replicas on different nodes in step 12 (`kube-system` is outside Argo CD), so a rebooted node's ztunnel finds DNS and istiod at once. A power loss that restarts every node is unmeasured on real hardware.
```

- [ ] **Step 4: The networking page**

Append to `## Service mesh` in `docs/reference/architecture/networking.md`, before `## Egress`:

```markdown
### Node restarts

When a node restarts, the kubelet recreates its pods while the node's ztunnel may still be waiting for istiod. istio-cni cannot hand those pods to ztunnel within its 5-second limit, containerd tears their sandboxes down, and istio-cni's snapshot keeps their dead network namespaces, which ztunnel then retries forever: the node's meshed pods stay `Pending` and ztunnel stays not Ready. The CronJob `istio-system/istio-cni-watchdog` deletes a node's istio-cni pod when that node's ztunnel has run for 3 minutes without becoming Ready; the new istio-cni builds its snapshot again and the pods start. Look at `kubectl -n istio-system get pods` and the watchdog Jobs' logs. istio-cni writes its own CNI config file (`istioOwnedCNIConfig`), because k3s rewrites `10-flannel.conflist` at every start.
```

- [ ] **Step 5: Build the docs**

Run: `mkdocs build --strict 2>&1 | tail -3`
Expected: `INFO - Documentation built in ...` with no warning.

- [ ] **Step 6: Commit**

```bash
git add CLAUDE.md docs/reference/architecture/networking.md
git commit -m "docs: node restarts, the istio-cni watchdog and its production checklist"
```

---

### Task 5: Full checks and the sandbox from scratch

**Files:** none new.

**Interfaces:**
- Consumes: everything above.
- Produces: a green `make dev` from a fresh sandbox, ready for the final review and the PR.

- [ ] **Step 1: The basic checks**

Run: `for d in system/istio-system; do scripts/render-chart $d >/dev/null && scripts/render-chart $d dev >/dev/null && echo "render $d ok"; done; (cd test && gofmt -l . && go vet ./... && go test -run 'TestSmokeAppsForEnv|TestIdentity(TOTPWaitsForNextStep|PKCE)|TestGatewayRouteReady|TestGatewayNodeAddressFromInspect|TestNodeRestartBlockers|TestIstioCNIWatchdog' ./... | tail -1); mkdocs build --strict 2>&1 | tail -1; scripts/check-mesh-policies >/dev/null 2>&1; echo mesh=$?; scripts/check-env-config | tail -1; scripts/check-secret-stores | tail -1; make -C infra check 2>&1 | tail -1`
Expected: `render system/istio-system ok`; `ok`; docs built; `mesh=0`; the three checks end with their success lines.

- [ ] **Step 2: A fresh sandbox, the full suite**

Run: `make dev-clean > .superpowers/sdd/2026-10-18-mesh-node-restart/clean.log 2>&1; echo clean=$?; make dev > .superpowers/sdd/2026-10-18-mesh-node-restart/dev.log 2>&1; echo dev=$?; grep -E '^DONE|recovered|FAIL' .superpowers/sdd/2026-10-18-mesh-node-restart/dev.log | tail -6`
Expected: `clean=0`; `dev=0`; the main run's `DONE N tests` without failures, then `TestNodeRestart` passing with `recovered …`.
