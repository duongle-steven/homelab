# Network Config Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every environment-specific address comes from `infra/<env>/cluster/config.json`; no chart or app file holds an IP address.

**Architecture:** Argo CD's `root` and `apps` ApplicationSets wrap their generator in a `matrix` with a git files generator that reads the environment's `config.json`, and pass `global.addresses.*` to every chart through `helm.valuesObject`. `scripts/env-values` resolves the same values offline for `scripts/render-chart`; `scripts/check-network-config` fails on any rendered address that does not come from `config.json`. Prometheus finds the control-plane nodes itself (`ScrapeConfig`, node discovery) instead of an address list.

**Tech Stack:** Argo CD ApplicationSet (matrix, git files generator, goTemplate), Helm, app-template 5.3.0 (`tpl` on strings), kube-prometheus-stack 92.3.0 (`ScrapeConfig` v1alpha1), POSIX sh + yq, Python 3 + PyYAML, Go tests, Ansible inventory.

**Spec:** `docs/superpowers/specs/2026-10-15-network-config-design.md`

## Global Constraints

- Production addresses: router `192.168.0.1`, Kubernetes API VIP `192.168.0.10`, Gateway VIP `192.168.0.2`, wireguard VIP `192.168.0.3`, nodes `192.168.0.11-13`, prefix length 24.
- `config.json` keeps upstream `nixos`'s format (`vip`, `network.prefix_length`, `network.gateway`) plus `service_vips.gateway` and `service_vips.wireguard`.
- Helm values: `global.addresses.apiVIP`, `global.addresses.router`, `global.addresses.gatewayVIP`, `global.addresses.wireguardVIP`; the sandbox sets them all to `""`.
- Addresses allowed in renders without coming from `config.json`: `0.0.0.0`, `127.0.0.0/8`, `10.42.0.0/16`, `10.43.0.0/16` (k3s pod and Service ranges), `172.16.0.0/24` (wireguard tunnel); CustomResourceDefinitions are skipped (their descriptions cite RFC section numbers like `4.2.1.10`).
- Control-plane job names stay `kube-controller-manager`, `kube-scheduler`, `kube-etcd`, `kube-proxy`.

## Review Focus

1. Argo CD must accept the matrix (two git generators on the same repo): the files generator's `.path` would clash with the directories generator's, so it uses `pathParamPrefix: config`. Pinned by Task 5's fresh `make dev` (every Application generated, all tests pass) and Task 1's render check.
2. Empty sandbox values must not leave broken output: the Gateway keeps ServiceLB, homepage drops the router link, wireguard and tailscale are not in the sandbox. Pinned in Task 2 (dev render checks).
3. `scripts/render-chart` must pass the same values Argo CD does, or the offline checks prove nothing: `scripts/env-values` reads the ApplicationSet's own `valuesObject` and generator path. Pinned in Task 1.
4. A render failure (chart download reset) must fail the check, not pass it with nothing rendered. Pinned in Task 2 (retries, then exit).
5. The control-plane scrape must authenticate like the chart's ServiceMonitors (Bearer token Secret, HTTPS with `insecureSkipVerify` for controller-manager and scheduler; HTTP for etcd and kube-proxy). Pinned by Task 3's sandbox test.

---

### Task 1: Environment config and parameter plumbing

**Files:**
- Create: `infra/production/cluster/config.json`, `infra/sandbox/cluster/config.json`, `scripts/env-values`
- Modify: `system/argocd/values.yaml` (`root` and `apps` ApplicationSets), `system/argocd/values-dev.yaml`, `scripts/render-chart`

**Interfaces:**
- Produces: `scripts/env-values <prod|dev> <root|apps>` prints `name=value` lines (`global.addresses.apiVIP=192.168.0.10` …); `scripts/render-chart` passes them as `--set-string` to charts (`root`) and app files (`apps`). Charts read `.Values.global.addresses.{apiVIP,router,gatewayVIP,wireguardVIP}`.

- [ ] **Step 1: Write the check**

Create `.superpowers/sdd/2026-10-15-network-config/check-task1.sh`:

```sh
#!/bin/sh
# config.json reaches the charts the way Argo CD passes it
set -e
expect_prod='global.addresses.apiVIP=192.168.0.10
global.addresses.gatewayVIP=192.168.0.2
global.addresses.router=192.168.0.1
global.addresses.wireguardVIP=192.168.0.3'
expect_dev='global.addresses.apiVIP=
global.addresses.gatewayVIP=
global.addresses.router=
global.addresses.wireguardVIP='
for appset in root apps; do
  test "$(scripts/env-values prod $appset | sort)" = "$expect_prod"
  test "$(scripts/env-values dev $appset | sort)" = "$expect_dev"
done
f=$(mktemp); trap 'rm -f "$f"' EXIT
for env in prod dev; do
  scripts/render-chart system/argocd $env > "$f" 2>/dev/null
  case $env in prod) want=infra/production/cluster/config.json ;; dev) want=infra/sandbox/cluster/config.json ;; esac
  got=$(yq ea '[select(.kind == "ApplicationSet") | .spec.generators[0].matrix.generators[1].git | .files[0].path + " " + .pathParamPrefix] | unique | join(",")' "$f")
  echo "$env: $got"
  test "$got" = "$want config"
  test "$(yq ea '[select(.kind == "ApplicationSet") | (.spec.template.spec.source.helm.valuesObject.global.addresses.apiVIP, .spec.template.spec.sources[0].helm.valuesObject.global.addresses.apiVIP) | select(. != null)] | unique | join(",")' "$f")" = '{{ .vip }}'
done
echo "task1: ok"
```

Run: `chmod +x .superpowers/sdd/2026-10-15-network-config/check-task1.sh && .superpowers/sdd/2026-10-15-network-config/check-task1.sh`
Expected: FAIL (`scripts/env-values: not found`).

- [ ] **Step 2: Config files**

`infra/production/cluster/config.json`:

```json
{
  "vip": "192.168.0.10",
  "network": {
    "prefix_length": 24,
    "gateway": "192.168.0.1"
  },
  "service_vips": {
    "gateway": "192.168.0.2",
    "wireguard": "192.168.0.3"
  }
}
```

`infra/sandbox/cluster/config.json` (k3d has no kube-vip; charts skip empty values):

```json
{
  "vip": "",
  "network": {
    "prefix_length": 0,
    "gateway": ""
  },
  "service_vips": {
    "gateway": "",
    "wireguard": ""
  }
}
```

- [ ] **Step 3: `system/argocd/values.yaml`**

In the `root` ApplicationSet replace the generators with:

```yaml
      generators:
        # Every component, crossed with the environment's addresses (infra/<env>/cluster/config.json, upstream's
        # format; OpenTofu reads the same file in step 7)
        - matrix:
            generators:
              - git:
                  repoURL: &repoURL http://forgejo-http.forgejo:3000/ops/homelab
                  revision: &revision master
                  directories:
                    - path: system/*
                    - path: platform/*
                    - path: apps/*
              - git:
                  repoURL: *repoURL
                  revision: *revision
                  files:
                    - path: infra/production/cluster/config.json
                  # Its own .path would replace the directory's
                  pathParamPrefix: config
```

In the `root` template's `source` (after `targetRevision: *revision`) add:

```yaml
            helm:
              # scripts/env-values resolves these offline for scripts/render-chart
              valuesObject:
                global:
                  network: &network
                    apiVIP: '{{ .vip }}'
                    router: '{{ .network.gateway }}'
                    gatewayVIP: '{{ .service_vips.gateway }}'
                    wireguardVIP: '{{ .service_vips.wireguard }}'
```

In the `apps` ApplicationSet replace the generators with the same shape (files generator on `apps/*.yaml` first):

```yaml
      generators:
        - matrix:
            generators:
              - git:
                  repoURL: *repoURL
                  revision: *revision
                  files:
                    - path: apps/*.yaml
              - git:
                  repoURL: *repoURL
                  revision: *revision
                  files:
                    - path: infra/production/cluster/config.json
                  pathParamPrefix: config
```

and in `sources[0].helm` (next to `parameters`) add:

```yaml
                valuesObject:
                  global:
                    network: *network
```

- [ ] **Step 4: `system/argocd/values-dev.yaml`**

Replace the `root` generators with the matrix, keeping its directory list:

```yaml
      generators:
        - matrix:
            generators:
              - git:
                  repoURL: &repoURL https://github.com/duongle-steven/homelab
                  # make dev force-pushes the current commit here, so a branch is tested before its merge
                  revision: &revision sandbox
                  directories:
                    - path: system/argocd
                    - path: system/istio-system
                    - path: system/cert-manager
                    - path: system/monitoring-system
                    - path: platform/external-secrets
                    - path: platform/global-secrets
                    - path: platform/kanidm
                    - path: platform/grafana
                    - path: platform/zot
                    - path: platform/forgejo
              - git:
                  repoURL: *repoURL
                  revision: *revision
                  files:
                    - path: infra/sandbox/cluster/config.json
                  pathParamPrefix: config
```

Replace the `apps` generators likewise (its seven `apps/<name>.yaml` paths as the first generator's `files`, the second generator as above). In the restated `apps` `sources[0].helm`, add:

```yaml
                valuesObject:
                  global:
                    network:
                      apiVIP: '{{ .vip }}'
                      router: '{{ .network.gateway }}'
                      gatewayVIP: '{{ .service_vips.gateway }}'
                      wireguardVIP: '{{ .service_vips.wireguard }}'
```

(`root`'s `source.helm` map merges with production's, so its `valuesObject` is not restated.)

- [ ] **Step 5: `scripts/env-values`**

```sh
#!/bin/sh
# Print the values Argo CD passes from the environment's config file (infra/<env>/cluster/config.json) through the
# ApplicationSet <appset>'s helm.valuesObject, as name=value lines, so offline renders match the cluster's.
# Usage: scripts/env-values <prod|dev> <root|apps>
set -eu
env="$1"
appset="$2"
argocd=system/argocd
# The sandbox restates generators and the apps source in values-<env>.yaml
pick() {
  if [ "$env" != prod ] && [ "$(yq "$1" "$argocd/values-$env.yaml")" != null ]; then
    yq "$1" "$argocd/values-$env.yaml"
  else
    yq "$1" "$argocd/values.yaml"
  fi
}
base=".argocd-apps.applicationsets.$appset"
config=$(pick "$base.generators[0].matrix.generators[1].git.files[0].path")
case "$appset" in
  root) object="$base.template.spec.source.helm.valuesObject" ;;
  apps) object="$base.template.spec.sources[0].helm.valuesObject" ;;
esac
pick "$object" | yq '.. | select(tag == "!!str") | (path | join(".")) + "=" + .' | while IFS='=' read -r name value; do
  field=$(printf '%s' "$value" | sed -n 's/^{{ *\(\.[A-Za-z_.]*\) *}}$/\1/p')
  if [ -n "$field" ]; then
    value=$(yq -p json -o yaml "$field" "$config")
    [ "$value" != null ] || { echo "$config has no $field" >&2; exit 1; }
  fi
  printf '%s=%s\n' "$name" "$value"
done
```

(yq prints an empty string as `""`: if Step 6 shows `global.addresses.router=""`, strip the quotes with `value=$(... | sed 's/^""$//')` and ledger it.)

- [ ] **Step 6: `scripts/render-chart`**

In the app-file branch, after the loop over `source_of '.helm.parameters[] …'`, add:

```sh
  for value in $(scripts/env-values "$env" apps); do
    set -- "$@" --set-string "$value"
  done
```

Before the chart branch's `[ -f "$target/values.yaml" ] && …` line, add:

```sh
# Argo CD passes the environment's addresses to every component (scripts/env-values)
for value in $(scripts/env-values "$env" root); do
  set -- "$@" --set-string "$value"
done
```

- [ ] **Step 7: Run the check**

Run: `chmod +x scripts/env-values && .superpowers/sdd/2026-10-15-network-config/check-task1.sh`
Expected: `prod: infra/production/cluster/config.json config`, `dev: infra/sandbox/cluster/config.json config`, `task1: ok`.

- [ ] **Step 8: Commit**

```bash
git add infra scripts/env-values scripts/render-chart system/argocd/values.yaml system/argocd/values-dev.yaml
git commit -m "feat: environment addresses from infra/<env>/cluster/config.json"
```

### Task 2: Consumers and `scripts/check-network-config`

**Files:**
- Create: `scripts/check-network-config`
- Modify: `system/istio-system/values.yaml`, `system/istio-system/values-dev.yaml`, `system/istio-system/templates/gateway.yaml`, `apps/wireguard.yaml`, `apps/tailscale.yaml`, `apps/homepage.yaml`

**Interfaces:**
- Consumes: `.Values.global.addresses.*` (Task 1).
- Produces: `scripts/check-network-config` (exit 0 when clean; prints one line per stray address).

- [ ] **Step 1: Write the check script**

`scripts/check-network-config`:

```python
#!/usr/bin/env python3
"""No chart or app file holds an IP address.

Renders every component and app file in production and the sandbox (scripts/render-chart, which passes the
environment's addresses like Argo CD) and fails on any IPv4 address that does not come from the environment's
infra/<env>/cluster/config.json, apart from addresses that are the same everywhere (FIXED). In production it also
checks that each address reaches the component that uses it (USES).
"""

import glob
import ipaddress
import json
import re
import subprocess
import sys
import time

import yaml

CONFIG = {"prod": "infra/production/cluster/config.json", "dev": "infra/sandbox/cluster/config.json"}
# Any address, loopback, k3s's pod and Service ranges, the wireguard tunnel
FIXED = [ipaddress.ip_network(n) for n in ("0.0.0.0/32", "127.0.0.0/8", "10.42.0.0/16", "10.43.0.0/16", "172.16.0.0/24")]
# Production: config.json field -> components whose render must contain its value
USES = {
    "service_vips.gateway": ["system/istio-system", "apps/tailscale.yaml"],
    "service_vips.wireguard": ["apps/wireguard.yaml"],
    "network.gateway": ["apps/homepage.yaml"],
}
IPV4 = re.compile(r"(?<![\d.])(\d{1,3}(?:\.\d{1,3}){3})(?![\d.])")


def render(target, env):
    # Chart downloads sometimes reset the connection: try three times
    for attempt in range(3):
        out = subprocess.run(["scripts/render-chart", target, env], capture_output=True, text=True)
        if out.returncode == 0:
            return [d for d in yaml.safe_load_all(out.stdout) if d]
        time.sleep(5)
    sys.exit(f"render failed: {target} {env}\n{out.stderr}")


def targets():
    dirs = sorted(glob.glob("system/*/") + glob.glob("platform/*/") + glob.glob("apps/*/"))
    return [d.rstrip("/") for d in dirs if glob.glob(d + "Chart.yaml")] + sorted(glob.glob("apps/*.yaml"))


def field(config, path):
    for key in path.split("."):
        config = config[key]
    return config


def addresses(value):
    if isinstance(value, dict):
        return set().union(*map(addresses, value.values())) if value else set()
    return {value} if isinstance(value, str) and value else set()


def main():
    failures = []
    for env, path in CONFIG.items():
        with open(path) as f:
            config = json.load(f)
        allowed = addresses(config)
        for target in targets():
            text = ""
            for doc in render(target, env):
                if doc.get("kind") == "CustomResourceDefinition":
                    continue
                dumped = yaml.safe_dump(doc)
                text += dumped
                for match in IPV4.findall(dumped):
                    try:
                        address = ipaddress.ip_address(match)
                    except ValueError:
                        continue
                    if match in allowed or any(address in n for n in FIXED):
                        continue
                    failures.append(f"{target} ({env}): {doc.get('kind')}/{doc.get('metadata', {}).get('name')}: {match}")
            if env == "prod":
                for key, users in USES.items():
                    if target in users and field(config, key) not in text:
                        failures.append(f"{target} (prod): does not use {key} ({field(config, key)})")
    for failure in failures:
        print(failure)
    print(f"check-network-config: {len(failures)} failures")
    sys.exit(1 if failures else 0)


if __name__ == "__main__":
    main()
```

- [ ] **Step 2: Run it (fails)**

Run: `chmod +x scripts/check-network-config && scripts/check-network-config > .superpowers/sdd/2026-10-15-network-config/check-red.log; tail -1 .superpowers/sdd/2026-10-15-network-config/check-red.log; grep -c 192.168.1 .superpowers/sdd/2026-10-15-network-config/check-red.log`
Expected: non-zero failures, including `192.168.1.224` (istio-system, tailscale, wireguard), `192.168.1.225`, `192.168.1.100`, `192.168.1.1`, `192.168.1.110-113` (monitoring-system), and `does not use` lines.

- [ ] **Step 3: Istio Gateway**

`system/istio-system/values.yaml`: replace the `gateway.loadBalancer` block with

```yaml
gateway:
  # kube-vip serves the LAN Gateway's Service on its own VIP (global.addresses.gatewayVIP, from
  # infra/<env>/cluster/config.json): Forgejo SSH keeps port 22 (node sshd owns it on the node IPs) and the address
  # moves to another node when one fails. Without a VIP (the sandbox), k3s ServiceLB serves it.
  loadBalancerClass: kube-vip.io/kube-vip-class
```

`system/istio-system/values-dev.yaml`: delete the line `  loadBalancer: null` (and the `gateway:` key if it becomes empty).

`system/istio-system/templates/gateway.yaml`: replace `{{- with .Values.gateway.loadBalancer }}` … `{{- end }}` (the ConfigMap) with

```yaml
{{- with .Values.global.addresses.gatewayVIP }}
# Merged into the Service Istio generates for the Gateway (spec.infrastructure.parametersRef)
apiVersion: v1
kind: ConfigMap
metadata:
  name: gateway-options
data:
  service: |
    metadata:
      annotations:
        kube-vip.io/loadbalancerIPs: {{ . }}
    spec:
      loadBalancerClass: {{ $.Values.gateway.loadBalancerClass }}
---
{{- end }}
```

and `{{- if .Values.gateway.loadBalancer }}` with `{{- if .Values.global.addresses.gatewayVIP }}`.

- [ ] **Step 4: App files**

`apps/wireguard.yaml`:
- Service annotation: `kube-vip.io/loadbalancerIPs: 192.168.1.225` → `kube-vip.io/loadbalancerIPs: '{{ .Values.global.addresses.wireguardVIP }}'`; the comment above it → `# Its own kube-vip address (global.addresses.wireguardVIP, from infra/<env>/cluster/config.json), like the LAN Gateway`.
- The `homelab` peer: `AllowedIPs = 172.16.0.1/32, 192.168.1.100/32, 192.168.1.224/27` → `AllowedIPs = 172.16.0.1/32`, and add above `[Peer]` of `homelab` the line `# Clients also route the API and Gateway VIPs (infra/<env>/cluster/config.json) through this peer`. (This ExternalSecret template is ESO's, not Helm's, so it cannot read Helm values; the server ignores the peer whose key is its own, so only clients copying the list used these addresses. docs/getting-started/vpn-setup.md shows the client config.)

`apps/tailscale.yaml`: `TS_ROUTES: 192.168.1.224/27` → `TS_ROUTES: '{{ .Values.global.addresses.gatewayVIP }}/32'`.

`apps/homepage.yaml`: replace

```yaml
          - Router:
            - href: https://192.168.1.1
              icon: router.svg
```

with

```yaml
          {{- with .Values.global.addresses.router }}
          - Router:
            - href: https://{{ . }}
              icon: router.svg
          {{- end }}
```

- [ ] **Step 5: Renders**

Run:
```sh
scripts/render-chart system/istio-system | grep -A3 'loadbalancerIPs'
scripts/render-chart system/istio-system dev | grep -c 'gateway-options'
scripts/render-chart apps/wireguard.yaml | grep loadbalancerIPs
scripts/render-chart apps/tailscale.yaml | grep -A1 TS_ROUTES
scripts/render-chart apps/homepage.yaml | grep -B1 -A2 'Router'
scripts/render-chart apps/homepage.yaml dev | grep -c 'Router'
```
Expected: `kube-vip.io/loadbalancerIPs: 192.168.0.2` with `loadBalancerClass: kube-vip.io/kube-vip-class`; `0` (no ConfigMap in the sandbox, ServiceLB); `192.168.0.3`; `value: 192.168.0.2/32`; `href: https://192.168.0.1` under `Router:` with the YAML indentation intact; `0`.

- [ ] **Step 6: Run the check**

Run: `scripts/check-network-config | tail -6`
Expected: only `system/monitoring-system (prod)` failures for `192.168.1.110`–`192.168.1.113` remain (Task 3).

- [ ] **Step 7: Commit**

```bash
git add scripts/check-network-config system/istio-system apps/wireguard.yaml apps/tailscale.yaml apps/homepage.yaml
git commit -m "feat: the Gateway, wireguard, tailscale and homepage read the environment's addresses"
```

### Task 3: Control-plane scrape by node discovery

**Files:**
- Create: `system/monitoring-system/templates/control-plane-scrape.yaml`, `test/monitoring_test.go`
- Modify: `system/monitoring-system/values.yaml`, `system/monitoring-system/values-dev.yaml`, `metal/k3d-dev.yaml`, `test/mesh_test.go`, `Makefile`

**Interfaces:**
- Produces: `prometheusQuery(t *testing.T, query string) ([]map[string]string, error)` in `test/monitoring_test.go` (the `metric` label sets of an instant query); `TestMeshPrometheusTargetsUp` uses it.

- [ ] **Step 1: Write the failing test**

`test/monitoring_test.go`:

```go
package test

import (
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/stretchr/testify/assert"
)

// prometheusQuery runs an instant query through the API server's service proxy and returns each result's labels.
func prometheusQuery(t *testing.T, query string) ([]map[string]string, error) {
	options := k8s.NewKubectlOptions("", "", "monitoring-system")
	options.Logger = logger.Discard
	out, err := k8s.RunKubectlAndGetOutputE(t, options, "get", "--raw",
		"/api/v1/namespaces/monitoring-system/services/monitoring-system-kube-pro-prometheus:9090/proxy/api/v1/query?query="+url.QueryEscape(query))
	if err != nil {
		return nil, err
	}
	var res struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return nil, err
	}
	metrics := make([]map[string]string, 0, len(res.Data.Result))
	for _, r := range res.Data.Result {
		metrics = append(metrics, r.Metric)
	}
	return metrics, nil
}

// Prometheus finds the control-plane nodes itself (ScrapeConfig, node discovery): no address list to keep in step
func TestMonitoringControlPlaneTargets(t *testing.T) {
	meshSandboxOnly(t)
	jobs := []string{"kube-controller-manager", "kube-scheduler", "kube-etcd", "kube-proxy"}
	assert.Eventually(t, func() bool {
		metrics, err := prometheusQuery(t, `count by (job) (up{job=~"kube-controller-manager|kube-scheduler|kube-etcd|kube-proxy"} == 1)`)
		if err != nil {
			return false
		}
		found := map[string]bool{}
		for _, m := range metrics {
			found[m["job"]] = true
		}
		for _, job := range jobs {
			if !found[job] {
				t.Logf("no target up for job %s", job)
				return false
			}
		}
		return true
	}, 5*time.Minute, 30*time.Second, "control-plane jobs have no target up")
}
```

In `test/mesh_test.go`, replace the body of `TestMeshPrometheusTargetsUp` after `meshSandboxOnly(t)` with:

```go
	assert.Eventually(t, func() bool {
		metrics, err := prometheusQuery(t, "up == 0")
		if err != nil {
			return false
		}
		for _, m := range metrics {
			t.Logf("target down: %v", m)
		}
		return len(metrics) == 0
	}, 5*time.Minute, 30*time.Second, "Prometheus has targets down")
```

and remove imports it no longer uses (`go vet` tells).

In `Makefile`, `smoke_filter='Smoke|Identity|SSO|Gateway|Forgejo|Mesh'` → `smoke_filter='Smoke|Identity|SSO|Gateway|Forgejo|Mesh|Monitoring'`.

- [ ] **Step 2: Run it (fails on the running sandbox)**

Run: `cd test && go vet ./... && cd .. && make env=dev ENV=dev INSECURE_SKIP_VERIFY=1 smoke_filter='TestMonitoringControlPlaneTargets|TestMeshPrometheusTargetsUp' smoke-test 2>&1 | grep -E '^(PASS|FAIL) Test|no target up' | sort -u`
Expected: `FAIL TestMonitoringControlPlaneTargets` (the sandbox does not scrape the control plane), `PASS TestMeshPrometheusTargetsUp`.

- [ ] **Step 3: ScrapeConfigs**

`system/monitoring-system/templates/control-plane-scrape.yaml`:

```yaml
# k3s runs the control plane and kube-proxy inside its own process, so kube-prometheus-stack's pod selectors find
# nothing: discover the nodes instead (no address list). Job names stay the chart's, for its dashboards and rules.
{{- $components := list
  (dict "job" "kube-controller-manager" "port" 10257 "https" true "controlPlane" true)
  (dict "job" "kube-scheduler" "port" 10259 "https" true "controlPlane" true)
  (dict "job" "kube-etcd" "port" 2381 "https" false "controlPlane" true)
  (dict "job" "kube-proxy" "port" 10249 "https" false "controlPlane" false) }}
{{- range $components }}
---
apiVersion: monitoring.coreos.com/v1alpha1
kind: ScrapeConfig
metadata:
  name: {{ .job }}
  labels:
    # Prometheus selects ScrapeConfigs of its release
    release: {{ $.Release.Name }}
spec:
  kubernetesSDConfigs:
    - role: Node
  {{- if .https }}
  scheme: HTTPS
  tlsConfig:
    insecureSkipVerify: true
  {{- end }}
  authorization:
    type: Bearer
    credentials:
      name: {{ $.Release.Name }}-kube-pro-prometheus-token
      key: token
  relabelings:
    {{- if .controlPlane }}
    - sourceLabels: [__meta_kubernetes_node_label_node_role_kubernetes_io_control_plane]
      regex: "true"
      action: keep
    {{- end }}
    - sourceLabels: [__meta_kubernetes_node_address_InternalIP]
      targetLabel: __address__
      replacement: ${1}:{{ .port }}
    - targetLabel: job
      replacement: {{ .job }}
    - sourceLabels: [__meta_kubernetes_node_name]
      targetLabel: instance
  {{- if ne .job "kube-etcd" }}
  # Every port of the k3s process serves all components' metrics: keep only this component's
  metricRelabelings:
    - action: drop
      sourceLabels: [__name__]
      regex: (apiserver|etcd|kubelet|container)_.+
  {{- end }}
{{- end }}
```

`system/monitoring-system/values.yaml`: replace the four blocks `kubeControllerManager`, `kubeScheduler`, `kubeEtcd`, `kubeProxy` (and the comment above them) with

```yaml
  # templates/control-plane-scrape.yaml scrapes these by node discovery; the chart's address-list scrape is off,
  # its rules and dashboards stay
  kubeControllerManager:
    enabled: false
  kubeScheduler:
    enabled: false
  kubeEtcd:
    enabled: false
  kubeProxy:
    enabled: false
```

Check that the chart keeps the rules: `scripts/render-chart system/monitoring-system | yq 'select(.kind=="PrometheusRule") | .metadata.name' | grep -E 'controller|scheduler|etcd|proxy'`. If they disappear with `enabled: false`, set `defaultRules.rules.{etcd,kubeControllerManager,kubeProxy,kubeSchedulerAlerting,kubeSchedulerRecording}: true` explicitly and ledger it.

`system/monitoring-system/values-dev.yaml`: delete the comment `# k3s runs them inside its own process …`, the four `enabled: false` blocks and the `defaultRules` block that turns their rules off (the sandbox scrapes them now).

`metal/k3d-dev.yaml`: the comment `# Control plane metrics like production (the sandbox does not scrape them: no fixed node address)` → `# Control plane metrics like production (system/monitoring-system scrapes them by node discovery)`, and add after the scheduler arg:

```yaml
      - arg: --kube-proxy-arg=metrics-bind-address=0.0.0.0
        nodeFilters: [server:*]
```

- [ ] **Step 4: Render and check**

Run: `scripts/render-chart system/monitoring-system | yq 'select(.kind=="ScrapeConfig") | .metadata.name' && scripts/check-network-config | tail -1`
Expected: the four job names; `check-network-config: 0 failures`.

- [ ] **Step 5: Run the test on the sandbox**

The running sandbox syncs branch `sandbox` and its k3d node lacks the kube-proxy flag, so commit (Step 6) first, then:

Run: `make dev-clean; make dev 2>&1 | tail -3` is Task 5's job; here run the quicker proof: `make dev > .superpowers/sdd/2026-10-15-network-config/make-dev-task3.log 2>&1; grep -E '^(PASS|FAIL) Test(Monitoring|MeshPrometheus)|^DONE' .superpowers/sdd/2026-10-15-network-config/make-dev-task3.log`
Expected: `PASS TestMonitoringControlPlaneTargets`, `PASS TestMeshPrometheusTargetsUp` except `kube-proxy` if the existing k3d node lacks the flag. If only `kube-proxy` fails here, ledger it and rely on Task 5's fresh sandbox.

- [ ] **Step 6: Commit** (before Step 5)

```bash
git add system/monitoring-system metal/k3d-dev.yaml test/monitoring_test.go test/mesh_test.go Makefile
git commit -m "feat(monitoring): scrape the control plane by node discovery, in the sandbox too"
```

### Task 4: Ansible inventory, configure script, docs

**Files:**
- Modify: `metal/inventories/prod.yml`, `scripts/configure`, `docs/reference/architecture/networking.md`, `docs/getting-started/vpn-setup.md`, `docs/installation/` (the page that tells a new user what to configure), `CLAUDE.md`

- [ ] **Step 1: Inventory**

`metal/inventories/prod.yml`:

```yaml
all:
  vars:
    # The Kubernetes API VIP comes from the environment's config (infra/production/cluster/config.json)
    control_plane_endpoint: "{{ (lookup('file', inventory_dir ~ '/../../infra/production/cluster/config.json') | from_json).vip }}"
metal:
  children:
    masters:
      hosts:
        # Fill in each machine's MAC address, disk and network interface before the first deploy
        metal0: {ansible_host: 192.168.0.11, mac: 'FILL-ME', disk: sda, network_interface: eno1}
        metal1: {ansible_host: 192.168.0.12, mac: 'FILL-ME', disk: sda, network_interface: eno1}
        metal2: {ansible_host: 192.168.0.13, mac: 'FILL-ME', disk: sda, network_interface: eno1}
```

(`gateway_ip` and `wireguard_ip` are gone: nothing in `metal/` reads them. `metal3`, upstream's agent, is gone with upstream's fourth machine; keep the `workers` group only if `metal/` playbooks reference it — check with `grep -rn workers metal/*.yml metal/roles`, and if they do, keep `workers: {hosts: {}}`.)

Run: `cd metal && ansible -i inventories/prod.yml metal0 -m debug -a var=control_plane_endpoint -c local 2>&1 | grep control_plane_endpoint`
Expected: `"control_plane_endpoint": "192.168.0.10"`.

- [ ] **Step 2: `scripts/configure`**

Remove `gateway_ip` and `wireguard_ip` from `upstream_config`, their two `Prompt.ask` lines and their two `find_and_replace` calls. Add after the remaining prompts a printed hint: `print("Addresses (API, Gateway and wireguard VIPs, router): edit infra/production/cluster/config.json")`.

Run: `python3 -c "import ast,sys; ast.parse(open('scripts/configure').read())" && grep -c '_ip' scripts/configure`
Expected: no error; `0`.

- [ ] **Step 3: Docs**

- `docs/reference/architecture/networking.md`: `192.168.1.224` → `192.168.0.2`, `192.168.1.100` → `192.168.0.10`, and add after the paragraph on kube-vip: "These addresses are set in `infra/<env>/cluster/config.json` (upstream's format: `vip`, `network`, plus `service_vips`); Argo CD passes them to every chart as `global.addresses.*`, and `scripts/check-network-config` keeps addresses out of charts and app files."
- `docs/getting-started/vpn-setup.md`: `192.168.1.226` → `192.168.0.3` (the wireguard VIP; the old value was already off by one), `AllowedIPs = 172.16.0.1/32, 192.168.1.224/27` → `AllowedIPs = 172.16.0.1/32, 192.168.0.10/32, 192.168.0.2/32` with a sentence: "Use the `vip` and `service_vips.gateway` of `infra/production/cluster/config.json`."
- The installation page that lists what a new user changes (`grep -rln "make configure" docs/installation docs/getting-started`): add "Set your network's addresses in `infra/production/cluster/config.json`."
- `docs/how-to-guides/add-or-remove-nodes.md` uses example addresses in a diff; change `192.168.1.11x` to `192.168.0.1x` to match (`.110`→`.11`, `.111`→`.12`, `.112`→`.13`, `.113`→`.14`, `.114`→`.15`).

- [ ] **Step 4: `CLAUDE.md`**

- Roadmap 6: replace "; 6d egress" with "; 6d-1 network addresses from `infra/<env>/cluster/config.json` (done); 6d-2 egress".
- Rules, after the version rule: "- Addresses that depend on where the homelab runs (VIPs, router) live only in `infra/<env>/cluster/config.json` (upstream `nixos`'s format plus `service_vips`); Argo CD passes them to every chart and app as `global.addresses.*` (`scripts/env-values` does the same offline). Charts and app files hold no IP address: `scripts/check-network-config` checks it."
- Basic checks list: add `scripts/check-network-config` after `scripts/check-mesh-policies`, and the offline Go test list is unchanged.
- Sandbox: `make dev` runs `Smoke|Identity|SSO|Gateway|Forgejo|Mesh|Monitoring`.
- Production checklist: replace `192.168.1.224` (Gateway) and `192.168.1.225` (wireguard) with `192.168.0.2` and `192.168.0.3`; replace "The node `metal3` is still a k3s agent (upstream makes every node a server)." with "`metal/inventories/prod.yml` has the node addresses `192.168.0.11-13` but placeholder MAC addresses (`FILL-ME`), disks and interfaces: fill them in first."; add "Wireguard's peer list (`apps/wireguard.yaml`: keys, the `horus` peer) is upstream's; replace it with your own peers." and "The control-plane scrape by node discovery ran only on the one-node sandbox."
- Run `grep -n "192.168.1" CLAUDE.md docs/reference docs/getting-started docs/installation docs/how-to-guides` — Expected: no output.

- [ ] **Step 5: Docs build and commit**

Run: `mkdocs build --strict 2>&1 | tail -1`
Expected: `Documentation built`.

```bash
git add metal/inventories/prod.yml scripts/configure docs CLAUDE.md
git commit -m "docs: network addresses live in infra/<env>/cluster/config.json"
```

### Task 5: Prove it on a fresh sandbox

- [ ] **Step 1: Basic checks**

Run: `scripts/check-network-config | tail -1; scripts/check-mesh-policies > .superpowers/sdd/2026-10-15-network-config/mesh.log 2>&1; echo mesh=$?; (cd test && go vet ./... && go test -run 'TestSmokeAppsForEnv|TestIdentity(HasGroup|TOTPWaitsForNextStep|PKCE)|TestGatewayRouteReady|TestGatewayNodeAddressFromInspect' ./...) | tail -1`
Expected: `check-network-config: 0 failures`, `mesh=0`, `ok`.

- [ ] **Step 2: Fresh `make dev`**

Run: `make dev-clean; make dev > .superpowers/sdd/2026-10-15-network-config/make-dev.log 2>&1; echo exit=$?; grep -E '^(--- FAIL|FAIL)|^DONE' .superpowers/sdd/2026-10-15-network-config/make-dev.log`
Expected: `exit=0`, `DONE 96 tests` (95 + `TestMonitoringControlPlaneTargets`), no FAIL.

- [ ] **Step 3: Argo CD generated everything from the matrix**

Run: `KUBECONFIG=metal/kubeconfig.yaml kubectl -n argocd get applications --no-headers | wc -l; KUBECONFIG=metal/kubeconfig.yaml kubectl -n argocd get applicationsets -o jsonpath='{range .items[*]}{.metadata.name} {.status.conditions[?(@.type=="ErrorOccurred")].status}{"\n"}{end}'`
Expected: `17`; `apps False`, `root False`.
