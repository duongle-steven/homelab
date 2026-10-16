# kind + Cilium Sandbox (1a) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `make dev` builds a local kind + Cilium cluster that bootstraps ArgoCD from the `dev` branch on GitHub and runs ingress-nginx, cert-manager (dev CA), kube-prometheus-stack and excalidraw, reachable at `https://*.127-0-0-1.nip.io` from Windows.

**Architecture:** `metal/` creates the kind cluster on a fixed Docker network, installs Cilium through the existing Ansible role, and starts a socat proxy from `127.0.0.1:80/443` to the ingress LoadBalancer IP. `system/bootstrap.yml` installs ArgoCD with `values-dev.yaml`. The dev ApplicationSet syncs an allowlist of components from GitHub, each rendered with `values.yaml` + an optional `values-dev.yaml`.

**Tech Stack:** Nix flake (nixos-26.05 + nixos-unstable for kind/cilium-cli), kind 0.32.0 (node v1.36.1), Cilium 1.20.2, Helm 3.20, Ansible 13 (core 2.21), ArgoCD (chart 10.10.3), ingress-nginx (4.15.1), cert-manager (v1.21.2), kube-prometheus-stack (92.3.0), app-template (5.3.0), Go + terratest.

**Spec:** `docs/superpowers/specs/2026-10-12-kind-cilium-sandbox-design.md`

## Global Constraints

- Work on branch `dev`. Never touch `master` in this plan.
- One concern per commit; chart bumps are one commit per chart.
- Every tool and chart touched uses its **current stable** release, pinned exactly. Versions verified on 2026-10-12:
  - nixpkgs `nixos-26.05`; `kind` 0.32.0 and `cilium-cli` 0.19.7 from `nixos-unstable` (26.05 has kind 0.31, whose newest node image v1.35 is older than Cilium's supported maximum)
  - kind node image `kindest/node:v1.36.1@sha256:3489c7674813ba5d8b1a9977baea8a6e553784dab7b84759d1014dbd78f7ebd5` (Cilium 1.20 supports Kubernetes 1.33–1.36; 1.37 is not supported yet)
  - Cilium chart `1.20.2`
  - `argo-cd` `10.10.3`, `argocd-apps` `2.0.6`, `ingress-nginx` `4.15.1`, `cert-manager` `v1.21.2`, `kube-prometheus-stack` `92.3.0`, `app-template` `5.3.0` (excalidraw only)
  - `alpine/socat:1.8.1.3`
  - Helm stays at 3.20.2 from 26.05 (Helm 4 is only in unstable and is a major change; out of scope)
- Fixed addresses: Docker network `kind` = `172.30.0.0/16`; LB pool `172.30.255.200/29`; ingress-nginx LB IP `172.30.255.200`.
- Names: cluster `homelab-dev`; proxy container `homelab-dev-proxy`; kubeconfig `metal/kubeconfig.yaml`.
- Make variable `env` (`prod` default, `dev`). Ansible extra var `homelab_env`. Go test env var `ENV`.
- Dev GitOps source: `https://github.com/duongle-steven/homelab`, revision `dev`. Allowlist: `system/argocd`, `system/ingress-nginx`, `system/cert-manager`, `system/monitoring-system`, `apps/excalidraw`.
- Prod `values.yaml` changes only where a chart bump forces it.
- All commands run inside the dev shell (`nix develop`, or direnv in `~/homelab`).
- Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- `git push origin dev` publishes to GitHub; the user approved this flow in the spec (ArgoCD reads from `origin dev`).

## Review Focus

1. **Second `make dev` on an existing sandbox** must succeed without recreating anything (network, cluster, proxy, Cilium, ArgoCD) → checked in Task 11 step "run again".
2. **Ports 80/443 already taken** on the WSL host (another web server, an old proxy) → the proxy must fail loudly, not silently → Task 4 checks `ss` before starting and the target errors out.
3. **ArgoCD self-management dropping dev settings** (after bootstrap, ArgoCD re-renders itself only from the ApplicationSet) → Task 9 asserts the live ApplicationSet still points to GitHub `dev` after ArgoCD has synced itself.
4. **Plain `make` (prod) path changed by accident** → Task 10 renders the prod ArgoCD/apps values and diffs the generator against `master`.
5. **`make dev-clean` on a half-built sandbox** (cluster missing, proxy missing) must not fail → Task 4 runs clean twice in a row.

---

## File Structure

| File | Responsibility |
|---|---|
| `flake.nix`, `flake.lock` | dev shell tools |
| `metal/kind-dev.yaml` (new) | kind cluster definition |
| `metal/k3d-dev.yaml` (delete) | replaced by the file above |
| `metal/Makefile` | `env`-dependent `default`; `kind`, `proxy`, `clean` targets |
| `metal/inventories/dev.yml` (new) | dev vars: API endpoint, LB pool; empty `metal` group |
| `metal/inventories/prod.yml`, `stag.yml` | add Cilium API endpoint vars |
| `metal/roles/cilium/defaults/main.yml` | version bump, API endpoint from vars |
| `metal/roles/cilium/templates/ciliumloadbalancerippool.yaml` | `cilium.io/v2` |
| `system/ingress-nginx/{Chart.yaml,Chart.lock,values-dev.yaml}` | bump, dev LB IP |
| `system/cert-manager/{Chart.yaml,Chart.lock,values.yaml,values-dev.yaml,templates/clusterissuer.yaml}` | bump, dev CA issuer |
| `system/monitoring-system/{Chart.yaml,Chart.lock,values-dev.yaml}` | bump, dev overrides |
| `apps/excalidraw/{Chart.yaml,Chart.lock,values.yaml,values-dev.yaml}` | bump, schema fix, dev host |
| `system/argocd/{Chart.yaml,Chart.lock,values-dev.yaml}` | bump, dev ApplicationSet |
| `system/bootstrap.yml`, `system/Makefile` | env-aware bootstrap |
| `Makefile` | `dev`, `dev-clean` |
| `test/smoke_test.go`, `test/apps_test.go` (new) | env-aware app list |
| `CLAUDE.md` (new), `docs/installation/sandbox.md` | docs |

Helper used by render checks in several tasks (run from repo root, inside the dev shell). Write it once in Task 5 as `scripts/render-chart`:

```sh
#!/bin/sh
# Render a wrapper chart the same way ArgoCD does: values.yaml, then values-<env>.yaml if it exists.
# Usage: scripts/render-chart <chart-dir> [env]
set -eu
dir="$1"
env="${2:-prod}"
name="$(basename "$dir")"
set -- -f "$dir/values.yaml"
[ "$env" != prod ] && [ -f "$dir/values-$env.yaml" ] && set -- "$@" -f "$dir/values-$env.yaml"
helm dependency build "$dir" >/dev/null
helm template "$name" "$dir" --namespace "$name" --include-crds "$@"
```

---

### Task 1: Dev shell on current stable tools

**Files:**
- Modify: `flake.nix`
- Modify: `flake.lock`

**Interfaces:**
- Produces: `kind`, `cilium-cli`, `helm`, `kubectl`, `ansible`, `yq` on `PATH` in the dev shell.

- [ ] **Step 1: Show the current state fails the requirement**

Run: `nix develop -c sh -c 'kind version'`
Expected: FAIL, `kind: command not found` (the shell ships `kube3d`).

- [ ] **Step 2: Edit `flake.nix`**

Replace the whole file with:

```nix
{
  description = "Homelab";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";
    nixpkgs-unstable.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, nixpkgs-unstable, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
        # Only for tools whose stable release is newer than nixos-26.05 ships
        unstable = import nixpkgs-unstable { inherit system; };
      in
      with pkgs;
      {
        devShells.default = mkShell {
          packages = [
            ansible
            ansible-lint
            bmake
            diffutils
            docker
            docker-compose
            dyff
            git
            glibcLocales
            go
            gotestsum
            iproute2
            jq
            k9s
            kanidm_1_11
            kubectl
            kubernetes-helm
            kustomize
            libisoburn
            neovim
            openssh_gssapi
            opentofu # Drop-in replacement for Terraform
            p7zip
            pre-commit
            qrencode
            shellcheck
            unstable.cilium-cli
            unstable.kind
            wireguard-tools
            yamllint
            yq-go

            (python3.withPackages (p: with p; [
              jinja2
              kubernetes
              mkdocs-material
              netaddr
              pexpect
              rich
            ]))
          ];
        };
      }
    );
}
```

- [ ] **Step 3: Update the lock file**

Run: `nix flake update`
Expected: `flake.lock` now has `nixpkgs` on `nixos-26.05` and a `nixpkgs-unstable` node.

- [ ] **Step 4: Verify tool versions**

Run: `nix develop -c sh -c 'kind version; cilium version --client | head -1; helm version --short; kubectl version --client | head -1; ansible --version | head -1'`
Expected: `kind v0.32.0`, `cilium-cli: v0.19.7`, `v3.20.2`, `Client Version: v1.36.3`, `ansible [core 2.21.x]`.

If `kind` or `cilium-cli` in unstable has moved past the versions in Global Constraints, keep what unstable provides, but check that the kind release notes still list `kindest/node:v1.36.x` and update the node image digest in Task 2 to match.

- [ ] **Step 5: Commit**

```bash
git add flake.nix flake.lock
git commit -m "build: move dev shell to nixos-26.05, replace k3d with kind

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Preflight — WSL localhost forwarding and free ports

Throwaway probe; nothing is committed. It decides the proxy bind address used in Task 4.

**Files:** none

**Interfaces:**
- Produces: `PROXY_BIND` decision, either `127.0.0.1` (default) or `0.0.0.0`, recorded in Task 4.

- [ ] **Step 1: Ports 80 and 443 are free on the WSL host**

Run: `ss -ltnH '( sport = :80 or sport = :443 )'`
Expected: empty output. If not empty, stop and report what is listening.

- [ ] **Step 2: Start a probe on 127.0.0.1:80 from a host-network container**

Run:
```sh
docker run -d --rm --name homelab-probe --network host alpine/socat:1.8.1.3 \
  TCP-LISTEN:80,bind=127.0.0.1,fork,reuseaddr SYSTEM:'printf "HTTP/1.0 200 OK\r\n\r\nprobe-ok\n"'
```

- [ ] **Step 3: Reach it from Windows**

Run: `/mnt/c/Windows/System32/curl.exe -s --max-time 5 http://127.0.0.1/`
Expected: `probe-ok`

If it fails: `docker rm -f homelab-probe`, rerun Step 2 with `bind=0.0.0.0`, and repeat Step 3. If `0.0.0.0` works, use `PROXY_BIND=0.0.0.0` in Task 4. If neither works, stop and report: Windows access needs a different approach (spec risk 1).

- [ ] **Step 4: Clean up**

Run: `docker rm -f homelab-probe`

---

### Task 3: kind cluster on a fixed Docker network

**Files:**
- Create: `metal/kind-dev.yaml`
- Delete: `metal/k3d-dev.yaml`
- Create: `metal/inventories/dev.yml`
- Modify: `metal/Makefile`

**Interfaces:**
- Produces: `make -C metal env=dev kind` creates network `kind` (`172.30.0.0/16`) and cluster `homelab-dev` and writes `metal/kubeconfig.yaml`. Node name `homelab-dev-control-plane`. Inventory `inventories/dev.yml` with vars `cilium_k8s_service_host`, `cilium_k8s_service_port`, `load_balancer_ip_pool`.

- [ ] **Step 1: Confirm the target does not exist yet**

Run: `make -C metal env=dev kind`
Expected: FAIL, `No rule to make target 'kind'`.

- [ ] **Step 2: Create `metal/kind-dev.yaml`**

```yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: homelab-dev
nodes:
  - role: control-plane
    image: kindest/node:v1.36.1@sha256:3489c7674813ba5d8b1a9977baea8a6e553784dab7b84759d1014dbd78f7ebd5
networking:
  # Cilium is the CNI and replaces kube-proxy, same as production
  disableDefaultCNI: true
  kubeProxyMode: none
kubeadmConfigPatches:
  # Expose control plane metrics so kube-prometheus-stack can scrape them
  - |
    kind: ClusterConfiguration
    controllerManager:
      extraArgs:
        - name: bind-address
          value: 0.0.0.0
    scheduler:
      extraArgs:
        - name: bind-address
          value: 0.0.0.0
    etcd:
      local:
        extraArgs:
          - name: listen-metrics-urls
            value: http://0.0.0.0:2381
```

- [ ] **Step 3: Create `metal/inventories/dev.yml`**

```yaml
all:
  vars:
    cilium_k8s_service_host: homelab-dev-control-plane
    cilium_k8s_service_port: 6443
    load_balancer_ip_pool:
      - 172.30.255.200/29
# The kind node is not managed over SSH, so the metal plays have no hosts
metal:
  children:
    masters:
      hosts: {}
    workers:
      hosts: {}
```

- [ ] **Step 4: Rewrite `metal/Makefile`**

```make
.POSIX:

env ?= prod
export KUBECONFIG = $(shell pwd)/kubeconfig.yaml

kind_cluster = homelab-dev
kind_subnet = 172.30.0.0/16

default: ${env}

prod: boot cluster

dev: kind cluster

~/.ssh/id_ed25519:
	ssh-keygen -t ed25519 -P '' -f "$@"

boot: ~/.ssh/id_ed25519
	ansible-playbook \
		--inventory inventories/${env}.yml \
		boot.yml

cluster:
	ansible-playbook \
		--inventory inventories/${env}.yml \
		cluster.yml

console:
	ansible-console \
		--inventory inventories/${env}.yml

kind:
	docker network inspect kind >/dev/null 2>&1 \
		|| docker network create --subnet ${kind_subnet} kind
	kind get clusters | grep -qx ${kind_cluster} \
		|| kind create cluster --config kind-dev.yaml
	kind get kubeconfig --name ${kind_cluster} > kubeconfig.yaml
```

- [ ] **Step 5: Delete the k3d config**

Run: `git rm metal/k3d-dev.yaml`

- [ ] **Step 6: Create the cluster**

Run: `make -C metal env=dev kind`
Expected: kind prints `Creating cluster "homelab-dev"` and finishes.

- [ ] **Step 7: Verify network, node and control-plane flags**

Run:
```sh
docker network inspect kind -f '{{(index .IPAM.Config 0).Subnet}}'
KUBECONFIG=metal/kubeconfig.yaml kubectl get nodes
docker exec homelab-dev-control-plane sh -c 'ps -eo args | grep -E "kube-scheduler|kube-controller-manager|etcd " | grep -oE "(bind-address|listen-metrics-urls)=[^ ]+"'
```
Expected: `172.30.0.0/16`; node `homelab-dev-control-plane` with status `NotReady` (no CNI yet); `bind-address=0.0.0.0` twice and `listen-metrics-urls=http://0.0.0.0:2381`.

- [ ] **Step 8: Verify it is idempotent**

Run: `make -C metal env=dev kind`
Expected: succeeds and does not print `Creating cluster`.

- [ ] **Step 9: Commit**

```bash
git add metal/kind-dev.yaml metal/inventories/dev.yml metal/Makefile
git commit -m "feat(metal): create dev sandbox with kind instead of k3d

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Cilium 1.20 on kind, L2 LB IPs, localhost proxy, clean target

**Files:**
- Modify: `metal/roles/cilium/defaults/main.yml`
- Modify: `metal/roles/cilium/templates/ciliumloadbalancerippool.yaml`
- Modify: `metal/inventories/prod.yml`, `metal/inventories/stag.yml`
- Modify: `metal/Makefile`

**Interfaces:**
- Consumes: Task 3 cluster and `inventories/dev.yml`; Task 2 `PROXY_BIND`.
- Produces: `make -C metal env=dev` gives a Ready node with Cilium, LB IPAM serving `172.30.255.200/29`, and container `homelab-dev-proxy` forwarding `${PROXY_BIND}:80/443` → `172.30.255.200`. `make -C metal env=dev clean` removes cluster, proxy and network.

- [ ] **Step 1: Run the Cilium play and see it fail on kind**

Run: `make -C metal env=dev cluster`
Expected: the play runs Cilium 1.16.1 with `k8sServiceHost: 127.0.0.1` / `6444`; agents never become ready (`kubectl -n kube-system get pods -l k8s-app=cilium` shows `CrashLoopBackOff` or `Init` after 3 minutes). Uninstall before continuing: `KUBECONFIG=metal/kubeconfig.yaml helm -n kube-system uninstall cilium`.

- [ ] **Step 2: Edit `metal/roles/cilium/defaults/main.yml`**

```yaml
cilium_repo_url: https://helm.cilium.io
cilium_version: 1.20.2
cilium_namespace: kube-system
cilium_values:
  operator:
    replicas: 1
  kubeProxyReplacement: true
  l2announcements:
    enabled: true
  k8sServiceHost: "{{ cilium_k8s_service_host }}"
  k8sServicePort: "{{ cilium_k8s_service_port }}"
  hubble:
    relay:
      enabled: true
    ui:
      enabled: true
```

- [ ] **Step 3: Move the k3s endpoint into the prod and stag inventories**

In both `metal/inventories/prod.yml` and `metal/inventories/stag.yml`, under `all: vars:` (create the `all: vars:` block at the top of `stag.yml`, which has none), add:

```yaml
    # k3s-specific API endpoint, generic solution is in progress
    # https://github.com/cilium/cilium/issues/19038
    cilium_k8s_service_host: 127.0.0.1
    cilium_k8s_service_port: 6444
```

- [ ] **Step 4: Switch the IP pool to the v2 API**

`metal/roles/cilium/templates/ciliumloadbalancerippool.yaml`:

```yaml
apiVersion: cilium.io/v2
kind: CiliumLoadBalancerIPPool
metadata:
  name: default
spec:
  blocks:
    {% for cidr in load_balancer_ip_pool %}
    - cidr: {{ cidr }}
    {% endfor %}
```

`ciliuml2announcementpolicy.yaml` stays on `cilium.io/v2alpha1` (still the only version in 1.20).

- [ ] **Step 5: Add `proxy` and `clean` targets to `metal/Makefile`**

Change the `dev` line and append the targets. Use the `PROXY_BIND` from Task 2 (default `127.0.0.1`):

```make
dev: kind cluster proxy
```

```make
proxy_name = homelab-dev-proxy
proxy_bind = 127.0.0.1
ingress_ip = 172.30.255.200

proxy:
	@if docker ps --format '{{.Names}}' | grep -qx ${proxy_name}; then exit 0; fi; \
	docker rm -f ${proxy_name} >/dev/null 2>&1; \
	if ss -ltnH '( sport = :80 or sport = :443 )' | grep -q .; then \
		echo "ports 80/443 are already in use on this host:"; ss -ltnp '( sport = :80 or sport = :443 )'; exit 1; \
	fi; \
	docker run -d --name ${proxy_name} --network host --restart unless-stopped \
		--entrypoint sh alpine/socat:1.8.1.3 -c '\
			socat TCP-LISTEN:80,bind=${proxy_bind},fork,reuseaddr TCP:${ingress_ip}:80 & \
			exec socat TCP-LISTEN:443,bind=${proxy_bind},fork,reuseaddr TCP:${ingress_ip}:443'

clean:
	docker rm -f ${proxy_name} >/dev/null 2>&1 || true
	kind delete cluster --name ${kind_cluster}
	docker network rm kind >/dev/null 2>&1 || true
	rm -f kubeconfig.yaml
```

(`kind delete cluster` exits 0 when the cluster does not exist.)

- [ ] **Step 6: Build**

Run: `make -C metal env=dev`
Expected: success; the Cilium play waits for the CRDs and applies the pool and the L2 policy.

- [ ] **Step 7: Verify Cilium and the node**

Run: `KUBECONFIG=metal/kubeconfig.yaml cilium status --wait --wait-duration 5m && KUBECONFIG=metal/kubeconfig.yaml kubectl get nodes`
Expected: `Cilium: OK`, `Operator: OK`, `KubeProxyReplacement: True`; node `Ready`.

- [ ] **Step 8: Verify LB IPAM, L2 and the proxy end to end with a throwaway Service**

```sh
export KUBECONFIG=metal/kubeconfig.yaml
kubectl create deployment probe --image=docker.io/traefik/whoami:v1.11.0 --port=80
kubectl expose deployment probe --type=LoadBalancer --port=80 --overrides='{"metadata":{"annotations":{"lbipam.cilium.io/ips":"172.30.255.200"}}}'
kubectl wait --for=jsonpath='{.status.loadBalancer.ingress[0].ip}'=172.30.255.200 svc/probe --timeout=120s
kubectl rollout status deploy/probe --timeout=120s
curl -s --max-time 5 http://172.30.255.200/ | head -1
curl -s --max-time 5 http://127.0.0.1/ | head -1
/mnt/c/Windows/System32/curl.exe -s --max-time 5 http://127.0.0.1/ | head -1
kubectl delete svc,deploy probe
```
Expected: the three `curl` lines each print `Hostname: probe-...` (L2 from WSL, proxy in WSL, proxy from Windows).

If the first `curl` times out, L2 is not answering ARP because Cilium did not pick the node's interface. Fix it for dev only:
1. In `metal/roles/cilium/tasks/main.yml`, change the Install Cilium task to `values: "{{ cilium_values | combine(cilium_extra_values | default({}), recursive=True) }}"`.
2. In `metal/inventories/dev.yml`, under `all: vars:`, add:
   ```yaml
       cilium_extra_values:
         devices: eth+
   ```
3. Rerun Step 6 and recheck. Record the fix in the commit message.

- [ ] **Step 9: Verify idempotency, then clean twice**

Run:
```sh
make -C metal env=dev
make -C metal env=dev clean
make -C metal env=dev clean
docker ps -a --format '{{.Names}}' | grep -E 'homelab-dev' ; docker network ls --format '{{.Name}}' | grep -x kind
```
Expected: the build succeeds without recreating; both cleans succeed; the last line prints nothing.

- [ ] **Step 9b: Proxy refuses busy ports (Review Focus 2)**

Run:
```sh
docker run -d --rm --name homelab-busy --network host alpine/socat:1.8.1.3 TCP-LISTEN:80,bind=127.0.0.1,fork SYSTEM:'echo busy'
make -C metal env=dev proxy; echo "exit=$?"
docker rm -f homelab-busy
```
Expected: prints `ports 80/443 are already in use on this host:` with the listener, then `exit=2` (make reports the failed recipe).

- [ ] **Step 10: Rebuild for the next tasks and commit**

Run: `make -C metal env=dev`

```bash
git add metal/roles/cilium metal/inventories metal/Makefile
git commit -m "feat(metal): Cilium 1.20 with LB IPAM on kind and localhost proxy

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: ingress-nginx 4.15.1 with dev LB IP (+ render helper)

**Files:**
- Create: `scripts/render-chart`
- Modify: `system/ingress-nginx/Chart.yaml`, `system/ingress-nginx/Chart.lock`
- Create: `system/ingress-nginx/values-dev.yaml`

**Interfaces:**
- Produces: `scripts/render-chart <dir> [env]` (see File Structure). ingress-nginx Service `ingress-nginx-controller` annotated `lbipam.cilium.io/ips: 172.30.255.200` in dev.

- [ ] **Step 1: Create `scripts/render-chart`** with the content from File Structure, then `chmod +x scripts/render-chart`.

- [ ] **Step 2: Write the failing check**

Run: `scripts/render-chart system/ingress-nginx dev | yq 'select(.kind=="Service" and .spec.type=="LoadBalancer") | .metadata.annotations."lbipam.cilium.io/ips"'`
Expected: `null` (no dev values yet).

- [ ] **Step 3: Bump the chart**

In `system/ingress-nginx/Chart.yaml` set `version: 4.15.1` for `ingress-nginx`, then run `helm dependency update system/ingress-nginx`.

- [ ] **Step 4: Create `system/ingress-nginx/values-dev.yaml`**

```yaml
ingress-nginx:
  controller:
    service:
      annotations:
        lbipam.cilium.io/ips: 172.30.255.200
  # No Gitea in the sandbox
  tcp:
    "22": null
```

- [ ] **Step 5: Verify dev and prod renders**

Run:
```sh
scripts/render-chart system/ingress-nginx dev | yq 'select(.kind=="Service" and .spec.type=="LoadBalancer") | .metadata.annotations."lbipam.cilium.io/ips"'
scripts/render-chart system/ingress-nginx dev | yq 'select(.kind=="ConfigMap" and .metadata.name=="ingress-nginx-tcp") | .data'
scripts/render-chart system/ingress-nginx prod | yq 'select(.kind=="ConfigMap" and .metadata.name=="ingress-nginx-tcp") | .data'
```
Expected: `172.30.255.200`; dev tcp data empty (`null` or `{}`, or the ConfigMap is absent); prod still `"22": gitea/gitea-ssh:22`.

- [ ] **Step 6: Commit**

```bash
git add scripts/render-chart system/ingress-nginx
git commit -m "feat(ingress-nginx): bump to 4.15.1, pin dev LoadBalancer IP

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: cert-manager v1.21.2 with dev CA issuer

**Files:**
- Modify: `system/cert-manager/Chart.yaml`, `Chart.lock`, `values.yaml`, `templates/clusterissuer.yaml`
- Create: `system/cert-manager/values-dev.yaml`

**Interfaces:**
- Produces: in dev, `ClusterIssuer/selfsigned`, `Certificate/homelab-dev-ca` (secret `homelab-dev-ca`, namespace `cert-manager`), and `ClusterIssuer/letsencrypt-prod` of type `ca`. In prod, unchanged ACME issuer.

- [ ] **Step 1: Write the failing check**

Run: `scripts/render-chart system/cert-manager dev | yq 'select(.kind=="ClusterIssuer" and .metadata.name=="letsencrypt-prod") | .spec | keys'`
Expected: `- acme` (dev CA not implemented yet).

- [ ] **Step 2: Bump the chart and rename the CRD value**

`Chart.yaml`: `version: v1.21.2`. Then `helm dependency update system/cert-manager`.

`values.yaml`:

```yaml
cert-manager:
  crds:
    enabled: true
  prometheus:
    enabled: true
    servicemonitor:
      enabled: true
issuer:
  # Sandbox only: issue certificates from a local CA instead of Let's Encrypt
  dev: false
```

- [ ] **Step 3: Replace `templates/clusterissuer.yaml`**

```yaml
{{- if .Values.issuer.dev }}
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: selfsigned
spec:
  selfSigned: {}
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: homelab-dev-ca
  namespace: {{ .Release.Namespace }}
spec:
  isCA: true
  commonName: homelab-dev-ca
  secretName: homelab-dev-ca
  duration: 87600h
  privateKey:
    algorithm: ECDSA
    size: 256
  issuerRef:
    group: cert-manager.io
    kind: ClusterIssuer
    name: selfsigned
---
# Same name as production so app annotations do not change
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-prod
spec:
  ca:
    secretName: homelab-dev-ca
{{- else }}
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-prod
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    privateKeySecretRef:
      name: letsencrypt-prod
    solvers:
    - dns01:
        cloudflare:
          apiTokenSecretRef:
            name: cloudflare-api-token
            key: api-token
{{- end }}
```

- [ ] **Step 4: Create `system/cert-manager/values-dev.yaml`**

```yaml
issuer:
  dev: true
```

- [ ] **Step 5: Verify dev and prod renders**

Run:
```sh
scripts/render-chart system/cert-manager dev | yq 'select(.kind=="ClusterIssuer" or .kind=="Certificate") | .kind + "/" + .metadata.name + " " + (.spec | keys | join(","))'
scripts/render-chart system/cert-manager prod | yq 'select(.kind=="ClusterIssuer") | .metadata.name + " " + (.spec | keys | join(","))'
scripts/render-chart system/cert-manager prod | yq 'select(.kind=="CustomResourceDefinition") | .metadata.name' | wc -l
```
Expected: dev prints `ClusterIssuer/selfsigned selfSigned`, `Certificate/homelab-dev-ca ...isCA...`, `ClusterIssuer/letsencrypt-prod ca`; prod prints `letsencrypt-prod acme`; CRD count `6`.

- [ ] **Step 6: Commit**

```bash
git add system/cert-manager
git commit -m "feat(cert-manager): bump to v1.21.2, local CA issuer in dev

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: kube-prometheus-stack 92.3.0 with dev overrides

**Files:**
- Modify: `system/monitoring-system/Chart.yaml`, `Chart.lock`
- Create: `system/monitoring-system/values-dev.yaml`

**Interfaces:**
- Produces: in dev, no Alertmanager, no kube-proxy monitor/rules, no Loki datasource.

- [ ] **Step 1: Write the failing check**

Run: `scripts/render-chart system/monitoring-system dev | yq 'select(.kind=="Alertmanager") | .kind'; scripts/render-chart system/monitoring-system dev | grep -c 'loki.loki:3100'`
Expected: `Alertmanager` printed and loki count ≥ 1.

- [ ] **Step 2: Bump the chart**

`Chart.yaml`: `version: 92.3.0`. Then `helm dependency update system/monitoring-system`.

- [ ] **Step 3: Create `system/monitoring-system/values-dev.yaml`**

```yaml
kube-prometheus-stack:
  # The ntfy relay would send real notifications from the sandbox
  alertmanager:
    enabled: false
  # Cilium replaces kube-proxy
  kubeProxy:
    enabled: false
  defaultRules:
    rules:
      kubeProxy: false
  # No Loki in the sandbox
  grafana:
    additionalDataSources: []
```

- [ ] **Step 4: Verify dev and prod renders**

Run:
```sh
scripts/render-chart system/monitoring-system dev | yq 'select(.kind=="Alertmanager") | .kind'
scripts/render-chart system/monitoring-system dev | yq 'select(.kind=="ServiceMonitor") | .metadata.name' | grep -c proxy
scripts/render-chart system/monitoring-system dev | grep -c 'loki.loki:3100'
scripts/render-chart system/monitoring-system dev | yq 'select(.kind=="ServiceMonitor") | .metadata.name' | grep -E 'etcd|scheduler|controller-manager|kubelet|apiserver'
scripts/render-chart system/monitoring-system prod | yq 'select(.kind=="Alertmanager") | .kind'
```
Expected: empty; `0`; `0`; five ServiceMonitors (etcd, scheduler, controller-manager, kubelet, apiserver); prod prints `Alertmanager`.

If the prod render fails on removed/renamed values (major version jump 56 → 92), fix them in `values.yaml` and list each fix in the commit message.

- [ ] **Step 5: Commit**

```bash
git add system/monitoring-system
git commit -m "feat(monitoring): bump kube-prometheus-stack to 92.3.0, dev overrides

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: excalidraw on app-template 5.3.0 with dev host

**Files:**
- Modify: `apps/excalidraw/Chart.yaml`, `Chart.lock`, `values.yaml`
- Create: `apps/excalidraw/values-dev.yaml`

**Interfaces:**
- Produces: Ingress `excalidraw` (namespace `excalidraw`) with host `draw.127-0-0-1.nip.io` in dev.

- [ ] **Step 1: Bump and see the schema failure**

`Chart.yaml`: `version: 5.3.0`. Run `helm dependency update apps/excalidraw`, then `scripts/render-chart apps/excalidraw prod`.
Expected: FAIL, `at '/ingress/main/hosts/0/paths/0/service': additional properties 'name' not allowed`.

- [ ] **Step 2: Fix `values.yaml` for the new schema**

Replace the ingress path service block:

```yaml
              service:
                identifier: main
                port: http
```

- [ ] **Step 3: Create `apps/excalidraw/values-dev.yaml`**

```yaml
app-template:
  ingress:
    main:
      annotations:
        external-dns.alpha.kubernetes.io/target: null
        external-dns.alpha.kubernetes.io/cloudflare-proxied: null
      hosts:
        - host: &host draw.127-0-0-1.nip.io
          paths:
            - path: /
              pathType: Prefix
              service:
                identifier: main
                port: http
      tls:
        - hosts:
            - *host
          secretName: excalidraw-tls-certificate
```

- [ ] **Step 4: Verify dev and prod renders**

Run:
```sh
scripts/render-chart apps/excalidraw dev | yq 'select(.kind=="Ingress") | .metadata.name + " " + .spec.rules[0].host + " " + (.metadata.annotations | keys | join(","))'
scripts/render-chart apps/excalidraw prod | yq 'select(.kind=="Ingress") | .spec.rules[0].host + " " + (.spec.rules[0].http.paths[0].backend.service.port.number | tostring)'
```
Expected: `excalidraw draw.127-0-0-1.nip.io cert-manager.io/cluster-issuer`; prod `draw.khuedoan.com 80`.

- [ ] **Step 5: Commit**

```bash
git add apps/excalidraw
git commit -m "feat(excalidraw): bump app-template to 5.3.0, dev host

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: ArgoCD 10.10.3 with dev ApplicationSet and env-aware bootstrap

**Files:**
- Modify: `system/argocd/Chart.yaml`, `Chart.lock`
- Create: `system/argocd/values-dev.yaml`
- Modify: `system/bootstrap.yml`, `system/Makefile`

**Interfaces:**
- Consumes: Tasks 5–8 pushed to `origin dev`.
- Produces: `make -C system env=dev` installs ArgoCD; ApplicationSet `root` generates exactly the five allowlisted Applications from GitHub `dev`, each with `valueFiles: [values.yaml, values-dev.yaml]`.

- [ ] **Step 1: Write the failing check**

Run: `scripts/render-chart system/argocd dev | yq 'select(.kind=="ApplicationSet") | .spec.generators[0].git.repoURL'`
Expected: `http://gitea-http.gitea:3000/ops/homelab`.

- [ ] **Step 2: Bump the charts**

`Chart.yaml`: `argo-cd` → `10.10.3`, `argocd-apps` → `2.0.6`. Then `helm dependency update system/argocd`.

Run: `scripts/render-chart system/argocd prod >/dev/null`
Expected: success. If a value was renamed in argo-cd 10.x, fix it in `values.yaml` and note it in the commit.

- [ ] **Step 3: Create `system/argocd/values-dev.yaml`**

```yaml
# Sandbox: sync an allowlist from GitHub instead of in-cluster Gitea.
# Helm replaces lists and anchors do not cross files, so generator and source are restated in full.
argo-cd:
  global:
    domain: argocd.127-0-0-1.nip.io
argocd-apps:
  applicationsets:
    root:
      generators:
        - git:
            repoURL: &repoURL https://github.com/duongle-steven/homelab
            revision: &revision dev
            directories:
              - path: system/argocd
              - path: system/ingress-nginx
              - path: system/cert-manager
              - path: system/monitoring-system
              - path: apps/excalidraw
      template:
        spec:
          source:
            repoURL: *repoURL
            targetRevision: *revision
            helm:
              valueFiles:
                - values.yaml
                - values-dev.yaml
              ignoreMissingValueFiles: true
```

- [ ] **Step 4: Verify the dev render**

Run:
```sh
scripts/render-chart system/argocd dev | yq 'select(.kind=="ApplicationSet") | .spec.generators[0].git.repoURL, .spec.generators[0].git.revision, (.spec.generators[0].git.directories | length), .spec.template.spec.source.helm.valueFiles, .spec.template.spec.source.path, .spec.template.spec.syncPolicy.syncOptions'
scripts/render-chart system/argocd dev | yq 'select(.kind=="Ingress") | .spec.rules[0].host'
```
Expected: `https://github.com/duongle-steven/homelab`, `dev`, `5`, `[values.yaml, values-dev.yaml]`, `{{path}}` (kept from values.yaml), syncOptions still present; host `argocd.127-0-0-1.nip.io`.

- [ ] **Step 5: Make bootstrap env-aware**

`system/Makefile`:

```make
.POSIX:

env ?= prod
export KUBECONFIG = $(shell pwd)/../metal/kubeconfig.yaml

.PHONY: bootstrap
bootstrap:
	ansible-playbook \
		--extra-vars homelab_env=${env} \
		bootstrap.yml
```

`system/bootstrap.yml`, replace the `values_files` entry of "Render ArgoCD manifests from Helm chart" with:

```yaml
        values_files: >-
          {{
            ['argocd/values-dev.yaml'] if homelab_env | default('prod') == 'dev'
            else ['argocd/' ~ ((first_install.resources | length == 0) | ternary('values-seed.yaml', 'values.yaml'))]
          }}
```

(The chart's own `values.yaml` is always loaded by Helm, so dev renders `values.yaml` + `values-dev.yaml`.)

- [ ] **Step 6: Commit and push the GitOps changes so the sandbox can sync them**

```bash
git add system/argocd system/bootstrap.yml system/Makefile
git commit -m "feat(argocd): bump to 10.10.3, dev ApplicationSet and env-aware bootstrap

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git push -u origin dev
```

- [ ] **Step 7: Bootstrap and wait for the Applications**

Run:
```sh
make -C system env=dev
export KUBECONFIG=metal/kubeconfig.yaml
kubectl -n argocd wait --for=jsonpath='{.status.health.status}'=Healthy application --all --timeout=20m
kubectl -n argocd get applications -o custom-columns=NAME:.metadata.name,SYNC:.status.sync.status,HEALTH:.status.health.status
```
Expected: exactly `argocd`, `cert-manager`, `excalidraw`, `ingress-nginx`, `monitoring-system`, all `Synced` / `Healthy`. If one is stuck, `kubectl -n argocd get application <name> -o yaml | yq .status.conditions` and fix the values in that component's task, push, and repeat.

- [ ] **Step 8: Verify ArgoCD kept the dev settings after managing itself (Review Focus 3)**

Run:
```sh
kubectl -n argocd get applicationset root -o yaml | yq '.spec.generators[0].git.repoURL, .spec.generators[0].git.revision'
kubectl -n argocd get application argocd -o yaml | yq '.status.sync.status, .spec.source.helm.valueFiles'
```
Expected: `https://github.com/duongle-steven/homelab`, `dev`, `Synced`, `[values.yaml, values-dev.yaml]`.

- [ ] **Step 9: Verify the ingress IP and certificates**

Run:
```sh
kubectl -n ingress-nginx get svc ingress-nginx-controller -o jsonpath='{.status.loadBalancer.ingress[0].ip}'; echo
kubectl get certificate -A
```
Expected: `172.30.255.200`; `homelab-dev-ca`, `argocd-server-tls`, `excalidraw-tls-certificate` all `READY True`.

---

### Task 10: `make dev` / `make dev-clean` and env-aware smoke test

**Files:**
- Modify: `Makefile`
- Create: `test/apps_test.go`
- Modify: `test/smoke_test.go`

**Interfaces:**
- Consumes: `metal` `dev`/`clean` targets, `system` env-aware bootstrap.
- Produces: `make dev`, `make dev-clean`; `smokeApps(env string) []smokeApp` in package `test`.

- [ ] **Step 1: Write the failing unit test `test/apps_test.go`**

```go
package test

import (
	"reflect"
	"testing"
)

func TestSmokeAppsForEnv(t *testing.T) {
	dev := []smokeApp{
		{"argocd-server", "argocd"},
		{"excalidraw", "excalidraw"},
	}
	if got := smokeApps("dev"); !reflect.DeepEqual(got, dev) {
		t.Errorf("smokeApps(dev) = %v, want %v", got, dev)
	}

	prod := smokeApps("")
	if len(prod) != 6 || prod[0].name != "argocd-server" {
		t.Errorf("smokeApps(\"\") = %v, want the 6 production apps", prod)
	}
	if !reflect.DeepEqual(smokeApps("prod"), prod) {
		t.Errorf("smokeApps(prod) must equal the default list")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd test && go test -run TestSmokeAppsForEnv ./...`
Expected: FAIL, `undefined: smokeApp` / `undefined: smokeApps`.

- [ ] **Step 3: Implement in `test/smoke_test.go`**

Replace the `mainApps` declaration inside `TestSmoke` with a call, and add the type and function above `TestSmoke`:

```go
type smokeApp struct {
	name      string
	namespace string
}

// smokeApps returns the apps whose ingress is checked; the sandbox only runs a subset.
func smokeApps(env string) []smokeApp {
	if env == "dev" {
		return []smokeApp{
			{"argocd-server", "argocd"},
			{"excalidraw", "excalidraw"},
		}
	}

	return []smokeApp{
		{"argocd-server", "argocd"},
		{"gitea", "gitea"},
		{"grafana", "grafana"},
		{"homepage", "homepage"},
		{"kanidm", "kanidm"},
		{"zot", "zot"},
	}
}

func TestSmoke(t *testing.T) {
	t.Parallel()

	for _, app := range smokeApps(os.Getenv("ENV")) {
```

(The loop body is unchanged.)

- [ ] **Step 4: Run the unit test**

Run: `cd test && go test -run TestSmokeAppsForEnv ./... && go vet ./...`
Expected: PASS, no vet errors.

- [ ] **Step 5: Add `dev` and `dev-clean` to the root `Makefile`**

Add after `default:`:

```make
dev:
	$(MAKE) env=dev ENV=dev INSECURE_SKIP_VERIFY=1 metal system smoke-test

dev-clean:
	$(MAKE) -C metal env=dev clean
```

- [ ] **Step 6: Run the smoke test against the running sandbox**

Run: `make env=dev ENV=dev INSECURE_SKIP_VERIFY=1 smoke-test`
Expected: `TestSmoke/argocd-server` and `TestSmoke/excalidraw` PASS.

- [ ] **Step 7: Check prod defaults did not move (Review Focus 4)**

Run:
```sh
make -n -C metal | grep -E 'inventory|kind'
git diff master -- system/argocd/values.yaml | grep -E '^[+-] .*(repoURL|revision|path:)' || echo "prod generator unchanged"
```
Expected: two `--inventory inventories/prod.yml` lines (`boot.yml`, `cluster.yml`) and no `kind`; `prod generator unchanged`.

- [ ] **Step 8: Commit and push**

```bash
git add Makefile test/apps_test.go test/smoke_test.go
git commit -m "feat: add make dev/dev-clean and env-aware smoke test

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git push
```

---

### Task 11: Full acceptance run from scratch

**Files:** none (fixes go back to the task that owns the file)

- [ ] **Step 1: From scratch**

Run: `make dev-clean && time make dev`
Expected: exits 0; smoke test PASS (criteria 1, 5).

- [ ] **Step 2: Run again (Review Focus 1)**

Run: `make dev`
Expected: exits 0 without `Creating cluster`; proxy not recreated (`docker ps --filter name=homelab-dev-proxy --format '{{.Status}}'` shows the old uptime).

- [ ] **Step 3: Cilium and LB IP (criterion 2)**

Run: `export KUBECONFIG=metal/kubeconfig.yaml; cilium status | grep -E 'Cilium:|Operator:'; kubectl -n ingress-nginx get svc ingress-nginx-controller -o jsonpath='{.status.loadBalancer.ingress[0].ip}'`
Expected: `OK` twice; `172.30.255.200`.

- [ ] **Step 4: ArgoCD apps (criterion 3)**

Run: `kubectl -n argocd get applications`
Expected: 5 apps, all `Synced` / `Healthy`.

- [ ] **Step 5: Prometheus targets (criterion 4)**

Run:
```sh
kubectl -n monitoring-system port-forward svc/monitoring-system-kube-pr-prometheus 9090 >/dev/null 2>&1 &
sleep 3
curl -s 'http://127.0.0.1:9090/api/v1/targets?state=active' | jq -r '.data.activeTargets[] | select(.health!="up") | .labels.job + " " + .lastError'
kill %1
```
Expected: empty output. (If the Service name differs, find it with `kubectl -n monitoring-system get svc | grep prometheus`.)

- [ ] **Step 6: Windows browser (criterion 6)**

Run: `/mnt/c/Windows/System32/curl.exe -sk -o NUL -w '%{http_code}\n' https://draw.127-0-0-1.nip.io/ https://argocd.127-0-0-1.nip.io/`
Expected: `200` twice. Then ask the user to open both URLs in their Windows browser and confirm.

- [ ] **Step 7: RAM snapshot**

Run: `docker stats --no-stream homelab-dev-control-plane --format '{{.MemUsage}}'`
Expected: well under 12 GiB; record the number in the sandbox docs (Task 12).

- [ ] **Step 8: Clean (criterion 7)**

Run: `make dev-clean; docker ps -a --format '{{.Names}}' | grep homelab-dev; docker network ls --format '{{.Name}}' | grep -x kind; echo done`
Expected: only `done`.

---

### Task 12: Docs — repo CLAUDE.md and sandbox guide

**Files:**
- Create: `CLAUDE.md`
- Modify: `docs/installation/sandbox.md`
- Modify: `docs/superpowers/specs/2026-10-12-kind-cilium-sandbox-design.md` (status line)

- [ ] **Step 1: Create `CLAUDE.md`**

```markdown
# Homelab (fork of khuedoan/homelab)

This fork diverges from upstream. Selected upstream changes are still cherry-picked, so keep the `apps/<name>/{Chart.yaml,values.yaml}` layout.

## Roadmap

Each step changes one thing, with its own spec and plan in `docs/superpowers/`.

0. Workstation: NixOS-WSL (done, outside this repo)
1. Sandbox: 1a kind + Cilium foundation, 1b platform (external-secrets, kanidm, dex, Grafana, zot)
2. Replace ingress-nginx and `Ingress` objects with Gateway API (Cilium Gateway), tested in the sandbox
3. Production: kubeadm instead of k3s, Ubuntu LTS instead of Fedora (rewrite `metal/`)

## Rules

- Use the current stable release of every tool and chart, pinned exactly. The versions in this repo are not a reference; check upstream when touching something.
- Branch `dev` is the integration branch: changes land there, are tested in the sandbox, then merged into `master`.
- Sandbox config lives next to production config and is selected with `env=dev`. Never fork config into a separate branch or directory.

## Sandbox

- `make dev` builds it, `make dev-clean` removes it. Pages: `https://<app>.127-0-0-1.nip.io`.
- ArgoCD in the sandbox syncs from `github.com/duongle-steven/homelab` branch `dev`; push to see changes.
- Components synced in the sandbox are listed in `system/argocd/values-dev.yaml`.
- A component that needs different settings in the sandbox gets a `values-dev.yaml` next to its `values.yaml`. It is loaded only in the sandbox.
- Fixed addresses: Docker network `kind` 172.30.0.0/16, LB pool 172.30.255.200/29, ingress 172.30.255.200.
- Certificates come from a local CA (`homelab-dev-ca` in namespace `cert-manager`), issued under the production issuer name `letsencrypt-prod`.
```

- [ ] **Step 2: Rewrite `docs/installation/sandbox.md`**

```markdown
# Development sandbox

The sandbox is a local kind cluster for testing changes on the `dev` branch before they are merged into `master`.

## Prerequisites

- Docker and Nix (see [development shell](../concepts/development-shell.md))
- About 12 GiB of RAM available to Docker (measured usage: <RAM from Task 11 Step 7>)
- Ports `80` and `443` free on the host

## Build

```sh
git switch dev
nix develop
make dev
```

`make dev` creates the kind cluster with Cilium, bootstraps ArgoCD, waits for it, and runs the smoke test.

## Explore

- <https://argocd.127-0-0-1.nip.io> (password: `kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d`)
- <https://draw.127-0-0-1.nip.io>

On Windows with WSL, open these from the Windows browser.

Certificates are issued by a local CA that is recreated with the cluster. To remove the browser warning, export it and import it into your trust store (on Windows: double-click `homelab-dev-ca.crt` → Install Certificate → Local Machine → Trusted Root Certification Authorities):

```sh
kubectl -n cert-manager get secret homelab-dev-ca -o jsonpath='{.data.ca\.crt}' | base64 -d > homelab-dev-ca.crt
```

## Make changes

ArgoCD in the sandbox syncs from the `dev` branch on GitHub:

```sh
git commit -m "feat: harness the power of the sun"
git push origin dev
```

Only the components listed in `system/argocd/values-dev.yaml` are synced. A component that needs sandbox-specific settings gets a `values-dev.yaml` next to its `values.yaml`.

## Clean up

```sh
make dev-clean
```

## Differences from production

- Certificates come from a local CA instead of Let's Encrypt
- Only accessible from the host machine
- No Alertmanager, Loki, backups, or platform services (yet)
```

Replace `<RAM from Task 11 Step 7>` with the measured value.

- [ ] **Step 3: Mark the spec approved**

In the spec, change `Status: approved design, 2026-10-12. Branch: \`dev\`.` to `Status: approved 2026-10-12, implemented on branch \`dev\`.`

- [ ] **Step 4: Check the docs build**

Run: `mkdocs build --strict -d /tmp/claude-1000/-home-nixos-homelab/ef55c1f3-87d0-4054-b172-26f5aed35b1d/scratchpad/site 2>&1 | tail -3`
Expected: no warnings about `installation/sandbox.md`.

- [ ] **Step 5: Commit and push**

```bash
git add CLAUDE.md docs/installation/sandbox.md docs/superpowers/specs/2026-10-12-kind-cilium-sandbox-design.md
git commit -m "docs: add CLAUDE.md and rewrite sandbox guide for kind

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git push
```
