# Mesh Authorization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Inside the mesh nothing is reachable unless allowed: a mesh-wide deny-all, one `allow` AuthorizationPolicy per namespace (same namespace, the Gateways to route ports, Prometheus to metrics, named callers), NetworkPolicies for pods outside the mesh, and a render check that proves every route, monitor and webhook is covered.

**Architecture:** `system/istio-system` gets `AuthorizationPolicy` `deny-all` (`spec: {}`, root namespace) plus `allow-gateways` (the Istio Gateway pods accept everything; the deny-all would otherwise make them answer 403). Every chart and app file carries its own `allow` policy; opted-out pods get a `NetworkPolicy`. `scripts/check-mesh-exclusions` becomes `scripts/check-mesh-policies`, which derives the required allows from the renders (routes, ServiceMonitors/PodMonitors, webhooks, plus a short list of calls the renders cannot show) and fails on any gap.

**Tech Stack:** Istio 1.31.1 ambient (ztunnel L4 authorization), Kubernetes NetworkPolicy (k3s network policy controller), Helm wrapper charts and app-template `rawResources`, Python 3 + PyYAML, Go tests.

**Spec:** `docs/superpowers/specs/2026-10-15-mesh-authorization-design.md`

## Global Constraints

- Principals: LAN Gateway `cluster.local/ns/istio-system/sa/gateway-istio`; public Gateway `cluster.local/ns/istio-system/sa/public-istio`; Prometheus `cluster.local/ns/monitoring-system/sa/monitoring-system-kube-pro-prometheus`; Grafana `cluster.local/ns/grafana/sa/grafana`; Argo CD repo server `cluster.local/ns/argocd/sa/argocd-repo-server`.
- Ports in policies are **pod** ports (ztunnel matches after the Service), e.g. Forgejo SSH 2222.
- Every meshed namespace's policy is named `allow`; the same-namespace rule is always present.
- Egress is out of scope (step 6d).
- Branch `feat/mesh-authorization` from `dev`, PR into `dev`; ask before merging.
- Planning findings (2026-10-15, on the meshed sandbox, all spike resources removed afterwards):
  - A root-namespace `deny-all` alone made the Gateway itself answer **403** for every request (the Istio Gateway pods enforce root policies). Adding `allow-gateways` (selector `gateway.networking.k8s.io/gateway-class-name: istio`, `rules: [{}]`) let the Gateway through and ztunnel denied the backend (503, 108 `policy rejection` lines): the intended default deny. The spec is amended accordingly (Task 2).
  - `scripts/render-chart` rendered no ServiceMonitors (charts gate them on `monitoring.coreos.com/v1`, which Argo CD passes from the cluster); Task 1 adds those API versions.
  - A NetworkPolicy opening only trust-manager's webhook port kept its kubelet probes working.
  - The draft check, run on `dev`, failed with exactly the allows listed in Tasks 2–4 (148 failures across prod and sandbox renders, no unresolvable backends).

## Review Focus

1. In-cluster calls the renders cannot show (an app's config pointing at another namespace's Service). Expected: listed in `EXTRA`; any not listed breaks only that call. Check app configs for `.svc` or `<name>.<namespace>:` URLs. Covered in Task 1's `EXTRA` and Task 6's fresh `make dev`.
2. Operator-created pods with their own ports (Prometheus StatefulSet, Kanidm StatefulSet, CNPG instances): their callers are same-namespace or listed in `EXTRA`; anything else fails in the sandbox. Covered by Task 6.
3. The public Gateway principal for app files follows `global.publicGateway` (`public-istio` in production, `gateway-istio` in the sandbox). Pinned by Task 1's check over both renders.
4. Webhook and metrics ports of opted-out pods must stay open after chart upgrades change ports. Pinned by the check (it resolves the webhook Service and monitor ports from the render).
5. A namespace added later without an `allow` policy is fully closed (deny-all); the check fails on it before merge. Pinned by Task 1.

---

### Task 1: `scripts/check-mesh-policies` (the test)

**Files:**
- Create: `scripts/check-mesh-policies` (from `scripts/check-mesh-exclusions`, which is deleted)
- Modify: `scripts/render-chart` (monitoring API versions)

**Interfaces:**
- Produces: `scripts/check-mesh-policies` — exit 0 when every requirement holds; prints `ok   …` and `FAIL …` lines.

- [ ] **Step 1: `scripts/render-chart` passes the monitoring APIs**

After the `batch/v1/CronJob` line add:

```sh
# Charts that create Prometheus monitors only when the cluster serves them (kube-prometheus-stack's CRDs)
set -- "$@" --api-versions monitoring.coreos.com/v1
for kind in ServiceMonitor PodMonitor PrometheusRule; do
  set -- "$@" --api-versions "monitoring.coreos.com/v1/$kind"
done
```

Run: `scripts/render-chart system/argocd dev | yq ea '[select(.kind=="ServiceMonitor")] | length'`
Expected: `4` (it was `0`).

- [ ] **Step 2: Create the check**

`git mv scripts/check-mesh-exclusions scripts/check-mesh-policies`, then replace its content with:

```python
#!/usr/bin/env python3
"""Checks Istio ambient mesh policy in every component, rendered the way Argo CD does (production and sandbox).

- Every webhook and aggregated API the API server calls is outside the mesh (pods labelled
  istio.io/dataplane-mode=none, or a namespace outside the mesh): under strict mTLS it cannot reach meshed pods.
- NetworkPolicies in meshed namespaces let ztunnel's HBONE port (15008) in.
- Under the mesh-wide deny-all, every meshed namespace has an AuthorizationPolicy `allow`, and it lets in the
  Gateways to their route backends, Prometheus to its scrape targets, and the callers listed in EXTRA.
- Every pod outside the mesh in a meshed namespace has a NetworkPolicy that opens its webhook and metrics ports.
"""
import glob
import os
import subprocess
import sys
import time

import yaml

# Keep in step with the root ApplicationSet's templatePatch (system/argocd/values.yaml) and test/mesh_test.go
OUTSIDE = {"istio-system", "rook-ceph", "kube-system"}


class Loader(yaml.SafeLoader):
    """SafeLoader that reads a bare `=` (YAML 1.1's value tag, used by some charts) as a string."""


Loader.add_constructor("tag:yaml.org,2002:value", lambda loader, node: loader.construct_scalar(node))
WORKLOADS = {"Deployment", "DaemonSet", "StatefulSet"}


def render(target, env):
    # Chart downloads (GitHub Pages, the Istio repository) sometimes reset the connection: try three times
    for attempt in range(3):
        out = subprocess.run(["scripts/render-chart", target, env], capture_output=True, text=True)
        if out.returncode == 0:
            return [d for d in yaml.load_all(out.stdout, Loader=Loader) if d]
        time.sleep(5)
    sys.exit(f"render failed: {target} {env}\n{out.stderr}")


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


GATEWAYS = {
    "gateway": "cluster.local/ns/istio-system/sa/gateway-istio",
    "public": "cluster.local/ns/istio-system/sa/public-istio",
}
PROMETHEUS = "cluster.local/ns/monitoring-system/sa/monitoring-system-kube-pro-prometheus"
GRAFANA = "cluster.local/ns/grafana/sa/grafana"
# Calls the renders cannot show: (component, principal or None for plaintext from outside the mesh, pod port,
# only when this kind is rendered, why)
EXTRA = [
    ("platform/kanidm", GATEWAYS["gateway"], 8443, None, "kaniop creates Kanidm's HTTPRoute"),
    ("platform/forgejo", "cluster.local/ns/argocd/sa/argocd-repo-server", 3000, "Cluster",
     "production Argo CD syncs from Forgejo"),
    ("platform/forgejo", None, 8000, "Cluster", "the CloudNativePG operator polls forgejo-db's status port"),
    ("system/monitoring-system", GRAFANA, 9090, None, "Grafana's Prometheus data source"),
    ("system/loki", GRAFANA, 3100, None, "Grafana's Loki data source"),
]


def allows_hbone(policy):
    """A NetworkPolicy that lets every pod receive ztunnel's HBONE connections (TCP 15008) from anywhere."""
    spec = policy.get("spec") or {}
    if spec.get("podSelector") not in ({}, None):
        return False
    for rule in spec.get("ingress") or []:
        if rule.get("from"):
            continue
        for port in rule.get("ports") or []:
            if port.get("port") == 15008 and port.get("protocol", "TCP") == "TCP":
                return True
    return False


def labels_of(workload):
    return workload["spec"]["template"]["metadata"].get("labels") or {}


def selects(selector, labels):
    """Kubernetes label selector semantics: a plain map is matchLabels; matchExpressions supports In, NotIn,
    Exists and DoesNotExist. An empty selector selects nothing here (no chart relies on select-all)."""
    if not selector:
        return False
    if "matchLabels" not in selector and "matchExpressions" not in selector:
        selector = {"matchLabels": selector}
    for k, v in (selector.get("matchLabels") or {}).items():
        if labels.get(k) != v:
            return False
    for e in selector.get("matchExpressions") or []:
        op, key, values = e["operator"], e["key"], e.get("values") or []
        if (op == "In" and labels.get(key) not in values) or (op == "NotIn" and labels.get(key) in values) \
                or (op == "Exists" and key not in labels) or (op == "DoesNotExist" and key in labels):
            return False
    return bool(selector.get("matchLabels") or selector.get("matchExpressions"))


def pod_ports(workloads, selector, target):
    """(workload, pod port) pairs a Service selector and targetPort (number or name) resolve to."""
    out = []
    for w in workloads:
        if not selects(selector, labels_of(w)):
            continue
        if isinstance(target, int) or (isinstance(target, str) and target.isdigit()):
            out.append((w, int(target)))
            continue
        for c in w["spec"]["template"]["spec"].get("containers") or []:
            for p in c.get("ports") or []:
                if p.get("name") == target:
                    out.append((w, p["containerPort"]))
    return out


def service_pod_ports(svc, workloads, port=None, port_name=None, target=None):
    """(workload, pod port) pairs behind a Service port (by number or name) or a monitor's targetPort."""
    selector = (svc.get("spec") or {}).get("selector") or {}
    if target is not None:
        return pod_ports(workloads, selector, target)
    out = []
    for p in (svc.get("spec") or {}).get("ports") or []:
        if (port is not None and p.get("port") == port) or (port_name is not None and p.get("name") == port_name):
            out += pod_ports(workloads, selector, p.get("targetPort", p.get("port")))
    return out


def allowed(policies, principal, port):
    """Whether an ALLOW policy lets `principal` (None: plaintext from outside the mesh) reach `port`."""
    for pol in policies:
        spec = pol.get("spec") or {}
        if spec.get("action", "ALLOW") != "ALLOW":
            continue
        for rule in spec.get("rules") or []:
            sources = [f.get("source") or {} for f in rule.get("from") or []]
            if sources:
                ns = principal.split("/ns/")[1].split("/")[0] if principal else None
                ok = principal is not None and any(principal in (s.get("principals") or []) or ns in (s.get("namespaces") or []) for s in sources)
            else:
                ok = True
            ports = [str(p) for t in rule.get("to") or [] for p in ((t.get("operation") or {}).get("ports") or [])]
            if ok and (not ports or str(port) in ports):
                return True
    return False


def opened(netpols, workload, ns_of, port, from_namespace):
    """Whether a NetworkPolicy selecting `workload` lets `port` in from `from_namespace` (None: from anywhere)."""
    for n in netpols:
        spec = n.get("spec") or {}
        if ns_of(n) != ns_of(workload) or not selects(spec.get("podSelector") or {}, labels_of(workload)):
            continue
        for rule in spec.get("ingress") or []:
            sources = rule.get("from") or []
            source_ok = not sources or (from_namespace is not None and any(
                selects((f.get("namespaceSelector") or {}), {"kubernetes.io/metadata.name": from_namespace}) for f in sources))
            ports = [str(p.get("port")) for p in rule.get("ports") or []]
            if source_ok and (not ports or str(port) in ports):
                return True
    return False


def host_network(workload):
    return bool(workload["spec"]["template"]["spec"].get("hostNetwork"))


def outside(workload, ns_of):
    return ns_of(workload) in OUTSIDE or labels_of(workload).get("istio.io/dataplane-mode") == "none" or host_network(workload)


def check_policies(target, env, namespace, docs, ns_of, failures):
    workloads = [d for d in docs if d.get("kind") in WORKLOADS]
    services = {(ns_of(d), d["metadata"]["name"]): d for d in docs if d.get("kind") == "Service"}
    policies = [d for d in docs if d.get("kind") == "AuthorizationPolicy"]
    netpols = [d for d in docs if d.get("kind") == "NetworkPolicy"]
    meshed = {ns_of(w) for w in workloads if not outside(w, ns_of)}
    for ns in sorted(meshed):
        if not any(p["metadata"]["name"] == "allow" and ns_of(p) == ns for p in policies):
            failures.append(f"{target} ({env}): no AuthorizationPolicy allow in {ns}")
    needs = []  # (workload or None, namespace, principal, pod port, why)
    for route in (d for d in docs if d.get("kind") in ("HTTPRoute", "TCPRoute", "GRPCRoute")):
        for parent in route["spec"].get("parentRefs") or []:
            principal = GATEWAYS.get(parent.get("name"))
            for rule in route["spec"].get("rules") or [] if principal else []:
                for ref in rule.get("backendRefs") or []:
                    svc = services.get((ref.get("namespace") or ns_of(route), ref["name"]))
                    found = service_pod_ports(svc, workloads, port=ref.get("port")) if svc else []
                    if not found:
                        failures.append(f"{target} ({env}): route {route['metadata']['name']} backend {ref['name']}:{ref.get('port')} does not resolve to a pod port")
                    needs += [(w, ns_of(w), principal, p, f"route {route['metadata']['name']}") for w, p in found]
    for mon in (d for d in docs if d.get("kind") in ("ServiceMonitor", "PodMonitor")):
        spec = mon.get("spec") or {}
        names = (spec.get("namespaceSelector") or {}).get("matchNames") or [ns_of(mon)]
        why = f"monitor {mon['metadata']['name']}"
        if mon["kind"] == "ServiceMonitor":
            for (sns, _), svc in services.items():
                if sns in names and selects(spec.get("selector") or {}, svc["metadata"].get("labels") or {}):
                    for ep in spec.get("endpoints") or []:
                        found = service_pod_ports(svc, workloads, port_name=ep.get("port")) if ep.get("port") else service_pod_ports(svc, workloads, target=ep.get("targetPort"))
                        needs += [(w, sns, PROMETHEUS, p, why) for w, p in found]
        else:
            for w in workloads:
                if ns_of(w) in names and selects(spec.get("selector") or {}, labels_of(w)):
                    for ep in spec.get("podMetricsEndpoints") or []:
                        needs += [(w, ns_of(w), PROMETHEUS, p, why) for _, p in pod_ports([w], labels_of(w), ep.get("port") or ep.get("targetPort"))]
    for component, principal, port, kind, why in EXTRA:
        if component == target and (kind is None or any(d.get("kind") == kind for d in docs)):
            needs.append((None, namespace, principal, port, why))
    for hook in (s for d in docs for s in webhook_services(d)):
        svc = services.get((hook.get("namespace") or namespace, hook["name"]))
        for w, p in (service_pod_ports(svc, workloads, port=hook.get("port", 443)) if svc else []):
            needs.append((w, ns_of(w), "apiserver", p, f"webhook {hook['name']}"))
    for w, ns, principal, port, why in sorted({(id(n[0]), n[1:]): n for n in needs}.values(), key=lambda n: str(n[1:])):
        who = principal or "plaintext (outside the mesh)"
        if ns in OUTSIDE:
            continue
        if w is not None and host_network(w):
            continue  # neither the mesh nor NetworkPolicies apply to the node's network
        if w is not None and outside(w, ns_of):
            # Outside the mesh: the NetworkPolicy opens it (webhooks from anywhere, scrapes from monitoring-system)
            source = None if principal in ("apiserver", None) else principal.split("/ns/")[1].split("/")[0]
            ok = opened(netpols, w, ns_of, port, source)
            where = f"NetworkPolicy of {ns}/{w['metadata']['name']}"
        else:
            if principal == "apiserver":
                continue  # meshed webhook targets are reported by main()
            ok = allowed([p for p in policies if ns_of(p) == ns], principal, port)
            where = f"AuthorizationPolicy in {ns}"
        line = f"{target} ({env}): {where} port {port} for {who} ({why})"
        if ok:
            print(f"ok   {line}")
        else:
            failures.append(f"{line}: not allowed")
    for w in workloads:
        if ns_of(w) not in OUTSIDE and outside(w, ns_of) and not host_network(w) and not any(
                ns_of(n) == ns_of(w) and selects((n.get("spec") or {}).get("podSelector") or {}, labels_of(w)) for n in netpols):
            failures.append(f"{target} ({env}): {ns_of(w)}/{w['metadata']['name']} is outside the mesh without a NetworkPolicy")


def main():
    failures = []
    for target in targets():
        namespace = os.path.basename(target).removesuffix(".yaml")
        for env in ("prod", "dev"):
            docs = render(target, env)
            ns_of = lambda d: (d.get("metadata") or {}).get("namespace") or namespace
            # In the mesh, pods receive traffic on ztunnel's HBONE port, which a chart's NetworkPolicies must let in
            policies = [d for d in docs if d.get("kind") == "NetworkPolicy" and ns_of(d) not in OUTSIDE
                        and "Ingress" in ((d.get("spec") or {}).get("policyTypes") or ["Ingress"])]
            for ns in sorted({ns_of(d) for d in policies}):
                if any(allows_hbone(d) for d in policies if ns_of(d) == ns):
                    print(f"ok   {target} ({env}): NetworkPolicies in {ns} let HBONE (15008) in")
                else:
                    failures.append(f"{target} ({env}): NetworkPolicies in {ns} block ztunnel's HBONE port 15008")
            check_policies(target, env, namespace, docs, ns_of, failures)
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

- [ ] **Step 3: Run it (must fail with the planned gaps)**

Run: `scripts/check-mesh-policies > .superpowers/sdd/2026-10-15-mesh-authorization/red.log; echo "exit=$?"; grep -c '^FAIL' .superpowers/sdd/2026-10-15-mesh-authorization/red.log; grep -c 'does not resolve\|render failed\|Traceback' .superpowers/sdd/2026-10-15-mesh-authorization/red.log`
Expected: `exit=1`, about 148 FAIL lines (every one a missing `allow`, a not-allowed port, or a pod outside the mesh without a NetworkPolicy), and `0` for the last count.

- [ ] **Step 4: Commit**

```bash
git add scripts/check-mesh-policies scripts/check-mesh-exclusions scripts/render-chart
git commit -m "test: scripts/check-mesh-policies derives the mesh's required allows from the renders"
```

### Task 2: Default deny (`system/istio-system`)

**Files:**
- Create: `system/istio-system/templates/deny-all.yaml`
- Modify: `docs/superpowers/specs/2026-10-15-mesh-authorization-design.md` (the `allow-gateways` amendment)

- [ ] **Step 1: Write `deny-all.yaml`**

```yaml
# Mesh-wide default deny (root namespace): a meshed pod accepts a connection only when an AuthorizationPolicy
# allows it (each namespace's `allow`). Kubelet probes are exempt in ambient.
apiVersion: security.istio.io/v1
kind: AuthorizationPolicy
metadata:
  name: deny-all
spec: {}
---
# The Gateways enforce root-namespace policies too: without this they would answer 403 to every client.
# They stay reachable from anywhere; the backends' `allow` policies decide what passes through them.
apiVersion: security.istio.io/v1
kind: AuthorizationPolicy
metadata:
  name: allow-gateways
spec:
  selector:
    matchLabels:
      gateway.networking.k8s.io/gateway-class-name: istio
  rules:
    - {}
```

- [ ] **Step 2: Amend the spec**

In the spec's "Default deny" section append: "The Istio Gateway pods enforce root-namespace policies as well, so the same template adds `allow-gateways` (selector `gateway.networking.k8s.io/gateway-class-name: istio`, `rules: [{}]`); without it the Gateways answer 403 to every client (found while planning, 2026-10-15)."

- [ ] **Step 3: Check the render**

Run: `scripts/render-chart system/istio-system | yq ea '[select(.kind=="AuthorizationPolicy") | .metadata.name] | join(",")'`
Expected: `deny-all,allow-gateways`.

- [ ] **Step 4: Commit**

```bash
git add system/istio-system docs/superpowers/specs/2026-10-15-mesh-authorization-design.md
git commit -m "feat(istio-system): mesh-wide default deny; the Gateways stay open"
```

### Task 3: `allow` policies and NetworkPolicies in `system/` and `platform/`

**Files:** one `templates/authorization-policy.yaml` per chart below, plus the NetworkPolicies listed.

- [ ] **Step 1: Write the policies**

`system/argocd/templates/authorization-policy.yaml`:

```yaml
# Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
apiVersion: security.istio.io/v1
kind: AuthorizationPolicy
metadata:
  name: allow
  namespace: {{ .Release.Namespace }}
spec:
  action: ALLOW
  rules:
    # Pods in this namespace call each other freely
    - from:
        - source:
            namespaces: ["{{ .Release.Namespace }}"]
    # Gateway: the web UI
    - from:
        - source:
            principals: ["cluster.local/ns/istio-system/sa/gateway-istio"]
      to:
        - operation:
            ports: ["8080"]
    # Prometheus: application controller, server and repo server metrics
    - from:
        - source:
            principals: ["cluster.local/ns/monitoring-system/sa/monitoring-system-kube-pro-prometheus"]
      to:
        - operation:
            ports: ["8082", "8083", "8084"]
```

`system/cert-manager/templates/authorization-policy.yaml`:

```yaml
# Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
apiVersion: security.istio.io/v1
kind: AuthorizationPolicy
metadata:
  name: allow
  namespace: {{ .Release.Namespace }}
spec:
  action: ALLOW
  rules:
    # Pods in this namespace call each other freely
    - from:
        - source:
            namespaces: ["{{ .Release.Namespace }}"]
    # Prometheus: the controller's and cainjector's metrics
    - from:
        - source:
            principals: ["cluster.local/ns/monitoring-system/sa/monitoring-system-kube-pro-prometheus"]
      to:
        - operation:
            ports: ["9402"]
```

`system/cloudflared/templates/authorization-policy.yaml`:

```yaml
# Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
apiVersion: security.istio.io/v1
kind: AuthorizationPolicy
metadata:
  name: allow
  namespace: {{ .Release.Namespace }}
spec:
  action: ALLOW
  rules:
    # Pods in this namespace call each other freely
    - from:
        - source:
            namespaces: ["{{ .Release.Namespace }}"]
```

`system/external-dns/templates/authorization-policy.yaml`:

```yaml
# Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
apiVersion: security.istio.io/v1
kind: AuthorizationPolicy
metadata:
  name: allow
  namespace: {{ .Release.Namespace }}
spec:
  action: ALLOW
  rules:
    # Pods in this namespace call each other freely
    - from:
        - source:
            namespaces: ["{{ .Release.Namespace }}"]
```

`system/kured/templates/authorization-policy.yaml`:

```yaml
# Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
apiVersion: security.istio.io/v1
kind: AuthorizationPolicy
metadata:
  name: allow
  namespace: {{ .Release.Namespace }}
spec:
  action: ALLOW
  rules:
    # Pods in this namespace call each other freely
    - from:
        - source:
            namespaces: ["{{ .Release.Namespace }}"]
```

`system/loki/templates/authorization-policy.yaml`:

```yaml
# Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
apiVersion: security.istio.io/v1
kind: AuthorizationPolicy
metadata:
  name: allow
  namespace: {{ .Release.Namespace }}
spec:
  action: ALLOW
  rules:
    # Pods in this namespace call each other freely
    - from:
        - source:
            namespaces: ["{{ .Release.Namespace }}"]
    # Grafana's Loki data source
    - from:
        - source:
            principals: ["cluster.local/ns/grafana/sa/grafana"]
      to:
        - operation:
            ports: ["3100"]
    # Prometheus: Loki's metrics
    - from:
        - source:
            principals: ["cluster.local/ns/monitoring-system/sa/monitoring-system-kube-pro-prometheus"]
      to:
        - operation:
            ports: ["3100"]
```

`system/monitoring-system/templates/authorization-policy.yaml`:

```yaml
# Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
apiVersion: security.istio.io/v1
kind: AuthorizationPolicy
metadata:
  name: allow
  namespace: {{ .Release.Namespace }}
spec:
  action: ALLOW
  rules:
    # Pods in this namespace call each other freely
    - from:
        - source:
            namespaces: ["{{ .Release.Namespace }}"]
    # Grafana's Prometheus data source
    - from:
        - source:
            principals: ["cluster.local/ns/grafana/sa/grafana"]
      to:
        - operation:
            ports: ["9090"]
```

`system/volsync-system/templates/authorization-policy.yaml`:

```yaml
# Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
apiVersion: security.istio.io/v1
kind: AuthorizationPolicy
metadata:
  name: allow
  namespace: {{ .Release.Namespace }}
spec:
  action: ALLOW
  rules:
    # Pods in this namespace call each other freely
    - from:
        - source:
            namespaces: ["{{ .Release.Namespace }}"]
    # Prometheus: VolSync's metrics
    - from:
        - source:
            principals: ["cluster.local/ns/monitoring-system/sa/monitoring-system-kube-pro-prometheus"]
      to:
        - operation:
            ports: ["8443"]
```

`platform/external-secrets/templates/authorization-policy.yaml`:

```yaml
# Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
apiVersion: security.istio.io/v1
kind: AuthorizationPolicy
metadata:
  name: allow
  namespace: {{ .Release.Namespace }}
spec:
  action: ALLOW
  rules:
    # Pods in this namespace call each other freely
    - from:
        - source:
            namespaces: ["{{ .Release.Namespace }}"]
```

`platform/forgejo/templates/authorization-policy.yaml`:

```yaml
# Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
apiVersion: security.istio.io/v1
kind: AuthorizationPolicy
metadata:
  name: allow
  namespace: {{ .Release.Namespace }}
spec:
  action: ALLOW
  rules:
    # Pods in this namespace call each other freely
    - from:
        - source:
            namespaces: ["{{ .Release.Namespace }}"]
    # Gateway: web (3000) and SSH (2222, the rootless image's port behind Service port 22)
    - from:
        - source:
            principals: ["cluster.local/ns/istio-system/sa/gateway-istio"]
      to:
        - operation:
            ports: ["3000", "2222"]
    # Production Argo CD syncs ops/homelab from Forgejo
    - from:
        - source:
            principals: ["cluster.local/ns/argocd/sa/argocd-repo-server"]
      to:
        - operation:
            ports: ["3000"]
{{- if and .Values.database.cnpg .Values.database.cnpg.enabled }}
---
# The CloudNativePG operator (outside the mesh, so no identity) polls each instance's status port
apiVersion: security.istio.io/v1
kind: AuthorizationPolicy
metadata:
  name: allow-cnpg-status
  namespace: {{ .Release.Namespace }}
spec:
  action: ALLOW
  selector:
    matchLabels:
      cnpg.io/cluster: forgejo-db
  rules:
    - to:
        - operation:
            ports: ["8000"]
{{- end }}
```

`platform/grafana/templates/authorization-policy.yaml`:

```yaml
# Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
apiVersion: security.istio.io/v1
kind: AuthorizationPolicy
metadata:
  name: allow
  namespace: {{ .Release.Namespace }}
spec:
  action: ALLOW
  rules:
    # Pods in this namespace call each other freely
    - from:
        - source:
            namespaces: ["{{ .Release.Namespace }}"]
    # Gateway: the web UI
    - from:
        - source:
            principals: ["cluster.local/ns/istio-system/sa/gateway-istio"]
      to:
        - operation:
            ports: ["3000"]
```

`platform/kanidm/templates/authorization-policy.yaml`:

```yaml
# Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
apiVersion: security.istio.io/v1
kind: AuthorizationPolicy
metadata:
  name: allow
  namespace: {{ .Release.Namespace }}
spec:
  action: ALLOW
  rules:
    # Pods in this namespace call each other freely
    - from:
        - source:
            namespaces: ["{{ .Release.Namespace }}"]
    # Gateway: kaniop's HTTPRoute (re-encrypted to Kanidm's own TLS)
    - from:
        - source:
            principals: ["cluster.local/ns/istio-system/sa/gateway-istio"]
      to:
        - operation:
            ports: ["8443"]
```

`platform/woodpecker/templates/authorization-policy.yaml`:

```yaml
# Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
apiVersion: security.istio.io/v1
kind: AuthorizationPolicy
metadata:
  name: allow
  namespace: {{ .Release.Namespace }}
spec:
  action: ALLOW
  rules:
    # Pods in this namespace call each other freely
    - from:
        - source:
            namespaces: ["{{ .Release.Namespace }}"]
    # Gateway: the web UI and API
    - from:
        - source:
            principals: ["cluster.local/ns/istio-system/sa/gateway-istio"]
      to:
        - operation:
            ports: ["8000"]
```

`platform/zot/templates/authorization-policy.yaml`:

```yaml
# Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
apiVersion: security.istio.io/v1
kind: AuthorizationPolicy
metadata:
  name: allow
  namespace: {{ .Release.Namespace }}
spec:
  action: ALLOW
  rules:
    # Pods in this namespace call each other freely
    - from:
        - source:
            namespaces: ["{{ .Release.Namespace }}"]
    # Gateway: the registry
    - from:
        - source:
            principals: ["cluster.local/ns/istio-system/sa/gateway-istio"]
      to:
        - operation:
            ports: ["5000"]
```

NetworkPolicies for the pods outside the mesh:

`system/cert-manager/templates/network-policy.yaml`:

```yaml
# Outside the mesh (the API server calls it): only its webhook and metrics ports
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: cert-manager-webhook
  namespace: {{ .Release.Namespace }}
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: webhook
      app.kubernetes.io/instance: cert-manager
  policyTypes:
    - Ingress
  ingress:
    # From anywhere: the API server calls from the nodes' addresses
    - ports:
        - protocol: TCP
          port: 10250
    # Prometheus scrapes its metrics
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: monitoring-system
      ports:
        - protocol: TCP
          port: 9402
---
{{- if (index .Values "trust-manager").enabled }}
# Outside the mesh (the API server calls it): only its webhook port
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: trust-manager
  namespace: {{ .Release.Namespace }}
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: trust-manager
      app.kubernetes.io/instance: cert-manager
  policyTypes:
    - Ingress
  ingress:
    # From anywhere: the API server calls from the nodes' addresses
    - ports:
        - protocol: TCP
          port: 6443
{{- end }}
```

`platform/external-secrets/templates/network-policy.yaml`:

```yaml
# Outside the mesh (the API server calls it): only its webhook port
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: external-secrets-webhook
  namespace: {{ .Release.Namespace }}
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: external-secrets-webhook
      app.kubernetes.io/instance: external-secrets
  policyTypes:
    - Ingress
  ingress:
    # From anywhere: the API server calls from the nodes' addresses
    - ports:
        - protocol: TCP
          port: 10250
```

`system/monitoring-system/templates/network-policy.yaml`:

```yaml
# Outside the mesh (the API server calls its admission webhook): only its HTTPS port (webhook and metrics)
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: prometheus-operator
  namespace: {{ .Release.Namespace }}
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: kube-prometheus-stack-prometheus-operator
      app.kubernetes.io/instance: monitoring-system
  policyTypes:
    - Ingress
  ingress:
    # From anywhere: the API server calls from the nodes' addresses
    - ports:
        - protocol: TCP
          port: 10250
```

`platform/cloudnative-pg/templates/network-policy.yaml`:

```yaml
# Outside the mesh (the API server calls its webhooks): only its webhook and metrics ports
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: cloudnative-pg
  namespace: {{ .Release.Namespace }}
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: cloudnative-pg
      app.kubernetes.io/instance: cloudnative-pg
  policyTypes:
    - Ingress
  ingress:
    # From anywhere: the API server calls from the nodes' addresses
    - ports:
        - protocol: TCP
          port: 9443
    # Prometheus scrapes its metrics
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: monitoring-system
      ports:
        - protocol: TCP
          port: 8080
```

- [ ] **Step 2: Run the check for these charts**

Run: `scripts/check-mesh-policies > .superpowers/sdd/2026-10-15-mesh-authorization/t3.log; grep '^FAIL' .superpowers/sdd/2026-10-15-mesh-authorization/t3.log | grep -v '^FAIL apps/'`
Expected: no output (only `apps/` lines remain). A remaining line is a port or selector this task got wrong: fix it from the line's text (it names the namespace, port and caller) and record a ruling.

- [ ] **Step 3: Commit**

```bash
git add system platform
git commit -m "feat: allow policies for system and platform; NetworkPolicies for the pods outside the mesh"
```

### Task 4: `allow` policies for the apps

**Files:** `apps/*.yaml` (`rawResources`), `apps/matrix/templates/authorization-policy.yaml`.

- [ ] **Step 1: Add the policies**

Append under `rawResources:` (create the key at the end of the file if the app has none):

`apps/actualbudget.yaml`:

```yaml
  # Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
  authorization-policy:
    forceRename: allow
    manifest:
      apiVersion: security.istio.io/v1
      kind: AuthorizationPolicy
      spec:
        action: ALLOW
        rules:
          # Pods in this namespace call each other freely
          - from:
              - source:
                  namespaces: ['{{ .Release.Namespace }}']
          # the LAN Gateway
          - from:
              - source:
                  principals: ['cluster.local/ns/istio-system/sa/gateway-istio']
            to:
              - operation:
                  ports: ["5006"]
```

`apps/homepage.yaml`:

```yaml
  # Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
  authorization-policy:
    forceRename: allow
    manifest:
      apiVersion: security.istio.io/v1
      kind: AuthorizationPolicy
      spec:
        action: ALLOW
        rules:
          # Pods in this namespace call each other freely
          - from:
              - source:
                  namespaces: ['{{ .Release.Namespace }}']
          # the LAN Gateway
          - from:
              - source:
                  principals: ['cluster.local/ns/istio-system/sa/gateway-istio']
            to:
              - operation:
                  ports: ["3000"]
```

`apps/jellyfin.yaml`:

```yaml
  # Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
  authorization-policy:
    forceRename: allow
    manifest:
      apiVersion: security.istio.io/v1
      kind: AuthorizationPolicy
      spec:
        action: ALLOW
        rules:
          # Pods in this namespace call each other freely
          - from:
              - source:
                  namespaces: ['{{ .Release.Namespace }}']
          # the LAN Gateway
          - from:
              - source:
                  principals: ['cluster.local/ns/istio-system/sa/gateway-istio']
            to:
              - operation:
                  ports: ["8096", "5055", "9696", "7878", "8989", "9091"]
```

`apps/ollama.yaml`:

```yaml
  # Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
  authorization-policy:
    forceRename: allow
    manifest:
      apiVersion: security.istio.io/v1
      kind: AuthorizationPolicy
      spec:
        action: ALLOW
        rules:
          # Pods in this namespace call each other freely
          - from:
              - source:
                  namespaces: ['{{ .Release.Namespace }}']
          # the LAN Gateway
          - from:
              - source:
                  principals: ['cluster.local/ns/istio-system/sa/gateway-istio']
            to:
              - operation:
                  ports: ["11434", "8080"]
```

`apps/opencloud.yaml`:

```yaml
  # Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
  authorization-policy:
    forceRename: allow
    manifest:
      apiVersion: security.istio.io/v1
      kind: AuthorizationPolicy
      spec:
        action: ALLOW
        rules:
          # Pods in this namespace call each other freely
          - from:
              - source:
                  namespaces: ['{{ .Release.Namespace }}']
          # the LAN Gateway
          - from:
              - source:
                  principals: ['cluster.local/ns/istio-system/sa/gateway-istio']
            to:
              - operation:
                  ports: ["9200"]
```

`apps/pairdrop.yaml`:

```yaml
  # Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
  authorization-policy:
    forceRename: allow
    manifest:
      apiVersion: security.istio.io/v1
      kind: AuthorizationPolicy
      spec:
        action: ALLOW
        rules:
          # Pods in this namespace call each other freely
          - from:
              - source:
                  namespaces: ['{{ .Release.Namespace }}']
          # the LAN Gateway
          - from:
              - source:
                  principals: ['cluster.local/ns/istio-system/sa/gateway-istio']
            to:
              - operation:
                  ports: ["3000"]
```

`apps/paperless.yaml`:

```yaml
  # Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
  authorization-policy:
    forceRename: allow
    manifest:
      apiVersion: security.istio.io/v1
      kind: AuthorizationPolicy
      spec:
        action: ALLOW
        rules:
          # Pods in this namespace call each other freely
          - from:
              - source:
                  namespaces: ['{{ .Release.Namespace }}']
          # the LAN Gateway
          - from:
              - source:
                  principals: ['cluster.local/ns/istio-system/sa/gateway-istio']
            to:
              - operation:
                  ports: ["8000"]
```

`apps/speedtest.yaml`:

```yaml
  # Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
  authorization-policy:
    forceRename: allow
    manifest:
      apiVersion: security.istio.io/v1
      kind: AuthorizationPolicy
      spec:
        action: ALLOW
        rules:
          # Pods in this namespace call each other freely
          - from:
              - source:
                  namespaces: ['{{ .Release.Namespace }}']
          # the LAN Gateway
          - from:
              - source:
                  principals: ['cluster.local/ns/istio-system/sa/gateway-istio']
            to:
              - operation:
                  ports: ["3000"]
```

`apps/blog.yaml`:

```yaml
  # Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
  authorization-policy:
    forceRename: allow
    manifest:
      apiVersion: security.istio.io/v1
      kind: AuthorizationPolicy
      spec:
        action: ALLOW
        rules:
          # Pods in this namespace call each other freely
          - from:
              - source:
                  namespaces: ['{{ .Release.Namespace }}']
          # the Gateway named by global.publicGateway (public in production, gateway in the sandbox)
          - from:
              - source:
                  principals: ['cluster.local/ns/istio-system/sa/{{ .Values.global.publicGateway }}-istio']
            to:
              - operation:
                  ports: ["3000"]
```

`apps/excalidraw.yaml`:

```yaml
  # Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
  authorization-policy:
    forceRename: allow
    manifest:
      apiVersion: security.istio.io/v1
      kind: AuthorizationPolicy
      spec:
        action: ALLOW
        rules:
          # Pods in this namespace call each other freely
          - from:
              - source:
                  namespaces: ['{{ .Release.Namespace }}']
          # the Gateway named by global.publicGateway (public in production, gateway in the sandbox)
          - from:
              - source:
                  principals: ['cluster.local/ns/istio-system/sa/{{ .Values.global.publicGateway }}-istio']
            to:
              - operation:
                  ports: ["80"]
```

`apps/wireguard.yaml`:

```yaml
  # Outside the mesh (a router): only its tunnel port
  network-policy:
    forceRename: wireguard
    manifest:
      apiVersion: networking.k8s.io/v1
      kind: NetworkPolicy
      spec:
        podSelector:
          matchLabels:
            app.kubernetes.io/name: wireguard
        policyTypes:
          - Ingress
        ingress:
          - ports:
              - protocol: UDP
                port: 51820
```

`apps/tailscale.yaml`:

```yaml
  # Outside the mesh (a router): only its direct-connection port (peers fall back to DERP relays without it)
  network-policy:
    forceRename: tailscale
    manifest:
      apiVersion: networking.k8s.io/v1
      kind: NetworkPolicy
      spec:
        podSelector:
          matchLabels:
            app.kubernetes.io/name: tailscale
        policyTypes:
          - Ingress
        ingress:
          - ports:
              - protocol: UDP
                port: 41641
```

`apps/matrix/templates/authorization-policy.yaml`:

```yaml
# Under the mesh-wide deny-all (system/istio-system): who may call into this namespace
apiVersion: security.istio.io/v1
kind: AuthorizationPolicy
metadata:
  name: allow
  namespace: {{ .Release.Namespace }}
spec:
  action: ALLOW
  rules:
    # Pods in this namespace call each other freely
    - from:
        - source:
            namespaces: ["{{ .Release.Namespace }}"]
    # LAN Gateway: dendrite's client and federation API
    - from:
        - source:
            principals: ["cluster.local/ns/istio-system/sa/gateway-istio"]
      to:
        - operation:
            ports: ["8008"]
    # Public Gateway: Element
    - from:
        - source:
            principals: ["cluster.local/ns/istio-system/sa/public-istio"]
      to:
        - operation:
            ports: ["80"]
```

- [ ] **Step 2: Run the whole check**

Run: `scripts/check-mesh-policies > .superpowers/sdd/2026-10-15-mesh-authorization/t4.log; echo "exit=$?"; grep -c '^ok' .superpowers/sdd/2026-10-15-mesh-authorization/t4.log; grep '^FAIL' .superpowers/sdd/2026-10-15-mesh-authorization/t4.log`
Expected: `exit=0`, the `ok` count, no FAIL lines. Fix any FAIL from its text and record a ruling.

- [ ] **Step 3: Commit**

```bash
git add apps
git commit -m "feat(apps): allow policies; NetworkPolicies for the VPN routers"
```

### Task 5: Mesh tests

**Files:**
- Modify: `test/mesh_test.go`

**Interfaces:**
- Consumes: `meshCurl(t, name, target, labels string) string` (existing), plus a namespace parameter added here.
- Produces: `meshCurlIn(t, namespace, name, target, labels string) string`; `TestMeshAllowsSameNamespace`, `TestMeshPrometheusTargetsUp`; `TestMeshRejectsPlaintext` expecting the meshed pod in `default` to be refused.

- [ ] **Step 1: Write the tests**

In `test/mesh_test.go`:

- Rename `meshCurl(t, name, target, labels)` to `meshCurlIn(t, namespace, name, target, labels)`: `k8s.NewKubectlOptions("", "", namespace)` instead of `"default"`; update the two calls in `TestMeshRejectsPlaintext` to pass `"default"`.
- In `TestMeshRejectsPlaintext`, replace

  ```go
	assert.Equal(t, "200", meshCurl(t, "mesh-member", target, "istio.io/dataplane-mode=ambient"))
  ```

  with

  ```go
	// In the mesh but not allowed into excalidraw (default deny): refused after the mTLS handshake
	assert.Equal(t, "000", meshCurlIn(t, "default", "mesh-member", target, "istio.io/dataplane-mode=ambient"))
  ```

  and replace the final assertion block (the `src.identity`/`dst.identity` check) with:

  ```go
	assert.Contains(t, logs, `src.identity="spiffe://cluster.local/ns/default/sa/default"`,
		"ztunnel did not authenticate the meshed pod (mTLS)")
	assert.Regexp(t, `src.identity="spiffe://cluster.local/ns/default/sa/default"[^\n]*policy rejection|policy rejection[^\n]*src.identity="spiffe://cluster.local/ns/default/sa/default"`, logs,
		"ztunnel did not refuse the meshed pod by policy")
  ```

- Add:

  ```go
// TestMeshAllowsSameNamespace proves the `allow` policies' same-namespace rule: a meshed pod inside excalidraw
// reaches excalidraw.
func TestMeshAllowsSameNamespace(t *testing.T) {
	meshSandboxOnly(t)
	assert.Equal(t, "200", meshCurlIn(t, "excalidraw", "mesh-neighbour", "http://excalidraw.excalidraw", ""))
}

// TestMeshPrometheusTargetsUp proves the allows for Prometheus: no scrape target is down.
func TestMeshPrometheusTargetsUp(t *testing.T) {
	meshSandboxOnly(t)
	options := k8s.NewKubectlOptions("", "", "monitoring-system")
	options.Logger = logger.Discard
	query := "/api/v1/namespaces/monitoring-system/services/monitoring-system-kube-pro-prometheus:9090/proxy/api/v1/query?query=up%3D%3D0"
	assert.Eventually(t, func() bool {
		out, err := k8s.RunKubectlAndGetOutputE(t, options, "get", "--raw", query)
		if err != nil {
			return false
		}
		var res struct {
			Data struct {
				Result []struct {
					Metric map[string]string `json:"metric"`
				} `json:"result"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(out), &res) != nil {
			return false
		}
		for _, r := range res.Data.Result {
			t.Logf("target down: %v", r.Metric)
		}
		return len(res.Data.Result) == 0
	}, 5*time.Minute, 30*time.Second, "Prometheus has targets down")
}
  ```

  (add `"encoding/json"` to the imports).

- [ ] **Step 2: Run them against the current sandbox (no policies yet: must fail where the default deny is expected)**

Run: `make env=dev ENV=dev INSECURE_SKIP_VERIFY=1 smoke_filter='TestMesh' smoke-test 2>&1 | grep -E '^(PASS|FAIL) TestMesh[A-Za-z]+ |^DONE'`
Expected: `FAIL TestMeshRejectsPlaintext` (the meshed pod still gets 200: no deny-all yet), `PASS` for the others.

- [ ] **Step 3: Offline checks and commit**

Run: `cd test && go vet ./... && go test -run 'TestMeshOptOutAllowed|TestSmokeAppsForEnv' ./...`
Expected: `ok`.

```bash
git add test/mesh_test.go
git commit -m "test: the mesh's default deny, same-namespace allow and Prometheus targets"
```

### Task 6: Docs

**Files:** `docs/reference/architecture/networking.md`, `CLAUDE.md`, `docs/installation/sandbox.md` (if it names the check).

- [ ] **Step 1: Write**

- `networking.md`, at the end of "Service mesh", add a paragraph: "Inside the mesh nothing is reachable unless allowed: `deny-all` in `istio-system` (the Gateways keep `allow-gateways`) and one `AuthorizationPolicy` `allow` per namespace, in the namespace's chart or app file: pods of the same namespace, the Gateway its routes attach to (on the pod ports behind them), Prometheus on the metrics ports, and a few named callers (Grafana to Prometheus and Loki, Argo CD's repo server to Forgejo, CloudNativePG's operator to its instances). Pods outside the mesh get a NetworkPolicy that opens only their webhook, VPN or metrics ports. A new app needs its `allow` policy; `scripts/check-mesh-policies` fails without it. Outbound traffic is not restricted yet."
- `CLAUDE.md`: roadmap item 6 `...; 6b Istio ambient mesh with strict mTLS (done); 6c default-deny AuthorizationPolicy and NetworkPolicy with explicit allows` → `...; 6b ... (done); 6c default-deny ingress: AuthorizationPolicy per namespace, NetworkPolicy for pods outside the mesh (done); 6d egress`. Replace every `scripts/check-mesh-exclusions` with `scripts/check-mesh-policies`. In the mesh bullet add: "Every meshed namespace needs an `AuthorizationPolicy` `allow` (same namespace, its Gateway on the route pod ports, Prometheus on metrics, named callers); add in-cluster calls the renders cannot show to `EXTRA` in `scripts/check-mesh-policies`." In "Before the first production deploy" add: "The production-only allows (Loki, Woodpecker, Argo CD's repo server to Forgejo, CloudNativePG's operator to `forgejo-db`, Matrix) were only rendered."

- [ ] **Step 2: Check and commit**

Run: `grep -rn 'check-mesh-exclusions' CLAUDE.md docs scripts test | grep -v superpowers; mkdocs build --strict 2>&1 | tail -1`
Expected: no grep output; `Documentation built`.

```bash
git add CLAUDE.md docs
git commit -m "docs: default-deny ingress in the mesh"
```

### Task 7: After the merge into `dev` (sandbox)

Runs only after the PR is merged (ask before merging).

- [ ] **Step 1:** `make dev-clean; make dev 2>&1 | tail -5` — expected: every test passes (93 plus the two new mesh tests, counted by gotestsum with subtests). On a failure, read `kubectl -n istio-system logs -l app=ztunnel --tail=-1 | grep 'policy rejection'` for the refused caller and port, add the allow (or an `EXTRA` entry), and fix forward.
- [ ] **Step 2:** the Argo CD applications are all Synced/Healthy; `https://home.127-0-0-1.nip.io` returns 200 from Windows (`/mnt/c/Windows/System32/curl.exe`).
