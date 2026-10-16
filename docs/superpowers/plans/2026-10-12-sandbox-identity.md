# Sandbox Identity (1b) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The sandbox runs generated secrets (ESO), Kanidm managed by kaniop with three groups and four test accounts, and Dex in front of Kanidm. Go + chromedp tests prove that each test account logs in through Dex with the right `groups` claim and that an account in no group is refused.

**Architecture:**
- **Secrets.** ESO `Password` generators in namespace `global-secrets` replace the Go Job; the existing `ClusterSecretStore` distributes them.
- **Identity.** kaniop deploys Kanidm (`Kanidm` CR) and reconciles groups, persons and Dex's OAuth2 client from git.
- **Dex.** Dex uses PKCE against Kanidm and trusts the dev CA through its own certificate's `ca.crt`.
- **Sandbox DNS.** A CoreDNS rewrite, sandbox only, sends `*.127-0-0-1.nip.io` to ingress-nginx inside the cluster.
- **Tests.**
  - Credentials: test accounts enrol a password and TOTP through Kanidm's credential-update API, using the reset token that kaniop publishes as an event. The credentials are kept in a Secret so reruns can log in again.
  - Login: chromedp drives the login and captures the authorization code from a Dex client named `smoke`.

**Tech Stack:** external-secrets chart 2.12.0, kaniop chart 0.18.0 (Kanidm 1.11.2), Dex chart 0.26.0 (Dex 2.46.0), Ansible (kubernetes.core), Go 1.26 + chromedp v0.21.0 + pquerna/otp v1.5.0, nixpkgs `chromium`.

**Spec:** `docs/superpowers/specs/2026-10-12-sandbox-identity-design.md`

## Global Constraints

- **Workflow:**
  - Work on branch `dev`, and push to `origin dev` when a task says so (ArgoCD in the sandbox syncs from it).
  - One concern per commit. Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
  - All commands run inside the dev shell (`nix develop` or direnv in `~/homelab`). `export KUBECONFIG=$PWD/metal/kubeconfig.yaml` for `kubectl`.
  - `Chart.lock` is gitignored; never commit it.
- **Versions:** current stable releases, pinned exactly (verified 2026-10-12): external-secrets `2.12.0`, kaniop `0.18.0`, Kanidm image `docker.io/kanidm/server:1.11.2`, Dex chart `0.26.0`, chromedp `v0.21.0`, pquerna/otp `v1.5.0`.
- **Helm values:** Helm replaces lists and cannot delete keys across values files; dev overrides restate whole lists.
- **Names:**
  - Groups: `homelab-admins`, `homelab-editors`, `homelab-users`.
  - Test accounts: `dev-admin`, `dev-editor`, `dev-viewer`, `dev-outsider`, with mail `<name>@example.com`.
  - Kanidm CR `kanidm` in namespace `kanidm`; admin Secret `kanidm-admin-passwords`.
  - Dex OAuth2 client `dex` in namespace `dex`; its Secret `dex-kanidm-oauth2-credentials` gets aliases `KANIDM_CLIENT_ID` and `KANIDM_CLIENT_SECRET`.
  - Dex test client `smoke` with redirect `https://smoke.127-0-0-1.nip.io/callback`; its secret is `dex.smoke` (key `client_secret`) in namespace `global-secrets`.
  - Test credentials Secret `identity-test-credentials` in namespace `kanidm`.
- **Hosts (dev):** `auth.127-0-0-1.nip.io` (Kanidm) and `dex.127-0-0-1.nip.io` (Dex). Production keeps the domains already in `values.yaml`.
- **Group membership:** `KanidmGroup.members` replaces the whole membership. It is set **only** in dev; production groups never carry `members`, so people added by `scripts/onboard-user` are kept.
- **Make:** `make dev` runs the smoke test with filter `Smoke|Identity`.

## Review Focus

1. **Second `make dev`.** Test accounts already have credentials, so kaniop publishes no new reset token, and the tests must log in with the stored credentials instead of failing. → Task 8, step "rerun".
2. **TOTP code reuse.** Kanidm rejects a code from a 30 s step that was already used, for example enrolment followed at once by login. → Task 7, unit test `TestIdentityTOTPWaitsForNextStep`.
3. **Production group membership wiped.** A prod render of `KanidmGroup` with `members` would remove every onboarded person. → Task 4 asserts that the prod render has no `members`.
4. **`groups` claim format.** Kanidm may send SPNs (`homelab-admins@auth…`). The assertion must accept both forms, and the actual value is logged for 1c. → Task 7, unit test `TestIdentityHasGroup`.
5. **Dex starting before its Kanidm client Secret exists.** Dex must converge without manual action. → Task 6 waits for Dex readiness after a cold sync and checks that the restarts settle.

---

## File Structure

| File | Responsibility |
|---|---|
| `platform/external-secrets/Chart.yaml` | ESO 2.12.0 |
| 10 files with `external-secrets.io/v1beta1` | API v1 |
| `platform/global-secrets/{values.yaml,values-dev.yaml,templates/generated-secrets.yaml}` | generated secrets |
| `platform/global-secrets/files/`, `templates/secret-generator/` | deleted |
| `metal/roles/coredns_rewrite/tasks/main.yml`, `metal/cluster.yml`, `metal/inventories/dev.yml` | sandbox DNS rewrite |
| `platform/kanidm/{Chart.yaml,values.yaml,values-dev.yaml}` | kaniop + Kanidm settings |
| `platform/kanidm/templates/{kanidm.yaml,groups.yaml,test-accounts.yaml}` | Kanidm CR, groups, dev persons |
| `platform/kanidm/templates/{issuer.yaml,certificate.yaml}` | deleted |
| `platform/dex/{Chart.yaml,values.yaml,values-dev.yaml}` | Dex 0.26 + connector + smoke client |
| `platform/dex/templates/{kanidm-client.yaml,secret.yaml}` | OAuth2 client, ExternalSecret |
| `system/argocd/values-dev.yaml` | allowlist |
| `scripts/onboard-user` | people by group |
| `scripts/hacks`, `scripts/kanidm-reset-password` | deleted |
| `Makefile` | `post-install` removed, `smoke_filter` |
| `flake.nix`, `flake.lock` | `chromium` |
| `test/go.mod`, `test/go.sum` | chromedp, otp, testify |
| `test/identity_helpers_test.go` | credentials, TOTP, browser login, token exchange |
| `test/identity_test.go` | `TestIdentityLogin`, `TestIdentityDenied`, unit tests |
| `test/smoke_test.go` | dev list adds kanidm, dex |
| `docs/installation/sandbox.md`, `docs/concepts/secrets-management.md`, `docs/getting-started/user-onboarding.md`, `CLAUDE.md` | docs |

---

### Task 1: external-secrets 2.12.0 and the v1 API everywhere

**Files:**
- Modify: `platform/external-secrets/Chart.yaml`
- Modify: `apps/paperless/templates/secret.yaml`, `apps/tailscale/templates/secret.yaml`, `apps/wireguard/values.yaml`, `platform/dex/templates/secret.yaml`, `platform/gitea/templates/admin-secret.yaml`, `platform/global-secrets/templates/clustersecretstore/clustersecretstore.yaml`, `platform/grafana/templates/secret.yaml`, `platform/renovate/templates/secret.yaml`, `platform/woodpecker/templates/secret.yaml`, `platform/zot/templates/admin-secret.yaml`

**Interfaces:**
- Produces: ESO CRDs at `external-secrets.io/v1` and `generators.external-secrets.io/v1alpha1`. Every ExternalSecret in the repo uses `v1`.

- [ ] **Step 1: Failing check**

Run: `grep -rln 'external-secrets.io/v1beta1' apps platform system | wc -l`
Expected: `10`

- [ ] **Step 2: Bump the chart**

In `platform/external-secrets/Chart.yaml`, set `version: 2.12.0`.

- [ ] **Step 3: Move every file to v1**

Run: `grep -rl 'external-secrets.io/v1beta1' apps platform system | xargs sed -i 's#external-secrets.io/v1beta1#external-secrets.io/v1#'`

- [ ] **Step 4: Verify**

Run:
```sh
grep -rln 'external-secrets.io/v1beta1' apps platform system | wc -l
for d in platform/external-secrets apps/paperless apps/tailscale apps/wireguard platform/dex platform/gitea platform/global-secrets platform/grafana platform/renovate platform/woodpecker platform/zot; do scripts/render-chart $d >/dev/null && echo "ok $d"; done
scripts/render-chart platform/external-secrets | yq 'select(.kind=="CustomResourceDefinition") | .metadata.name' | grep -E '^(externalsecrets|clustersecretstores|passwords)\.'
```
Expected: `0`; eleven `ok` lines; `externalsecrets.external-secrets.io`, `clustersecretstores.external-secrets.io` and `passwords.generators.external-secrets.io`.

If a chart fails on the v1 schema (for example a removed field), fix that file and name the change in the commit message.

- [ ] **Step 5: Commit**

```bash
git add platform/external-secrets apps/paperless apps/tailscale apps/wireguard platform/dex/templates/secret.yaml platform/gitea platform/global-secrets/templates/clustersecretstore platform/grafana platform/renovate platform/woodpecker platform/zot
git commit -m "feat(external-secrets): bump to 2.12.0 and move to the v1 API

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: global-secrets from ESO Password generators, in the sandbox

**Files:**
- Delete: `platform/global-secrets/files/`, `platform/global-secrets/templates/secret-generator/`
- Create: `platform/global-secrets/values.yaml`, `platform/global-secrets/values-dev.yaml`, `platform/global-secrets/templates/generated-secrets.yaml`
- Modify: `system/argocd/values-dev.yaml`

**Interfaces:**
- Consumes: Task 1 CRDs.
- Produces: Secrets `gitea.admin`, `dex.grafana`, `dex.gitea`, `registry.admin`, `woodpecker.agent` and `paperless.admin` (plus `dex.smoke` in dev) in namespace `global-secrets`, with the same keys as before, created once.

- [ ] **Step 1: Failing check**

Run: `scripts/render-chart platform/global-secrets dev | yq 'select(.kind=="ExternalSecret") | .spec.target.name' | grep -v '^---'`
Expected: empty (the Job renders, no ExternalSecrets).

- [ ] **Step 2: Remove the Job**

Run: `git rm -r -q platform/global-secrets/files platform/global-secrets/templates/secret-generator`

- [ ] **Step 3: Create `platform/global-secrets/values.yaml`**

```yaml
# Random secrets generated once per cluster and shared through the global-secrets ClusterSecretStore.
# They are never rotated automatically; see docs/concepts/secrets-management.md.
secrets:
  - name: gitea.admin
    key: password
    special: true
  - name: dex.grafana
    key: client_secret
  - name: dex.gitea
    key: client_secret
  - name: registry.admin
    key: password
    special: true
  - name: woodpecker.agent
    key: secret
  - name: paperless.admin
    key: PAPERLESS_ADMIN_PASSWORD
    special: true
# Environment-specific additions, so the list above does not have to be restated
extraSecrets: []
```

- [ ] **Step 4: Create `platform/global-secrets/values-dev.yaml`**

```yaml
extraSecrets:
  - name: dex.smoke
    key: client_secret
```

- [ ] **Step 5: Create `platform/global-secrets/templates/generated-secrets.yaml`**

```yaml
{{- range concat .Values.secrets .Values.extraSecrets }}
---
apiVersion: generators.external-secrets.io/v1alpha1
kind: Password
metadata:
  name: {{ .name }}
  namespace: {{ $.Release.Namespace }}
spec:
  length: {{ .length | default 32 }}
  digits: 6
  symbols: {{ if .special }}6{{ else }}0{{ end }}
  allowRepeat: true
  noUpper: false
---
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: {{ .name }}
  namespace: {{ $.Release.Namespace }}
spec:
  # Generate once; regenerating would break consumers (see secrets-management docs)
  refreshInterval: "0"
  target:
    name: {{ .name }}
    template:
      data:
        {{ .key }}: "{{ `{{ .password }}` }}"
  dataFrom:
    - sourceRef:
        generatorRef:
          apiVersion: generators.external-secrets.io/v1alpha1
          kind: Password
          name: {{ .name }}
{{- end }}
```

- [ ] **Step 6: Verify renders**

Run:
```sh
scripts/render-chart platform/global-secrets prod | yq 'select(.kind=="ExternalSecret") | .spec.target.name + " " + (.spec.target.template.data | keys | .[0]) + " " + .spec.refreshInterval' | grep -v '^---'
scripts/render-chart platform/global-secrets dev | yq 'select(.kind=="ExternalSecret") | .spec.target.name' | grep -v '^---' | wc -l
scripts/render-chart platform/global-secrets prod | yq 'select(.kind=="Password" and .metadata.name=="dex.grafana") | .spec.symbols'
```
Expected:
- six lines: `gitea.admin password 0`, `dex.grafana client_secret 0`, `dex.gitea client_secret 0`, `registry.admin password 0`, `woodpecker.agent secret 0`, `paperless.admin PAPERLESS_ADMIN_PASSWORD 0`
- `7`
- `0`

- [ ] **Step 7: Add to the sandbox allowlist**

In `system/argocd/values-dev.yaml`, add after `- path: apps/excalidraw`:

```yaml
              - path: platform/external-secrets
              - path: platform/global-secrets
```

- [ ] **Step 8: Commit, push, and verify in the sandbox**

```bash
git add platform/global-secrets system/argocd/values-dev.yaml
git commit -m "feat(global-secrets): generate secrets with ESO Password generators

Replaces the golang:1.19 Job that downloaded modules at runtime.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git push
```

Run:
```sh
kubectl -n argocd wait --for=jsonpath='{.status.health.status}'=Healthy application/external-secrets application/global-secrets --timeout=15m
kubectl -n global-secrets get externalsecret -o custom-columns=NAME:.metadata.name,READY:.status.conditions[0].status
before=$(kubectl -n global-secrets get secret dex.smoke -o jsonpath='{.data.client_secret}')
kubectl -n external-secrets rollout restart deploy && kubectl -n external-secrets rollout status deploy --timeout=5m
sleep 60
after=$(kubectl -n global-secrets get secret dex.smoke -o jsonpath='{.data.client_secret}')
[ -n "$before" ] && [ "$before" = "$after" ] && echo "value kept"
```
Expected: both apps become Healthy (if the applications do not exist yet, wait about 3 minutes for the ApplicationSet to create them); seven ExternalSecrets `True`; `value kept`.

---

### Task 3: CoreDNS rewrite for sandbox hostnames

**Files:**
- Create: `metal/roles/coredns_rewrite/tasks/main.yml`
- Modify: `metal/cluster.yml`, `metal/inventories/dev.yml`

**Interfaces:**
- Produces: inside the sandbox, any `*.127-0-0-1.nip.io` name resolves to the `ingress-nginx-controller` ClusterIP.

- [ ] **Step 1: Failing check**

Run: `kubectl run dnstest --rm -i --restart=Never --image=docker.io/library/busybox:1.37 -- nslookup auth.127-0-0-1.nip.io 2>&1 | grep -A1 '^Name:'; kubectl -n ingress-nginx get svc ingress-nginx-controller -o jsonpath='{.spec.clusterIP}'; echo`
Expected: the answer is `127.0.0.1`, not the ClusterIP printed on the last line.

- [ ] **Step 2: Create `metal/roles/coredns_rewrite/tasks/main.yml`**

```yaml
- name: Read the CoreDNS configuration
  kubernetes.core.k8s_info:
    kind: ConfigMap
    namespace: kube-system
    name: coredns
  register: coredns

- name: Add the rewrite rule
  vars:
    corefile: "{{ coredns.resources[0].data.Corefile }}"
  when: coredns_rewrite_rule not in corefile
  kubernetes.core.k8s:
    state: patched
    kind: ConfigMap
    namespace: kube-system
    name: coredns
    definition:
      data:
        Corefile: "{{ corefile | replace('    ready\n', '    ready\n    ' ~ coredns_rewrite_rule ~ '\n') }}"
  register: corefile_patch

- name: Restart CoreDNS to apply the rule now
  when: corefile_patch is changed
  kubernetes.core.k8s:
    state: patched
    kind: Deployment
    namespace: kube-system
    name: coredns
    definition:
      spec:
        template:
          metadata:
            annotations:
              homelab/corefile-patched: "{{ now(utc=true).isoformat() }}"
```

- [ ] **Step 3: Run the role from `metal/cluster.yml` only when the variable is set**

Replace the second play with:

```yaml
- name: Install Kubernetes addons
  hosts: localhost
  roles:
    - cilium
    - role: coredns_rewrite
      when: coredns_rewrite_rule is defined
```

- [ ] **Step 4: Set the rule in `metal/inventories/dev.yml`**

Under `all: vars:` add:

```yaml
    # Pods would otherwise resolve sandbox hostnames to their own loopback
    coredns_rewrite_rule: 'rewrite name regex (.*)\.127-0-0-1\.nip\.io ingress-nginx-controller.ingress-nginx.svc.cluster.local answer auto'
```

- [ ] **Step 5: Apply and verify**

Run:
```sh
make -C metal env=dev cluster
kubectl -n kube-system rollout status deploy/coredns --timeout=3m
kubectl run dnstest --rm -i --restart=Never --image=docker.io/library/busybox:1.37 -- nslookup auth.127-0-0-1.nip.io 2>&1 | grep -A1 '^Name:'
make -C metal env=dev cluster | grep -c 'Add the rewrite rule.*changed'
```
Expected:
- the answer is the ingress-nginx ClusterIP
- the second run reports `0` changes for the rewrite task, because the rule is already present

- [ ] **Step 6: Commit**

```bash
git add metal/roles/coredns_rewrite metal/cluster.yml metal/inventories/dev.yml
git commit -m "feat(metal): resolve sandbox hostnames to ingress-nginx inside the cluster

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Kanidm managed by kaniop

**Files:**
- Modify: `platform/kanidm/Chart.yaml`, `platform/kanidm/values.yaml`
- Create: `platform/kanidm/values-dev.yaml`, `platform/kanidm/templates/kanidm.yaml`, `platform/kanidm/templates/groups.yaml`, `platform/kanidm/templates/test-accounts.yaml`
- Delete: `platform/kanidm/templates/issuer.yaml`, `platform/kanidm/templates/certificate.yaml`
- Modify: `system/argocd/values-dev.yaml`

**Interfaces:**
- Produces:
  - `Kanidm/kanidm` (namespace `kanidm`) with admin Secret `kanidm-admin-passwords` (`IDM_ADMIN_PASSWORD`) and ingress host from `kanidm.domain`
  - `KanidmGroup` `homelab-admins`, `homelab-editors`, `homelab-users`
  - in dev, `KanidmPersonAccount` for the four test accounts, with `TokenCreated` events carrying `…/ui/reset?token=…`
  - OAuth2 clients are accepted from namespace `dex`

- [ ] **Step 1: Failing check**

Run: `scripts/render-chart platform/kanidm dev | yq 'select(.kind=="Kanidm" or .kind=="KanidmGroup") | .kind' | grep -v '^---' | wc -l`
Expected: `0`

- [ ] **Step 2: Replace `platform/kanidm/Chart.yaml`**

```yaml
apiVersion: v2
name: kanidm
version: 0.0.0
dependencies:
  - name: kaniop
    version: 0.18.0
    repository: oci://ghcr.io/pando85/helm-charts
```

- [ ] **Step 3: Replace `platform/kanidm/values.yaml`**

```yaml
kaniop: {}
kanidm:
  domain: auth.khuedoan.com
  image: docker.io/kanidm/server:1.11.2
  storage: 1Gi
# Only the sandbox sets members: KanidmGroup.members replaces the whole membership,
# and people are added with scripts/onboard-user.
groupMembers: {}
# Sandbox-only test accounts (no personal data)
testAccounts: []
```

- [ ] **Step 4: Create `platform/kanidm/values-dev.yaml`**

```yaml
kanidm:
  domain: auth.127-0-0-1.nip.io
groupMembers:
  homelab-admins: [dev-admin]
  homelab-editors: [dev-editor]
  homelab-users: [dev-viewer]
testAccounts:
  - dev-admin
  - dev-editor
  - dev-viewer
  - dev-outsider
```

- [ ] **Step 5: Create `platform/kanidm/templates/kanidm.yaml`**

```yaml
apiVersion: kaniop.rs/v1beta1
kind: Kanidm
metadata:
  name: kanidm
  namespace: {{ .Release.Namespace }}
spec:
  domain: {{ .Values.kanidm.domain }}
  image: {{ .Values.kanidm.image }}
  replicaGroups:
    - name: default
      replicas: 1
  storage:
    volumeClaimTemplate:
      spec:
        accessModes:
          - ReadWriteOnce
        resources:
          requests:
            storage: {{ .Values.kanidm.storage }}
  # cert-manager issues the ingress certificate; Kanidm serves the same secret (kanidm-tls)
  ingress:
    ingressClassName: nginx
    annotations:
      cert-manager.io/cluster-issuer: letsencrypt-prod
      nginx.ingress.kubernetes.io/backend-protocol: HTTPS
  # Dex owns its OAuth2 client in its own namespace
  oauth2ClientNamespaceSelector:
    matchLabels:
      kubernetes.io/metadata.name: dex
```

- [ ] **Step 6: Create `platform/kanidm/templates/groups.yaml`**

```yaml
{{- range $group := list "homelab-admins" "homelab-editors" "homelab-users" }}
---
apiVersion: kaniop.rs/v1beta1
kind: KanidmGroup
metadata:
  name: {{ $group }}
  namespace: {{ $.Release.Namespace }}
spec:
  kanidmRef:
    name: kanidm
  {{- with index $.Values.groupMembers $group }}
  members:
    {{- toYaml . | nindent 4 }}
  {{- end }}
{{- end }}
```

- [ ] **Step 7: Create `platform/kanidm/templates/test-accounts.yaml`**

```yaml
{{- range .Values.testAccounts }}
---
apiVersion: kaniop.rs/v1beta1
kind: KanidmPersonAccount
metadata:
  name: {{ . }}
  namespace: {{ $.Release.Namespace }}
spec:
  kanidmRef:
    name: kanidm
  personAttributes:
    displayname: {{ . }}
    mail:
      - {{ . }}@example.com
{{- end }}
```

- [ ] **Step 8: Delete the self-signed certificate**

Run: `git rm -q platform/kanidm/templates/issuer.yaml platform/kanidm/templates/certificate.yaml`

- [ ] **Step 9: Verify renders (Review Focus 3)**

Run:
```sh
scripts/render-chart platform/kanidm prod | yq 'select(.kind=="KanidmGroup") | .metadata.name + " members=" + (.spec.members | tostring)' | grep -v '^---'
scripts/render-chart platform/kanidm prod | yq 'select(.kind=="KanidmPersonAccount") | .metadata.name' | grep -v '^---' | wc -l
scripts/render-chart platform/kanidm dev | yq 'select(.kind=="KanidmGroup") | .metadata.name + " " + (.spec.members | join(","))' | grep -v '^---'
scripts/render-chart platform/kanidm dev | yq 'select(.kind=="KanidmPersonAccount") | .metadata.name' | grep -v '^---'
scripts/render-chart platform/kanidm prod | yq 'select(.kind=="Kanidm") | .spec.domain + " " + .spec.image'
scripts/render-chart platform/kanidm prod | yq 'select(.kind=="CustomResourceDefinition") | .metadata.name' | grep -c kaniop.rs
```
Expected:
- prod: three lines ending in `members=null`, and `0` persons
- dev: `homelab-admins dev-admin`, `homelab-editors dev-editor`, `homelab-users dev-viewer`, then the four account names
- prod Kanidm: `auth.khuedoan.com docker.io/kanidm/server:1.11.2`
- CRD count `≥ 4`

- [ ] **Step 10: Add to the allowlist, commit, push**

In `system/argocd/values-dev.yaml`, add `- path: platform/kanidm` after `- path: platform/global-secrets`.

```bash
git add platform/kanidm system/argocd/values-dev.yaml
git commit -m "feat(kanidm): deploy with kaniop, groups and sandbox test accounts in git

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git push
```

- [ ] **Step 11: Verify in the sandbox**

Run:
```sh
kubectl -n argocd wait --for=jsonpath='{.status.health.status}'=Healthy application/kanidm --timeout=20m
kubectl -n kanidm wait --for=condition=Initialized kanidm/kanidm --timeout=10m
kubectl -n kanidm get kanidmgroups,kanidmpersonaccounts
kubectl -n kanidm get secret kanidm-admin-passwords -o jsonpath='{.data.IDM_ADMIN_PASSWORD}' | wc -c
curl -sk -o /dev/null -w '%{http_code}\n' https://auth.127-0-0-1.nip.io/status
kubectl -n kanidm get events --field-selector involvedObject.name=dev-admin,reason=TokenCreated -o jsonpath='{.items[0].message}' | grep -o '/ui/reset?token=' 
kubectl -n kanidm get ingress -o custom-columns=NAME:.metadata.name,HOST:.spec.rules[0].host
```
Expected:
- the app is Healthy and the Kanidm is Initialized
- three groups and four persons, all ready
- a non-zero password length
- `200`
- `/ui/reset?token=`
- the ingress name and host `auth.127-0-0-1.nip.io`; record the ingress name for Task 7's smoke list

If `Initialized` never arrives, read `kubectl -n kanidm describe kanidm kanidm` and the operator logs (`kubectl -n kanidm logs deploy/kanidm-kaniop`, or the deployment named in `kubectl -n kanidm get deploy`) before changing anything.

---

### Task 5: onboard-user by group; remove the hacks

**Files:**
- Modify: `scripts/onboard-user`, `Makefile`
- Delete: `scripts/hacks`, `scripts/kanidm-reset-password`

**Interfaces:**
- Consumes: `kanidm-admin-passwords`, groups `homelab-*` (Task 4).
- Produces: `scripts/onboard-user <username> <fullname> <mail> <admins|editors|users>`.

- [ ] **Step 1: Failing check**

Run: `scripts/onboard-user probe "Probe User" probe@example.com users; echo "exit=$?"`
Expected: fails. The old script reads the ingress `kanidm` and adds the person to `editor`, which no longer exists.

- [ ] **Step 2: Replace `scripts/onboard-user`**

```sh
#!/bin/sh
# Usage: scripts/onboard-user <username> <fullname> <mail> <admins|editors|users>

set -eu

username="${1}"
fullname="${2}"
mail="${3}"
group="${4}"

case "${group}" in
    admins|editors|users) ;;
    *) echo "group must be one of: admins, editors, users" >&2; exit 1 ;;
esac

export KUBECONFIG="${KUBECONFIG:-./metal/kubeconfig.yaml}"
host="$(kubectl get kanidm --namespace kanidm kanidm --output jsonpath='{.spec.domain}')"
url="https://${host}"

# The sandbox CA is not in the system trust store; certificates from Let's Encrypt have no ca.crt
ca_file="$(mktemp)"
trap 'rm -f "${ca_file}"' EXIT
kubectl get secret --namespace kanidm kanidm-tls --output jsonpath='{.data.ca\.crt}' | base64 -d > "${ca_file}"
if [ -s "${ca_file}" ]; then
    export KANIDM_CA_PATH="${ca_file}"
fi

echo "Log in as idm_admin with this password:"
kubectl get secret --namespace kanidm kanidm-admin-passwords --output jsonpath='{.data.IDM_ADMIN_PASSWORD}' | base64 -d
echo

kanidm login --url "${url}" --name idm_admin
kanidm person create "${username}" "${fullname}" --url "${url}" --name idm_admin
kanidm person update "${username}" --url "${url}" --name idm_admin --mail "${mail}"
kanidm group add-members "homelab-${group}" "${username}" --url "${url}" --name idm_admin
kanidm person credential create-reset-token "${username}" --url "${url}" --name idm_admin
```

- [ ] **Step 3: Remove the hacks and `post-install`**

Run: `git rm -q scripts/hacks scripts/kanidm-reset-password`

In `Makefile`:
- change `default: metal system external smoke-test post-install clean` to `default: metal system external smoke-test clean`
- delete the `post-install:` target and its recipe line

- [ ] **Step 4: Verify against the sandbox, then remove the probe user**

`kanidm login` reads the password from a terminal, so run it under `script`:

```sh
pw=$(kubectl -n kanidm get secret kanidm-admin-passwords -o jsonpath='{.data.IDM_ADMIN_PASSWORD}' | base64 -d)
printf '%s\n' "$pw" | script -qec 'scripts/onboard-user probe "Probe User" probe@example.com users' /dev/null | tail -5
printf '%s\n' "$pw" | script -qec 'sh -c "export KANIDM_CA_PATH=\$(mktemp); kubectl -n kanidm get secret kanidm-tls -o jsonpath={.data.ca\\.crt} | base64 -d > \$KANIDM_CA_PATH; kanidm login --url https://auth.127-0-0-1.nip.io --name idm_admin && kanidm group list-members homelab-users --url https://auth.127-0-0-1.nip.io --name idm_admin && kanidm person delete probe --url https://auth.127-0-0-1.nip.io --name idm_admin"' /dev/null | tail -4
grep -n 'post-install\|hacks' Makefile; echo "makefile-checked"
```
Expected:
- the first command ends with a reset link (`…/ui/reset?token=…`)
- the member list contains `probe` (next to `dev-viewer`), and the delete succeeds
- `makefile-checked` with no matches above it

If the CLI cannot read the password through `script`, record a ruling and verify interactively, with the user, in Task 8.

- [ ] **Step 5: Commit**

```bash
git add scripts/onboard-user Makefile
git commit -m "feat(scripts): onboard users into homelab groups, drop the kanidm hacks

The kaniop operator now owns groups, OAuth2 clients and admin credentials.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Dex 0.26 with the Kanidm client, PKCE and the dev smoke client

**Files:**
- Modify: `platform/dex/Chart.yaml`, `platform/dex/values.yaml`, `platform/dex/templates/secret.yaml`
- Create: `platform/dex/values-dev.yaml`, `platform/dex/templates/kanidm-client.yaml`
- Modify: `system/argocd/values-dev.yaml`

**Interfaces:**
- Consumes:
  - Kanidm `kanidm/kanidm`, accepting OAuth2 clients from namespace `dex` (Task 4)
  - Secret `dex.smoke` (Task 2)
  - the CoreDNS rewrite (Task 3)
- Produces:
  - Dex at `https://dex.127-0-0-1.nip.io` with connector `kanidm`
  - static client `smoke` (secret from `dex.smoke`), and `grafana-sso` kept for 1c
  - Secret `dex-kanidm-oauth2-credentials` with `KANIDM_CLIENT_ID` and `KANIDM_CLIENT_SECRET`

- [ ] **Step 1: Failing check**

Run: `scripts/render-chart platform/dex dev | yq 'select(.kind=="KanidmOAuth2Client") | .metadata.name' | grep -v '^---' | wc -l`
Expected: `0`

- [ ] **Step 2: Bump the chart**

In `platform/dex/Chart.yaml`, set `version: 0.26.0`.

- [ ] **Step 3: Replace `platform/dex/values.yaml`**

```yaml
dex:
  config:
    issuer: https://dex.khuedoan.com
    storage:
      type: kubernetes
      config:
        inCluster: true
    oauth2:
      skipApprovalScreen: true
    connectors:
      - type: oidc
        id: kanidm
        name: Kanidm
        config:
          clientID: $KANIDM_CLIENT_ID
          clientSecret: $KANIDM_CLIENT_SECRET
          redirectURI: https://dex.khuedoan.com/callback
          issuer: https://auth.khuedoan.com/oauth2/openid/dex
          pkceChallenge: S256
          insecureEnableGroups: true
          getUserInfo: true
          scopes:
            - openid
            - profile
            - email
            - groups
    staticClients:
      - id: grafana-sso
        name: Grafana
        redirectURIs:
          - 'https://grafana.khuedoan.com/login/generic_oauth'
        secretEnv: GRAFANA_SSO_CLIENT_SECRET
  envFrom:
    - secretRef:
        name: dex-secrets
    # Created by kaniop from templates/kanidm-client.yaml
    - secretRef:
        name: dex-kanidm-oauth2-credentials
  ingress:
    enabled: true
    className: nginx
    annotations:
      cert-manager.io/cluster-issuer: letsencrypt-prod
    hosts:
      - host: &host dex.khuedoan.com
        paths:
          - path: /
            pathType: ImplementationSpecific
    tls:
      - secretName: dex-tls-certificate
        hosts:
          - *host
kanidmClient:
  dexHost: dex.khuedoan.com
# Sandbox-only client used by the identity tests
smokeClient: false
```

- [ ] **Step 4: Create `platform/dex/values-dev.yaml`**

```yaml
dex:
  config:
    issuer: https://dex.127-0-0-1.nip.io
    connectors:
      - type: oidc
        id: kanidm
        name: Kanidm
        config:
          clientID: $KANIDM_CLIENT_ID
          clientSecret: $KANIDM_CLIENT_SECRET
          redirectURI: https://dex.127-0-0-1.nip.io/callback
          issuer: https://auth.127-0-0-1.nip.io/oauth2/openid/dex
          pkceChallenge: S256
          insecureEnableGroups: true
          getUserInfo: true
          # The sandbox CA, from Dex's own CA-issued certificate
          rootCAs:
            - /etc/dex/dev-ca/ca.crt
          scopes:
            - openid
            - profile
            - email
            - groups
    staticClients:
      - id: grafana-sso
        name: Grafana
        redirectURIs:
          - 'https://grafana.127-0-0-1.nip.io/login/generic_oauth'
        secretEnv: GRAFANA_SSO_CLIENT_SECRET
      - id: smoke
        name: Smoke test
        redirectURIs:
          - 'https://smoke.127-0-0-1.nip.io/callback'
        secretEnv: SMOKE_CLIENT_SECRET
  volumes:
    - name: dev-ca
      secret:
        secretName: dex-tls-certificate
        items:
          - key: ca.crt
            path: ca.crt
  volumeMounts:
    - name: dev-ca
      mountPath: /etc/dex/dev-ca
      readOnly: true
  ingress:
    hosts:
      - host: &host dex.127-0-0-1.nip.io
        paths:
          - path: /
            pathType: ImplementationSpecific
    tls:
      - secretName: dex-tls-certificate
        hosts:
          - *host
kanidmClient:
  dexHost: dex.127-0-0-1.nip.io
smokeClient: true
```

- [ ] **Step 5: Create `platform/dex/templates/kanidm-client.yaml`**

```yaml
apiVersion: kaniop.rs/v1beta1
kind: KanidmOAuth2Client
metadata:
  name: dex
  namespace: {{ .Release.Namespace }}
spec:
  kanidmRef:
    name: kanidm
    namespace: kanidm
  displayname: Dex
  origin: https://{{ .Values.kanidmClient.dexHost }}
  redirectUrl:
    - https://{{ .Values.kanidmClient.dexHost }}/callback
  # First-party client managed by us
  disableConsentPrompt: true
  scopeMap:
    {{- range list "homelab-admins" "homelab-editors" "homelab-users" }}
    - group: {{ . }}
      scopes:
        - openid
        - profile
        - email
        - groups
    {{- end }}
  # Keep the variable names Dex's config already uses
  secretKeyAliases:
    clientId:
      - KANIDM_CLIENT_ID
    clientSecret:
      - KANIDM_CLIENT_SECRET
```

- [ ] **Step 6: Replace `platform/dex/templates/secret.yaml`**

```yaml
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: dex-secrets
  namespace: {{ .Release.Namespace }}
spec:
  secretStoreRef:
    kind: ClusterSecretStore
    name: global-secrets
  target:
    name: dex-secrets
  data:
    # Static clients; the Kanidm connector credentials come from kaniop
    - secretKey: GRAFANA_SSO_CLIENT_SECRET
      remoteRef:
        key: dex.grafana
        property: client_secret
    {{- if .Values.smokeClient }}
    - secretKey: SMOKE_CLIENT_SECRET
      remoteRef:
        key: dex.smoke
        property: client_secret
    {{- end }}
```

- [ ] **Step 7: Verify renders**

Run:
```sh
scripts/render-chart platform/dex dev | yq 'select(.kind=="KanidmOAuth2Client") | .spec.redirectUrl[0] + " " + (.spec.scopeMap | length | tostring)'
scripts/render-chart platform/dex dev | yq 'select(.kind=="ExternalSecret") | .spec.data[].secretKey' | grep -v '^---'
scripts/render-chart platform/dex prod | yq 'select(.kind=="ExternalSecret") | .spec.data[].secretKey' | grep -v '^---'
scripts/render-chart platform/dex dev | yq 'select(.kind=="Secret" and .metadata.name=="dex") | .data."config.yaml"' | base64 -d | yq '.connectors[0].config.issuer, .connectors[0].config.pkceChallenge, (.staticClients | map(.id) | join(","))'
scripts/render-chart platform/dex prod | yq 'select(.kind=="Secret" and .metadata.name=="dex") | .data."config.yaml"' | base64 -d | yq '.connectors[0].config.rootCAs, (.staticClients | map(.id) | join(","))'
```
Expected:
- `https://dex.127-0-0-1.nip.io/callback 3`
- dev keys `GRAFANA_SSO_CLIENT_SECRET` and `SMOKE_CLIENT_SECRET`; prod key only `GRAFANA_SSO_CLIENT_SECRET`
- dev config: `https://auth.127-0-0-1.nip.io/oauth2/openid/dex`, `S256`, `grafana-sso,smoke`
- prod config: `null`, `grafana-sso`

If the Dex chart stores the config somewhere other than Secret `dex` / key `config.yaml`, find it with `yq 'select(.data."config.yaml") | .kind + "/" + .metadata.name'` and adjust the two commands.

- [ ] **Step 8: Add to the allowlist, commit, push**

In `system/argocd/values-dev.yaml`, add `- path: platform/dex` after `- path: platform/kanidm`.

```bash
git add platform/dex system/argocd/values-dev.yaml
git commit -m "feat(dex): bump to 0.26.0, Kanidm client via kaniop with PKCE, sandbox smoke client

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git push
```

- [ ] **Step 9: Verify in the sandbox (Review Focus 5)**

Run:
```sh
kubectl -n argocd wait --for=jsonpath='{.status.health.status}'=Healthy application/dex --timeout=20m
kubectl -n dex rollout status deploy/dex --timeout=10m
kubectl -n dex get secret dex-kanidm-oauth2-credentials -o jsonpath='{.data}' | yq -p json 'keys'
kubectl -n dex get kanidmoauth2client dex -o yaml | yq '.status'
kubectl run oidctest --rm -i --restart=Never --image=docker.io/curlimages/curl:8.16.0 -- sh -c 'curl -sk -o /dev/null -w "%{http_code} " https://auth.127-0-0-1.nip.io/oauth2/openid/dex/.well-known/openid-configuration; curl -sk -o /dev/null -w "%{http_code}\n" https://dex.127-0-0-1.nip.io/.well-known/openid-configuration'
kubectl -n dex logs deploy/dex | grep -iE 'error|failed' | tail -5; echo "logs-checked"
sleep 120; kubectl -n dex get pods -o custom-columns=NAME:.metadata.name,RESTARTS:.status.containerStatuses[0].restartCount,READY:.status.containerStatuses[0].ready
```
Expected:
- dex is Healthy and the rollout is complete
- the Secret keys include `CLIENT_ID`, `CLIENT_SECRET`, `KANIDM_CLIENT_ID` and `KANIDM_CLIENT_SECRET`
- the client status is ready
- `200 200` from inside the cluster (this proves the CoreDNS rewrite and the Kanidm OIDC path)
- no connector errors before `logs-checked`
- the Dex pod is ready, and its restart count does not grow during the 2-minute wait

If Kanidm's discovery URL returns 404, the client name in Kanidm differs from `dex`. Read it from the KanidmOAuth2Client status, update both `issuer` lines (values.yaml and values-dev.yaml), and record a ruling.

---

### Task 7: Identity tests (Go + chromedp)

**Files:**
- Modify: `flake.nix`, `flake.lock`, `test/go.mod`, `test/go.sum`, `test/smoke_test.go`, `Makefile`
- Create: `test/identity_helpers_test.go`, `test/identity_test.go`

**Interfaces:**
- Consumes: everything above.
- Produces:
  - `ensureCredentials(t, name) credentials`
  - `(*totpClock).code(t, account, credentials) string`
  - `browserAuthorize(t, account, credentials) (code string, ok bool)`
  - `exchangeCode(t, code, secret) map[string]any`
  - `hasGroup(claims, group) bool`
  - tests `TestIdentityLogin` and `TestIdentityDenied`, plus unit tests `TestIdentityTOTPWaitsForNextStep` and `TestIdentityHasGroup`

- [ ] **Step 1: Add chromium and the Go modules**

In `flake.nix`, add `chromium` to `packages` (alphabetically, after `bmake`). Then:

```sh
nix develop -c true
cd test && go get github.com/chromedp/chromedp@v0.21.0 github.com/pquerna/otp@v1.5.0 github.com/stretchr/testify@latest && cd ..
```

- [ ] **Step 2: Write the unit tests first, in `test/identity_test.go`**

```go
package test

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIdentityTOTPWaitsForNextStep(t *testing.T) {
	c := credentials{TOTPSecret: "JBSWY3DPEHPK3PXP", Algorithm: "sha256", Digits: 6, Period: 30}
	t0 := time.Unix(1_800_000_000, 0) // start of a 30 s step
	now := t0
	var slept time.Duration
	clock := &totpClock{
		now:   func() time.Time { return now },
		sleep: func(d time.Duration) { slept += d; now = now.Add(d) },
		last:  map[string]uint64{},
	}

	first := clock.code(t, "dev-admin", c)
	second := clock.code(t, "dev-admin", c)

	assert.Equal(t, 30*time.Second, slept, "second code must wait for the next step")
	assert.NotEqual(t, first, second)
	assert.Equal(t, totpCode(t, c, t0.Add(30*time.Second)), second)
}

func TestIdentityHasGroup(t *testing.T) {
	assert.True(t, hasGroup(map[string]any{"groups": []any{"homelab-admins"}}, "homelab-admins"))
	assert.True(t, hasGroup(map[string]any{"groups": []any{"homelab-admins@auth.127-0-0-1.nip.io"}}, "homelab-admins"))
	assert.False(t, hasGroup(map[string]any{"groups": []any{"homelab-admins-old"}}, "homelab-admins"))
	assert.False(t, hasGroup(map[string]any{}, "homelab-admins"))
}

func TestIdentityLogin(t *testing.T) {
	if os.Getenv("ENV") != "dev" {
		t.Skip("identity tests run against the sandbox only")
	}
	secret := smokeClientSecret(t)

	for _, account := range testAccounts {
		if account.group == "" {
			continue
		}
		t.Run(account.name, func(t *testing.T) {
			c := ensureCredentials(t, account.name)

			code, ok := browserAuthorize(t, account.name, c)
			require.True(t, ok, "no authorization code for %s", account.name)

			claims := exchangeCode(t, code, secret)
			t.Logf("groups claim for %s: %v", account.name, claims["groups"])
			assert.Equal(t, account.name+"@example.com", claims["email"])
			assert.True(t, hasGroup(claims, account.group), "groups %v lack %s", claims["groups"], account.group)
		})
	}
}

func TestIdentityDenied(t *testing.T) {
	if os.Getenv("ENV") != "dev" {
		t.Skip("identity tests run against the sandbox only")
	}
	c := ensureCredentials(t, "dev-outsider")

	_, ok := browserAuthorize(t, "dev-outsider", c)

	assert.False(t, ok, "an account in no homelab group must not get an authorization code")
}
```

- [ ] **Step 3: Run the unit tests and watch them fail**

Run: `cd test && go test -run 'TestIdentity(TOTPWaitsForNextStep|HasGroup)' ./...`
Expected: FAIL to build with `undefined: credentials`, `undefined: totpClock`, `undefined: hasGroup`, and the other helpers.

- [ ] **Step 4: Create `test/identity_helpers_test.go`**

```go
package test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"
)

const (
	kanidmURL         = "https://auth.127-0-0-1.nip.io"
	dexURL            = "https://dex.127-0-0-1.nip.io"
	smokeRedirect     = "https://smoke.127-0-0-1.nip.io/callback"
	credentialsSecret = "identity-test-credentials"
)

type testAccount struct {
	name  string
	group string // empty: the account is in no homelab group
}

var testAccounts = []testAccount{
	{"dev-admin", "homelab-admins"},
	{"dev-editor", "homelab-editors"},
	{"dev-viewer", "homelab-users"},
	{"dev-outsider", ""},
}

// credentials of a sandbox test account, stored in a Secret so reruns can log in again
type credentials struct {
	Password   string `json:"password"`
	TOTPSecret string `json:"totpSecret"` // base32 without padding
	Algorithm  string `json:"algorithm"`  // sha1, sha256 or sha512
	Digits     int    `json:"digits"`
	Period     uint   `json:"period"`
}

// The sandbox uses a local CA; these tests only ever run against it.
var insecureClient = &http.Client{
	Timeout:       30 * time.Second,
	Transport:     &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func kanidmNamespace() *k8s.KubectlOptions { return k8s.NewKubectlOptions("", "", "kanidm") }

func randomPassword(t *testing.T) string {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	var b strings.Builder
	for i := 0; i < 32; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		require.NoError(t, err)
		b.WriteByte(alphabet[n.Int64()])
	}
	return b.String()
}

// ensureCredentials returns stored credentials, or enrols new ones with the reset token kaniop published.
func ensureCredentials(t *testing.T, account string) credentials {
	stored, err := k8s.RunKubectlAndGetOutputE(t, kanidmNamespace(),
		"get", "secret", credentialsSecret, "-o", "jsonpath={.data."+account+"}")
	if err == nil && stored != "" {
		raw, err := base64.StdEncoding.DecodeString(stored)
		require.NoError(t, err)
		var c credentials
		require.NoError(t, json.Unmarshal(raw, &c))
		return c
	}

	c := enrol(t, account, resetToken(t, account))

	encoded, err := json.Marshal(c)
	require.NoError(t, err)
	if _, err := k8s.RunKubectlAndGetOutputE(t, kanidmNamespace(), "get", "secret", credentialsSecret); err != nil {
		k8s.RunKubectl(t, kanidmNamespace(), "create", "secret", "generic", credentialsSecret)
	}
	patch, err := json.Marshal(map[string]any{"stringData": map[string]string{account: string(encoded)}})
	require.NoError(t, err)
	k8s.RunKubectl(t, kanidmNamespace(), "patch", "secret", credentialsSecret, "--type", "merge", "-p", string(patch))
	return c
}

var resetTokenPattern = regexp.MustCompile(`/ui/reset\?token=([^\s"&]+)`)

// resetToken reads the newest credential reset link kaniop published for the account.
func resetToken(t *testing.T, account string) string {
	var token string
	require.Eventually(t, func() bool {
		out, err := k8s.RunKubectlAndGetOutputE(t, kanidmNamespace(), "get", "events",
			"--field-selector", "involvedObject.name="+account+",reason=TokenCreated",
			"--sort-by", ".lastTimestamp", "-o", "jsonpath={range .items[*]}{.message}{\"\\n\"}{end}")
		if err != nil {
			return false
		}
		matches := resetTokenPattern.FindAllStringSubmatch(out, -1)
		if len(matches) == 0 {
			return false
		}
		token = matches[len(matches)-1][1]
		return true
	}, 5*time.Minute, 10*time.Second, "no TokenCreated event for %s", account)
	return token
}

func kanidmPost(t *testing.T, path string, body any, out any) {
	payload, err := json.Marshal(body)
	require.NoError(t, err)
	resp, err := insecureClient.Post(kanidmURL+path, "application/json", bytes.NewReader(payload))
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "POST %s: %s", path, raw)
	if out != nil {
		require.NoError(t, json.Unmarshal(raw, out), "POST %s: %s", path, raw)
	}
}

type cuSession struct {
	Token string `json:"token"`
}

type cuStatus struct {
	MfaRegState json.RawMessage `json:"mfaregstate"`
}

func (s cuStatus) state() string {
	var name string
	if json.Unmarshal(s.MfaRegState, &name) == nil {
		return name
	}
	var tagged map[string]json.RawMessage
	if json.Unmarshal(s.MfaRegState, &tagged) == nil {
		for k := range tagged {
			return k
		}
	}
	return ""
}

// enrol sets a password and a TOTP through Kanidm's credential update API (Kanidm requires MFA for persons).
func enrol(t *testing.T, account, token string) credentials {
	var exchanged []json.RawMessage
	kanidmPost(t, "/v1/credential/_exchange_intent", token, &exchanged)
	require.Len(t, exchanged, 2)
	var session cuSession
	require.NoError(t, json.Unmarshal(exchanged[0], &session))

	update := func(request any) cuStatus {
		var status cuStatus
		kanidmPost(t, "/v1/credential/_update", []any{request, session}, &status)
		return status
	}

	c := credentials{Password: randomPassword(t)}
	update(map[string]string{"password": c.Password})

	generated := update("totpgenerate")
	var reg struct {
		TotpCheck struct {
			Secret []int  `json:"secret"`
			Algo   string `json:"algo"`
			Step   uint   `json:"step"`
			Digits int    `json:"digits"`
		} `json:"TotpCheck"`
	}
	require.NoError(t, json.Unmarshal(generated.MfaRegState, &reg), "mfaregstate: %s", generated.MfaRegState)
	secret := make([]byte, len(reg.TotpCheck.Secret))
	for i, b := range reg.TotpCheck.Secret {
		secret[i] = byte(b)
	}
	c.TOTPSecret = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
	c.Algorithm, c.Period, c.Digits = reg.TotpCheck.Algo, reg.TotpCheck.Step, reg.TotpCheck.Digits

	code, err := strconv.Atoi(clock.code(t, account, c))
	require.NoError(t, err)
	verified := update(map[string][]any{"totpverify": {code, "smoke-test"}})
	if verified.state() == "TotpInvalidSha1" {
		verified = update("totpacceptsha1")
	}
	require.Equal(t, "None", verified.state(), "TOTP not accepted: %s", verified.MfaRegState)

	kanidmPost(t, "/v1/credential/_commit", session, nil)
	return c
}

func totpCode(t *testing.T, c credentials, at time.Time) string {
	algorithms := map[string]otp.Algorithm{"sha1": otp.AlgorithmSHA1, "sha256": otp.AlgorithmSHA256, "sha512": otp.AlgorithmSHA512}
	algorithm, ok := algorithms[c.Algorithm]
	require.True(t, ok, "unknown TOTP algorithm %q", c.Algorithm)
	digits := otp.DigitsSix
	if c.Digits == 8 {
		digits = otp.DigitsEight
	}
	code, err := totp.GenerateCodeCustom(c.TOTPSecret, at, totp.ValidateOpts{Period: c.Period, Digits: digits, Algorithm: algorithm})
	require.NoError(t, err)
	return code
}

// totpClock hands out codes from a step not used before, because Kanidm rejects a reused step.
type totpClock struct {
	now   func() time.Time
	sleep func(time.Duration)
	last  map[string]uint64
}

var clock = &totpClock{now: time.Now, sleep: time.Sleep, last: map[string]uint64{}}

func (k *totpClock) code(t *testing.T, account string, c credentials) string {
	for {
		now := k.now()
		step := uint64(now.Unix()) / uint64(c.Period)
		if step > k.last[account] {
			k.last[account] = step
			return totpCode(t, c, now)
		}
		k.sleep(time.Duration(uint64(c.Period)-uint64(now.Unix())%uint64(c.Period)) * time.Second)
	}
}

// browserAuthorize logs in through Dex and Kanidm and returns the code sent to the smoke client.
func browserAuthorize(t *testing.T, account string, c credentials) (string, bool) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("ignore-certificate-errors", true))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelAlloc()
	ctx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()
	ctx, cancelTimeout := context.WithTimeout(ctx, 3*time.Minute)
	defer cancelTimeout()

	codes := make(chan string, 1)
	chromedp.ListenTarget(ctx, func(ev any) {
		if e, ok := ev.(*network.EventRequestWillBeSent); ok && strings.HasPrefix(e.Request.URL, smokeRedirect) {
			if u, err := url.Parse(e.Request.URL); err == nil && u.Query().Get("code") != "" {
				select {
				case codes <- u.Query().Get("code"):
				default:
				}
			}
		}
	})

	authURL := dexURL + "/auth?" + url.Values{
		"client_id":     {"smoke"},
		"redirect_uri":  {smokeRedirect},
		"response_type": {"code"},
		"scope":         {"openid email profile groups"},
		"state":         {"identity-test"},
	}.Encode()

	require.NoError(t, chromedp.Run(ctx,
		network.Enable(),
		chromedp.Navigate(authURL),
		chromedp.WaitVisible(`input#username`),
		chromedp.SendKeys(`input#username`, account+"\n"),
		chromedp.WaitVisible(`input#password`),
		chromedp.SendKeys(`input#password`, c.Password+"\n"),
		chromedp.WaitVisible(`input#totp`),
	), "login form for %s", account)
	require.NoError(t, chromedp.Run(ctx, chromedp.SendKeys(`input#totp`, clock.code(t, account, c)+"\n")))

	select {
	case code := <-codes:
		return code, true
	case <-time.After(30 * time.Second):
		return "", false
	}
}

func smokeClientSecret(t *testing.T) string {
	secret := k8s.GetSecret(t, k8s.NewKubectlOptions("", "", "global-secrets"), "dex.smoke")
	return string(secret.Data["client_secret"])
}

// exchangeCode trades the code for tokens at Dex and returns the ID token claims.
func exchangeCode(t *testing.T, code, secret string) map[string]any {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {smokeRedirect}}
	req, err := http.NewRequest(http.MethodPost, dexURL+"/token", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.SetBasicAuth("smoke", secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := insecureClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "token exchange: %s", raw)

	var tokens struct {
		IDToken string `json:"id_token"`
	}
	require.NoError(t, json.Unmarshal(raw, &tokens))
	parts := strings.Split(tokens.IDToken, ".")
	require.Len(t, parts, 3)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(payload, &claims))
	return claims
}

// hasGroup accepts Kanidm's short group names and SPNs (name@domain).
func hasGroup(claims map[string]any, group string) bool {
	groups, _ := claims["groups"].([]any)
	for _, g := range groups {
		if s, ok := g.(string); ok && (s == group || strings.HasPrefix(s, group+"@")) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Run the unit tests**

Run: `cd test && go mod tidy && go vet ./... && go test -run 'TestIdentity(TOTPWaitsForNextStep|HasGroup)' ./...`
Expected: PASS, and no vet errors.

- [ ] **Step 6: Add kanidm and dex to the dev smoke list**

In `test/smoke_test.go`, change the dev branch of `smokeApps` to the following, using the Kanidm ingress name recorded in Task 4 Step 11 (`kanidm` assumed here):

```go
	if env == "dev" {
		return []smokeApp{
			{"argocd-server", "argocd"},
			{"excalidraw", "excalidraw"},
			{"kanidm", "kanidm"},
			{"dex", "dex"},
		}
	}
```

and update `TestSmokeAppsForEnv` in `test/apps_test.go` so its `dev` slice matches the four entries.

- [ ] **Step 7: Run the smoke filter from `make dev`**

In the root `Makefile`:
- add `smoke_filter ?= Smoke` below the `KUBE_CONFIG_PATH` line
- change the `smoke-test` recipe to `make -C test filter="$(smoke_filter)"`
- change the `dev` recipe to `$(MAKE) env=dev ENV=dev INSECURE_SKIP_VERIFY=1 smoke_filter='Smoke|Identity' metal system smoke-test`

- [ ] **Step 8: Probe the Kanidm login form before the first live run**

Selectors `input#username`, `input#password` and `input#totp` come from Kanidm's UI and must be checked against 1.11.2:

```sh
chromium --headless=new --ignore-certificate-errors --dump-dom 'https://auth.127-0-0-1.nip.io/ui/login' 2>/dev/null | grep -oE '<input[^>]+>' | head
```
Expected: an input with `id="username"`. If the ids differ, change the selectors in `browserAuthorize` and record a ruling. The password and TOTP ids are confirmed by the live run in Step 9: on failure, the `login form for …` message says which wait timed out.

- [ ] **Step 9: Live run**

Run: `make env=dev ENV=dev INSECURE_SKIP_VERIFY=1 smoke_filter='Smoke|Identity' smoke-test 2>&1 | tail -15`
Expected:
- PASS for `TestSmoke/{argocd-server,excalidraw,kanidm,dex}`, `TestIdentityLogin/{dev-admin,dev-editor,dev-viewer}` and `TestIdentityDenied`
- the log shows the `groups claim for …` lines; copy their format into the ledger for 1c

Then run it again at once:
Expected: PASS again, with stored credentials and no new enrolment (Review Focus 1).

- [ ] **Step 10: Commit and push**

```bash
git add flake.nix flake.lock test Makefile
git commit -m "test: log in through Dex and Kanidm with sandbox test accounts

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git push
```

---

### Task 8: Full acceptance run and RAM table

**Files:** none (fixes go back to the task that owns the file)

- [ ] **Step 1: From scratch**

Run: `make dev-clean && time make dev` (in the background; it can take 20+ minutes)
Expected: exit 0; all smoke and identity tests PASS (criteria 1, 4).

- [ ] **Step 2: Rerun (Review Focus 1)**

Run: `make dev`
Expected: exit 0 in a few minutes, identity tests PASS with stored credentials.

- [ ] **Step 3: Applications (criterion 2)**

Run: `kubectl -n argocd get applications`
Expected: 9 apps, all `Synced` / `Healthy`.

- [ ] **Step 4: No v1beta1, renders pass (criterion 3)**

Run:
```sh
grep -rln 'external-secrets.io/v1beta1' apps platform system | wc -l
for d in platform/external-secrets platform/global-secrets platform/kanidm platform/dex apps/paperless apps/tailscale apps/wireguard platform/gitea platform/grafana platform/renovate platform/woodpecker platform/zot; do for e in prod dev; do scripts/render-chart $d $e >/dev/null || echo "FAIL $d $e"; done; done; echo renders-checked
```
Expected: `0`, then only `renders-checked`.

- [ ] **Step 5: Prometheus targets (criterion 5)**

Run:
```sh
kubectl -n monitoring-system port-forward svc/monitoring-system-kube-pro-prometheus 9090 >/dev/null 2>&1 &
sleep 4
curl -s 'http://127.0.0.1:9090/api/v1/targets?state=active' | jq -r '.data.activeTargets[] | select(.health!="up") | .labels.job + " " + .lastError'
```
Expected: empty.

- [ ] **Step 6: RAM per namespace (criterion 6)**

Run (with the port-forward still open):
```sh
curl -s 'http://127.0.0.1:9090/api/v1/query' --data-urlencode 'query=sort_desc(sum by (namespace) (container_memory_working_set_bytes{container!=""}))' | jq -r '.data.result[] | "| \(.metric.namespace) | \((.value[1] | tonumber) / 1048576 | floor) MiB |"'
docker stats --no-stream homelab-dev-control-plane --format '{{.MemUsage}}'
kill %1
```
Expected: one table row per namespace plus the node total; keep the output for Task 9.

- [ ] **Step 7: Scripts gone, onboarding works (criterion 7)**

Run: `ls scripts/hacks scripts/kanidm-reset-password 2>&1 | grep -c 'No such file'`
Expected: `2`. `onboard-user` was verified in Task 5; if that was deferred, run it with the user now.

---

### Task 9: Docs

**Files:**
- Modify: `docs/installation/sandbox.md`, `docs/concepts/secrets-management.md`, `docs/getting-started/user-onboarding.md`, `CLAUDE.md`

- [ ] **Step 1: `docs/installation/sandbox.md`**

Add after the `## Explore` list:

```markdown
### Identity

Kanidm (<https://auth.127-0-0-1.nip.io>) and Dex (<https://dex.127-0-0-1.nip.io>) run in the sandbox with three groups:

| Group | Test account |
|---|---|
| `homelab-admins` | `dev-admin` |
| `homelab-editors` | `dev-editor` |
| `homelab-users` | `dev-viewer` |
| (none, refused) | `dev-outsider` |

The identity tests give each test account a password and TOTP the first time they run, and store them in the `identity-test-credentials` Secret in namespace `kanidm`:

```sh
kubectl -n kanidm get secret identity-test-credentials -o jsonpath='{.data.dev-admin}' | base64 -d
```

Use the `totpSecret` there in any authenticator app to log in by hand.
```

Add a `## Resource usage` section before `## Clean up` with the table from Task 8 Step 6, under the heading line `Measured with all sandbox components running (RAM per namespace):`.

- [ ] **Step 2: `docs/concepts/secrets-management.md`**

Replace the description of the secret generator Job with this section (keep the rest of the page):

```markdown
## Generated secrets

Random secrets (admin passwords, OAuth2 client secrets) are listed in `platform/global-secrets/values.yaml`. For each one, an external-secrets `Password` generator creates a Secret in namespace `global-secrets` once, and the `global-secrets` ClusterSecretStore shares it with the apps that reference it.

They are never rotated automatically: both sides of a client secret would have to restart together, and admin passwords are stored in the app's database on first start.

To rotate one by hand:

1. `kubectl -n global-secrets delete secret <name>`. external-secrets generates a new value.
2. Restart every app that uses it (for example `kubectl -n dex rollout restart deploy/dex` and the client app).
3. For an admin password, also change it inside the app to the new value.

Kanidm OAuth2 client secrets are generated by the kaniop operator instead, as `<client>-kanidm-oauth2-credentials` in the client's namespace.
```

- [ ] **Step 3: `docs/getting-started/user-onboarding.md`**

Replace the command examples with:

```markdown
Add a person to one of the three groups (`admins`, `editors` or `users`):

```sh
./scripts/onboard-user <username> "<Full Name>" <email> <admins|editors|users>
```

The script prints the `idm_admin` password for the login prompt and ends with a credential reset link to send to the person. Kanidm requires a password plus TOTP or a passkey.
```

- [ ] **Step 4: `CLAUDE.md`**

- Delete the checklist line starting `- kanidm CLI in the dev shell is 1.11;`.
- Under `## Sandbox`, add:

```markdown
- Identity config is in git: groups and OAuth2 clients are kaniop resources (`platform/kanidm`, `platform/dex/templates/kanidm-client.yaml`). People are created with `scripts/onboard-user`; never put personal data in the repo. Production `KanidmGroup`s must not set `members` (it replaces the whole membership).
- In the sandbox, CoreDNS rewrites `*.127-0-0-1.nip.io` to ingress-nginx so pods can reach Kanidm and Dex.
- Dex has a sandbox-only client `smoke` used by the identity tests (`make dev` runs `Smoke|Identity`).
```

- [ ] **Step 5: Check and commit**

Run: `mkdocs build --strict -d "$(mktemp -d)" 2>&1 | grep -E 'WARNING|ERROR' ; echo docs-checked`
Expected: only `docs-checked`.

```bash
git add docs/installation/sandbox.md docs/concepts/secrets-management.md docs/getting-started/user-onboarding.md CLAUDE.md
git commit -m "docs: sandbox identity, secret rotation and onboarding by group

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git push
```
