# Istio Ambient Mesh Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every namespace Argo CD manages joins Istio's ambient mesh with strict mTLS; `istio-system`, `rook-ceph`, `kube-system` and the webhook pods stay out; `make dev` passes all tests plus new mesh tests from scratch.

**Architecture:** `system/istio-system` gains Istio's `cni` and `ztunnel` charts, every Istio subchart runs `profile: ambient` with `global.platform: k3s`, and a mesh-wide `PeerAuthentication` enforces `STRICT`. Argo CD's shared `managedNamespaceMetadata` labels every namespace `istio.io/dataplane-mode: ambient`; the `root` ApplicationSet moves to go templates so a `templatePatch` can set `none` for `istio-system` and `rook-ceph`. Webhook pods opt out through their charts' pod labels, guarded by a new render check `scripts/check-mesh-exclusions`.

**Tech Stack:** Istio 1.31.1 (`base`, `istiod`, `cni`, `ztunnel`), Argo CD ApplicationSets (argocd-apps 2.0.6), Helm wrapper charts, Python 3 + PyYAML (render check), Go tests (terratest).

**Spec:** `docs/superpowers/specs/2026-10-15-istio-ambient-design.md`

## Global Constraints

- Istio charts `base`, `istiod`, `cni`, `ztunnel` 1.31.1 from `https://blob.istio.io/istio-release/charts`.
- `global.platform: k3s` in **both** production and the sandbox (planning spike, below). No `values-dev.yaml` override for the platform.
- Enrollment label `istio.io/dataplane-mode: ambient`; opt-out `istio.io/dataplane-mode: none`. Namespaces out of the mesh: `istio-system`, `rook-ceph` (label `none`), `kube-system` (not managed by Argo CD).
- Webhook pods with the `none` label: cert-manager webhook, trust-manager, external-secrets webhook, prometheus-operator, CloudNativePG operator.
- `PeerAuthentication` `default` in `istio-system`, `mtls.mode: STRICT`.
- Test image `curlimages/curl:8.23.0` (latest release).
- Branch `feat/istio-ambient` from `dev`, PR into `dev`; ask before merging.
- Planning spike (2026-10-15, a throwaway k3d cluster with k3s v1.37.1, Istio 1.31.1 installed with Helm, deleted afterwards):
  - `global.platform: k3d` broke pod creation: its `cniBinDir` `/bin` is wrong for current k3s images, kubelet looks in `/var/lib/rancher/k3s/data/cni` ("failed to find plugin istio-cni"), and the chained CNI config then blocked every new pod. `global.platform: k3s` worked on k3d.
  - With `k3s`: pods in an `ambient` namespace got `ambient.istio.io/redirection: enabled`. Under `STRICT`, a plain pod in `default` was refused and ztunnel logged `connection closed due to policy rejection: explicitly denied by: istio-system/istio_converted_static_strict`. A pod labelled `istio.io/dataplane-mode=ambient` in `default` got `200`, and ztunnel logged `src.identity="spiffe://cluster.local/ns/default/sa/default"` and `dst.identity="spiffe://cluster.local/ns/web/sa/default"`. A Gateway in `istio-system` (outside the mesh) reached the meshed app (`200`).
  - A scratch render of the four charts with the values in Task 1 gave the k3s CNI paths, `PILOT_ENABLE_AMBIENT=true`, and the requested resources.

## Review Focus

1. A webhook or aggregated API that the render check cannot see (installed by an operator at runtime, or a chart rendered differently in the cluster) would break API calls into a meshed namespace. Expected: `TestMeshEnrolled` fails on an unexpected opted-out or unredirected pod, and Argo CD sync errors show up in `make dev`. Pinned in Task 4 (the opted-out pods must be exactly the allowed webhook set).
2. Pods that existed before the namespace got its label are not redirected until they restart. On the fresh sandbox, enrollment races the first sync. Expected: `TestMeshEnrolled` fails on any unredirected pod; the fix (if needed) is a restart in `make dev`, not a weaker test. Pinned in Task 4.
3. Host-network pods (node-exporter) and completed Job pods cannot be redirected. Expected: `TestMeshEnrolled` skips exactly these and nothing else. Pinned in Task 4.
4. The `root` ApplicationSet's switch to go templates must keep every generated Application identical (names, namespaces, paths). Expected: a render check compares the template fields. Pinned in Task 2.
5. Prometheus in the mesh still scrapes targets outside it (k3s endpoints in production, node-exporter). Expected: unchanged scraping. Checked after the merge by the existing smoke tests and Task 6's Prometheus query.

---

### Task 1: Ambient in `system/istio-system`

**Files:**
- Modify: `system/istio-system/Chart.yaml`, `system/istio-system/values.yaml`
- Create: `system/istio-system/templates/peer-authentication.yaml`

**Interfaces:**
- Produces: DaemonSets `istio-cni-node` and `ztunnel` in `istio-system`; `PeerAuthentication` `istio-system/default` (STRICT); istiod with `PILOT_ENABLE_AMBIENT=true`.

- [ ] **Step 1: Write the check (fails before the change)**

Create `.superpowers/sdd/2026-10-15-istio-ambient/check-task1.sh`:

```sh
#!/bin/sh
# istio-system renders ambient with the k3s CNI paths in both environments, plus strict mTLS
set -e
for env in prod dev; do
  out=$(scripts/render-chart system/istio-system $env 2>/dev/null)
  test "$(echo "$out" | yq ea '[select(.kind == "DaemonSet") | .metadata.name] | sort | join(",")')" = "istio-cni-node,ztunnel"
  echo "$out" | yq 'select(.kind == "DaemonSet" and .metadata.name == "istio-cni-node") | .spec.template.spec.volumes[] | select(.hostPath) | .hostPath.path' | grep -qx /var/lib/rancher/k3s/data/cni
  test "$(echo "$out" | yq 'select(.kind == "Deployment" and .metadata.name == "istiod") | .spec.template.spec.containers[0].env[] | select(.name == "PILOT_ENABLE_AMBIENT") | .value')" = true
  test "$(echo "$out" | yq 'select(.kind == "PeerAuthentication") | .metadata.name + " " + .spec.mtls.mode')" = "default STRICT"
  echo "$env: ambient render ok"
done
echo "istio-system ambient checks: pass"
```

Run: `chmod +x .superpowers/sdd/2026-10-15-istio-ambient/check-task1.sh && .superpowers/sdd/2026-10-15-istio-ambient/check-task1.sh`
Expected: FAIL (no DaemonSets yet).

- [ ] **Step 2: Chart and values**

Append to the `dependencies` list in `system/istio-system/Chart.yaml`:

```yaml
  - name: cni
    version: 1.31.1
    repository: https://blob.istio.io/istio-release/charts
  - name: ztunnel
    version: 1.31.1
    repository: https://blob.istio.io/istio-release/charts
```

In `system/istio-system/values.yaml`, replace the `istiod:` block with:

```yaml
# Ambient mesh (no sidecars): istio-cni redirects pods of labelled namespaces to the node's ztunnel.
# The k3s profile also fits the k3d sandbox (its CNI plugins live in /var/lib/rancher/k3s/data/cni; Istio's k3d
# profile still points at /bin)
base: &ambient
  profile: ambient
  global:
    platform: k3s
istiod:
  <<: *ambient
  pilot:
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
cni:
  <<: *ambient
  resources:
    requests:
      cpu: 10m
      memory: 32Mi
ztunnel:
  <<: *ambient
  resources:
    requests:
      cpu: 10m
      memory: 64Mi
```

- [ ] **Step 3: Strict mTLS**

`system/istio-system/templates/peer-authentication.yaml`:

```yaml
# Mesh-wide (the root namespace): meshed pods accept only mTLS from other mesh workloads
apiVersion: security.istio.io/v1
kind: PeerAuthentication
metadata:
  name: default
spec:
  mtls:
    mode: STRICT
```

- [ ] **Step 4: Run the check**

Run: `.superpowers/sdd/2026-10-15-istio-ambient/check-task1.sh`
Expected: `prod: ambient render ok`, `dev: ambient render ok`, `istio-system ambient checks: pass`.

- [ ] **Step 5: Commit**

```bash
git add system/istio-system
git commit -m "feat(istio-system): ambient mesh (istio-cni, ztunnel) with strict mTLS"
```

### Task 2: Namespaces join the mesh (`system/argocd`)

**Files:**
- Modify: `system/argocd/values.yaml` (ApplicationSet `root`, shared `managedNamespaceMetadata`)

**Interfaces:**
- Consumes: nothing.
- Produces: every Argo CD-managed namespace labelled `istio.io/dataplane-mode: ambient`; `istio-system` and `rook-ceph` labelled `none`.

- [ ] **Step 1: Write the check (fails before the change)**

Create `.superpowers/sdd/2026-10-15-istio-ambient/check-task2.sh`:

```sh
#!/bin/sh
# root ApplicationSet: go templates with the same generated fields, ambient label, none for istio-system/rook-ceph
set -e
for env in prod dev; do
  out=$(scripts/render-chart system/argocd $env 2>/dev/null)
  root=$(echo "$out" | yq 'select(.kind == "ApplicationSet" and .metadata.name == "root")')
  test "$(echo "$root" | yq '.spec.goTemplate')" = true
  test "$(echo "$root" | yq '.spec.template.metadata.name + "|" + .spec.template.spec.destination.namespace + "|" + .spec.template.spec.source.path')" = "{{ .path.basename }}|{{ .path.basename }}|{{ .path.path }}"
  test "$(echo "$root" | yq '.spec.template.spec.syncPolicy.managedNamespaceMetadata.labels["istio.io/dataplane-mode"]')" = ambient
  echo "$root" | yq '.spec.templatePatch' | grep -q 'list "istio-system" "rook-ceph"'
  echo "$root" | yq '.spec.templatePatch' | grep -q 'istio.io/dataplane-mode: none'
  apps=$(echo "$out" | yq 'select(.kind == "ApplicationSet" and .metadata.name == "apps")')
  test "$(echo "$apps" | yq '.spec.template.spec.syncPolicy.managedNamespaceMetadata.labels["istio.io/dataplane-mode"]')" = ambient
  echo "$env: enrollment render ok"
done
echo "enrollment checks: pass"
```

Run: `chmod +x .superpowers/sdd/2026-10-15-istio-ambient/check-task2.sh && .superpowers/sdd/2026-10-15-istio-ambient/check-task2.sh`
Expected: FAIL (`goTemplate` is null).

- [ ] **Step 2: `root` on go templates, label and patch**

In `system/argocd/values.yaml`, ApplicationSet `root`:

- after `namespace: argocd` add:

  ```yaml
      goTemplate: true
      goTemplateOptions:
        - missingkey=error
  ```

- in `template`: `name: '{{path.basename}}'` → `name: '{{ .path.basename }}'`, `namespace: '{{path.basename}}'` → `namespace: '{{ .path.basename }}'`, `path: '{{path}}'` → `path: '{{ .path.path }}'`.
- in the shared `managedNamespaceMetadata` (under `syncPolicy: &syncPolicy`), before `annotations:` add:

  ```yaml
              labels:
                # Every namespace joins Istio's ambient mesh (system/istio-system); exceptions: templatePatch below
                istio.io/dataplane-mode: ambient
  ```

- after the `template:` block of `root` (same indentation as `template:`) add:

  ```yaml
      # The mesh itself and Ceph (host-network pods, its own encryption) stay out of the mesh
      templatePatch: |
        {{- if has .path.basename (list "istio-system" "rook-ceph") }}
        spec:
          syncPolicy:
            managedNamespaceMetadata:
              labels:
                istio.io/dataplane-mode: none
        {{- end }}
  ```

- [ ] **Step 3: Run the check**

Run: `.superpowers/sdd/2026-10-15-istio-ambient/check-task2.sh`
Expected: `prod: enrollment render ok`, `dev: enrollment render ok`, `enrollment checks: pass`.

- [ ] **Step 4: Commit**

```bash
git add system/argocd
git commit -m "feat(argocd): namespaces join the ambient mesh; istio-system and rook-ceph stay out"
```

### Task 3: Webhook pods opt out, guarded by `scripts/check-mesh-exclusions`

**Files:**
- Create: `scripts/check-mesh-exclusions`
- Modify: `system/cert-manager/values.yaml`, `system/monitoring-system/values.yaml`, `platform/cloudnative-pg/values.yaml`
- Create: `platform/external-secrets/values.yaml`

**Interfaces:**
- Produces: `scripts/check-mesh-exclusions` (exit 0 when every webhook/API service target is out of the mesh; prints one line per target).

- [ ] **Step 1: Write the check script (the test)**

`scripts/check-mesh-exclusions`:

```python
#!/usr/bin/env python3
"""Every webhook and aggregated API the API server calls must be outside Istio's ambient mesh.

The API server is not in the mesh, so under STRICT mTLS it cannot reach meshed pods. Renders every component
the way Argo CD does (production and sandbox) and checks that each webhook Service selects only pods labelled
istio.io/dataplane-mode=none, or lives in a namespace that stays out of the mesh.
"""
import glob
import os
import subprocess
import sys

import yaml

OUTSIDE = {"istio-system", "rook-ceph", "kube-system"}
WORKLOADS = {"Deployment", "DaemonSet", "StatefulSet"}


def render(target, env):
    out = subprocess.run(["scripts/render-chart", target, env], capture_output=True, text=True)
    if out.returncode != 0:
        sys.exit(f"render failed: {target} {env}\n{out.stderr}")
    return [d for d in yaml.safe_load_all(out.stdout) if d]


def targets():
    for d in sorted(glob.glob("system/*/") + glob.glob("platform/*/")):
        if os.path.exists(os.path.join(d, "Chart.yaml")):
            yield d.rstrip("/")
    yield "apps/matrix"
    yield from sorted(glob.glob("apps/*.yaml"))


def webhook_services(doc):
    kind = doc.get("kind")
    if kind in ("ValidatingWebhookConfiguration", "MutatingWebhookConfiguration"):
        for w in doc.get("webhooks") or []:
            if (w.get("clientConfig") or {}).get("service"):
                yield w["clientConfig"]["service"]
    elif kind == "APIService" and (doc.get("spec") or {}).get("service"):
        yield doc["spec"]["service"]
    elif kind == "CustomResourceDefinition":
        hook = ((doc.get("spec") or {}).get("conversion") or {}).get("webhook") or {}
        if (hook.get("clientConfig") or {}).get("service"):
            yield hook["clientConfig"]["service"]


def main():
    failures = []
    for target in targets():
        namespace = os.path.basename(target).removesuffix(".yaml")
        for env in ("prod", "dev"):
            docs = render(target, env)
            ns_of = lambda d: (d.get("metadata") or {}).get("namespace") or namespace
            for svc_ref in (s for d in docs for s in webhook_services(d)):
                svc_ns, svc_name = svc_ref.get("namespace") or namespace, svc_ref["name"]
                where = f"{target} ({env}): {svc_ns}/{svc_name}"
                if svc_ns in OUTSIDE:
                    print(f"ok   {where}: namespace outside the mesh")
                    continue
                service = next((d for d in docs if d.get("kind") == "Service" and d["metadata"]["name"] == svc_name and ns_of(d) == svc_ns), None)
                if service is None:
                    failures.append(f"{where}: Service not rendered by this component")
                    continue
                selector = (service.get("spec") or {}).get("selector") or {}
                pods = [d for d in docs if d.get("kind") in WORKLOADS and ns_of(d) == svc_ns
                        and selector.items() <= (d["spec"]["template"]["metadata"].get("labels") or {}).items()]
                if not pods:
                    failures.append(f"{where}: no workload matches the Service selector {selector}")
                for pod in pods:
                    label = (pod["spec"]["template"]["metadata"].get("labels") or {}).get("istio.io/dataplane-mode")
                    if label == "none":
                        print(f"ok   {where}: {pod['kind']} {pod['metadata']['name']} opts out")
                    else:
                        failures.append(f"{where}: {pod['kind']} {pod['metadata']['name']} lacks istio.io/dataplane-mode: none")
    for f in failures:
        print(f"FAIL {f}")
    sys.exit(1 if failures else 0)


if __name__ == "__main__":
    main()
```

Run: `chmod +x scripts/check-mesh-exclusions && scripts/check-mesh-exclusions | tail -12; echo "exit=$?"`
Expected: FAIL lines for cert-manager-webhook, trust-manager, external-secrets-webhook, the prometheus-operator Service and the CloudNativePG webhook; `istiod` lines are `ok` (namespace outside the mesh). Record every FAIL line: any target not in the Global Constraints list is a finding to rule on (add its pod label in Step 2 and a ledger ruling).

- [ ] **Step 2: Pod labels**

`system/cert-manager/values.yaml`, under `cert-manager:` add:

```yaml
  webhook:
    # The API server (outside the mesh) calls it: keep it out of the mesh's strict mTLS
    podLabels:
      istio.io/dataplane-mode: none
```

and under `trust-manager:` add:

```yaml
  app:
    # The API server (outside the mesh) calls its webhook
    podLabels:
      istio.io/dataplane-mode: none
```

`platform/external-secrets/values.yaml` (new):

```yaml
external-secrets:
  webhook:
    # The API server (outside the mesh) calls it: keep it out of the mesh's strict mTLS
    podLabels:
      istio.io/dataplane-mode: none
```

`system/monitoring-system/values.yaml`, under `kube-prometheus-stack:` add:

```yaml
  prometheusOperator:
    # The API server (outside the mesh) calls its admission webhook
    podLabels:
      istio.io/dataplane-mode: none
```

`platform/cloudnative-pg/values.yaml`, under `cloudnative-pg:` add:

```yaml
  # The API server (outside the mesh) calls the operator's webhooks
  podLabels:
    istio.io/dataplane-mode: none
```

If any key already exists in a file, merge into it instead of adding a second one.

- [ ] **Step 3: Run the check**

Run: `scripts/check-mesh-exclusions | grep -c '^ok'; scripts/check-mesh-exclusions >/dev/null; echo "exit=$?"`
Expected: a count of at least 5 `ok` lines and `exit=0`.

- [ ] **Step 4: Commit**

```bash
git add scripts/check-mesh-exclusions system/cert-manager system/monitoring-system platform/external-secrets platform/cloudnative-pg
git commit -m "feat: webhook pods stay out of the ambient mesh; scripts/check-mesh-exclusions guards it"
```

### Task 4: Mesh tests

**Files:**
- Create: `test/mesh_test.go`
- Modify: `Makefile` (`make dev` filter gains `Mesh`)

**Interfaces:**
- Consumes: labels from Tasks 1–3; existing helpers `getResource(t, namespace, out, args...) bool` (`test/gateway_helpers_test.go`), `k8s.RunKubectlAndGetOutputE` (terratest).
- Produces: `TestMeshEnrolled`, `TestMeshRejectsPlaintext`, and the offline `TestMeshOptOutAllowed` (pure function `optOutAllowed(name string) bool`).

- [ ] **Step 1: Write the tests**

`test/mesh_test.go`:

```go
package test

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// meshOptOuts are the pods the API server calls as webhooks (scripts/check-mesh-exclusions), by
// app.kubernetes.io/name; the mesh's strict mTLS would refuse the API server's plaintext calls.
var meshOptOuts = []string{"webhook", "trust-manager", "external-secrets-webhook", "kube-prometheus-stack-prometheus-operator"}

func optOutAllowed(name string) bool {
	for _, n := range meshOptOuts {
		if n == name {
			return true
		}
	}
	return false
}

func TestMeshOptOutAllowed(t *testing.T) {
	assert.True(t, optOutAllowed("trust-manager"))
	assert.False(t, optOutAllowed("excalidraw"))
}

func meshSandboxOnly(t *testing.T) {
	if os.Getenv("ENV") != "dev" {
		t.Skip("mesh tests run against the sandbox only")
	}
}

type meshPod struct {
	Metadata struct {
		Name        string            `json:"name"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		HostNetwork bool `json:"hostNetwork"`
	} `json:"spec"`
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

// TestMeshEnrolled checks every namespace Argo CD manages is in the mesh (istio-system out) and every running
// pod there is redirected to ztunnel, except the webhook pods, which opt out.
func TestMeshEnrolled(t *testing.T) {
	meshSandboxOnly(t)
	var apps struct {
		Items []struct {
			Spec struct {
				Destination struct {
					Namespace string `json:"namespace"`
				} `json:"destination"`
			} `json:"spec"`
		} `json:"items"`
	}
	require.True(t, getResource(t, "argocd", &apps, "applications"))
	namespaces := map[string]bool{}
	for _, a := range apps.Items {
		namespaces[a.Spec.Destination.Namespace] = true
	}
	require.NotEmpty(t, namespaces)

	for ns := range namespaces {
		t.Run(ns, func(t *testing.T) {
			var namespace struct {
				Metadata struct {
					Labels map[string]string `json:"labels"`
				} `json:"metadata"`
			}
			require.True(t, getResource(t, "default", &namespace, "namespace", ns))
			want := map[bool]string{true: "none", false: "ambient"}[ns == "istio-system"]
			require.Equal(t, want, namespace.Metadata.Labels["istio.io/dataplane-mode"])
			if want == "none" {
				return
			}
			// Pods created before the namespace got its label are redirected only after a restart
			assert.Eventually(t, func() bool {
				var pods struct {
					Items []meshPod `json:"items"`
				}
				if !getResource(t, ns, &pods, "pods") {
					return false
				}
				for _, p := range pods.Items {
					if p.Spec.HostNetwork || p.Status.Phase == "Succeeded" || p.Status.Phase == "Failed" {
						continue
					}
					if p.Metadata.Labels["istio.io/dataplane-mode"] == "none" {
						if !optOutAllowed(p.Metadata.Labels["app.kubernetes.io/name"]) {
							t.Logf("%s/%s opts out of the mesh but is not a known webhook pod", ns, p.Metadata.Name)
							return false
						}
						continue
					}
					if p.Metadata.Annotations["ambient.istio.io/redirection"] != "enabled" {
						t.Logf("%s/%s is not redirected to ztunnel", ns, p.Metadata.Name)
						return false
					}
				}
				return true
			}, 5*time.Minute, 15*time.Second, "pods in %s are not all in the mesh", ns)
		})
	}
}

// meshCurl runs curl from a temporary pod in namespace default (outside the mesh unless labels enroll the pod)
// and returns the HTTP status it printed ("000" when the connection failed).
func meshCurl(t *testing.T, name, target string, labels string) string {
	options := k8s.NewKubectlOptions("", "", "default")
	options.Logger = logger.Discard
	args := []string{"run", name, "--rm", "-i", "--restart=Never", "--image=curlimages/curl:8.23.0"}
	if labels != "" {
		args = append(args, "--labels="+labels)
	}
	// Give istio-cni a moment to redirect the new pod before it connects
	args = append(args, "--", "sh", "-c", `sleep 3; curl -s -m 10 -o /dev/null -w "%{http_code}\n" `+target+` || true`)
	out, _ := k8s.RunKubectlAndGetOutputE(t, options, args...)
	status := regexp.MustCompile(`(?m)^[0-9]{3}$`).FindString(out)
	require.NotEmpty(t, status, "no HTTP status from %s:\n%s", name, out)
	return status
}

// TestMeshRejectsPlaintext proves strict mTLS: a pod outside the mesh is refused, a pod in the mesh gets through,
// and ztunnel records both identities of the mTLS connection.
func TestMeshRejectsPlaintext(t *testing.T) {
	meshSandboxOnly(t)
	target := "http://excalidraw.excalidraw"
	assert.Equal(t, "000", meshCurl(t, "mesh-plain", target, ""), "a pod outside the mesh reached a meshed service in plaintext")
	assert.Equal(t, "200", meshCurl(t, "mesh-member", target, "istio.io/dataplane-mode=ambient"))

	options := k8s.NewKubectlOptions("", "", "istio-system")
	options.Logger = logger.Discard
	logs, err := k8s.RunKubectlAndGetOutputE(t, options, "logs", "daemonset/ztunnel", "--since=5m")
	require.NoError(t, err)
	assert.Contains(t, logs, "policy rejection", "ztunnel did not log the refused plaintext connection")
	assert.True(t, strings.Contains(logs, `src.identity="spiffe://cluster.local/ns/default/sa/default"`) &&
		strings.Contains(logs, `dst.identity="spiffe://cluster.local/ns/excalidraw/sa/`),
		"ztunnel did not log the mTLS connection from default to excalidraw")
}
```

The `meshOptOuts` names must match the `app.kubernetes.io/name` labels of the opted-out pods. Check them while writing: `for c in system/cert-manager system/monitoring-system platform/external-secrets; do scripts/render-chart $c dev | yq 'select(.kind=="Deployment" and .spec.template.metadata.labels["istio.io/dataplane-mode"]=="none") | .spec.template.metadata.labels["app.kubernetes.io/name"]'; done` and correct the list (ledger the correction).

- [ ] **Step 2: Run the offline checks**

Run: `cd test && go vet ./... && go test -run 'TestMeshOptOutAllowed|TestSmokeAppsForEnv' ./...`
Expected: `ok`.

- [ ] **Step 3: `make dev` runs them**

In `Makefile`, `smoke_filter='Smoke|Identity|SSO|Gateway|Forgejo'` → `smoke_filter='Smoke|Identity|SSO|Gateway|Forgejo|Mesh'`. Same change wherever the filter is written in docs (`grep -rn "Gateway|Forgejo" docs CLAUDE.md`).

- [ ] **Step 4: Commit**

```bash
git add test/mesh_test.go Makefile docs CLAUDE.md
git commit -m "test: mesh enrollment and strict mTLS"
```

### Task 5: Docs

**Files:**
- Modify: `docs/reference/architecture/networking.md`, `CLAUDE.md`

- [ ] **Step 1: `networking.md`**

Append:

```markdown
## Service mesh

Every namespace Argo CD manages joins Istio's ambient mesh (`istio.io/dataplane-mode: ambient`, set through Argo CD's `managedNamespaceMetadata`): istio-cni redirects each pod's traffic to the ztunnel on its node, which wraps it in mutual TLS with the workload's SPIFFE identity. A mesh-wide `PeerAuthentication` (`STRICT`) makes meshed pods refuse plaintext. There are no sidecars; apps need no changes.

Outside the mesh:

- `istio-system` (the mesh itself) and `rook-ceph` (host-network pods, Ceph's own encryption), labelled `none` by the `root` ApplicationSet's `templatePatch`
- `kube-system`, which Argo CD does not manage
- the pods the Kubernetes API server calls as webhooks (cert-manager, trust-manager, External Secrets, the Prometheus operator, CloudNativePG), labelled `none` on the pod; `scripts/check-mesh-exclusions` fails if a chart adds a webhook that is still meshed

The Istio Gateways reach meshed pods over HBONE, so inbound traffic is unchanged. If ztunnel is down on a node, meshed pods there cannot open connections.
```

- [ ] **Step 2: `CLAUDE.md`**

- Roadmap item 6: `...; 6b Istio ambient (mesh, mTLS, AuthorizationPolicy, network policies)` → `...; 6b Istio ambient mesh with strict mTLS (done); 6c default-deny AuthorizationPolicy and NetworkPolicy with explicit allows`.
- Rules, basic checks: after `mkdocs build --strict` add `, scripts/check-mesh-exclusions`.
- Sandbox section, after the Gateway bullet, add: `- Every namespace Argo CD manages joins Istio's ambient mesh with strict mTLS (label from the shared `managedNamespaceMetadata`; `istio-system` and `rook-ceph` get `none` from the `root` `templatePatch`; `kube-system` is not managed). Pods the API server calls (webhooks) must carry the pod label `istio.io/dataplane-mode: none`; `scripts/check-mesh-exclusions` checks every chart. Istio's `k3s` platform profile serves the k3d sandbox too (its `k3d` profile has the wrong CNI path).`
- `make dev runs ...` filter text: add `|Mesh`.
- "Before the first production deploy": add `- The ambient mesh on k3s, the CloudNativePG operator's opt-out and rook-ceph's exclusion were only rendered. Pods that start before istio-cni on a rebooted node are not redirected until they restart.`

- [ ] **Step 3: Check**

Run: `mkdocs build --strict 2>&1 | tail -1`
Expected: `Documentation built`.

- [ ] **Step 4: Commit**

```bash
git add docs CLAUDE.md
git commit -m "docs: Istio ambient mesh"
```

### Task 6: After the merge into `dev` (sandbox)

Runs only after the PR is merged (ask before merging).

- [ ] **Step 1: Build from scratch and test**

Run: `make dev-clean; make dev 2>&1 | tee /tmp/claude-1000/make-dev-ambient.log | tail -5`
Expected: `DONE 76 tests` (73 + `TestMeshOptOutAllowed`, `TestMeshEnrolled`, `TestMeshRejectsPlaintext`, plus their subtests counted by gotestsum — record the actual count) with no failures.

- [ ] **Step 2: Prometheus still scrapes**

Run: `kubectl get --raw "/api/v1/namespaces/monitoring-system/services/monitoring-system-kube-pro-prometheus:9090/proxy/api/v1/query?query=count(up==1)" | jq -r '.data.result[0].value[1]'` and the same with `up==0`.
Expected: `up==0` empty or `0`.

- [ ] **Step 3: RAM and the docs PR**

Measure as in step 6a; on a new branch from `dev` update the RAM table in `docs/installation/sandbox.md` (`istio-system` now includes istio-cni and ztunnel), `mkdocs build --strict`, commit `docs: sandbox RAM with the ambient mesh`, PR into `dev`.
