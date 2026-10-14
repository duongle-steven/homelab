# Compact apps layout (step 4) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the 11 app-template wrapper charts under `apps/` into `apps/<name>.yaml` values files deployed by an ApplicationSet `apps` that pins app-template once and passes `global.domain` and `global.publicGateway`, without changing what gets deployed.

**Architecture:** A second ApplicationSet in `system/argocd` (git files generator, multi-source: app-template chart + `$values` ref) creates one Application per file. `scripts/render-chart` renders an app file the same way, reading the chart and parameters from the ApplicationSet values. Every converted app is checked against a baseline render of its old wrapper chart.

**Tech Stack:** Argo CD ApplicationSet (`argocd-apps` chart 2.0.6, `goTemplate`), app-template 5.3.0, Helm, yq, Python (comparison script, not committed).

**Spec:** `docs/superpowers/specs/2026-10-14-apps-layout-design.md`

## Global Constraints

- Branch `feat/apps-layout` (spec committed there). PR into `dev`; merge only with the user's go-ahead. Commits end with:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01GZDRNK1oUcGXXSC3R6dhN2
  ```
- Run inside the dev shell. Never `rm` with variables or inside `sh -c` (the harness blocks it); use literal paths.
- app-template **5.3.0**, pinned only in the ApplicationSet `apps` (prod and dev sources).
- Parameters: prod `global.domain=khuedoan.com`, `global.publicGateway=homelab-public`; sandbox `global.domain=127-0-0-1.nip.io`, `global.publicGateway=homelab`.
- Application name = Helm release = namespace = file name without `.yaml`.
- Domain templating: `<host>.khuedoan.com` → `<host>.{{ .Values.global.domain }}` for this homelab's own hosts (routes, env, homepage links). Keep literal: the blog image `registry.khuedoan.com/blog`, `homelab.khuedoan.com` (upstream's docs site), `horus.khuedoan.com` (a WireGuard peer), `github.com/khuedoan/...`, peer comments.
- Public apps use `parentRefs[].name: '{{ .Values.global.publicGateway }}'` (excalidraw, blog). matrix stays `apps/matrix/` (chart, synced by `root`).
- `ExternalSecret`s through app-template set `target.deletionPolicy: Retain` (the ESO default the old templates had; app-template defaults to `Delete`).
- Workspace for baselines and the comparison script: `.superpowers/sdd/2026-10-14-apps-layout/` (git-ignored).

## Review Focus

1. **Fields app-template does not template:** a literal `{{ .Values.global.domain }}` or `{{ .Values.global.publicGateway }}` in a rendered object. → every task's comparison shows the substituted value; Task 7 greps renders for `{{ .Values`.
2. **Silent defaults of app-template 5 for apps moving from 3.x:** tailscale and wireguard get `automountServiceAccountToken: false` and a ServiceAccount. tailscale needs the token (`TS_KUBE_SECRET`). → Task 6 sets it at pod level and checks the render.
3. **ESO templates inside `externalSecrets`:** app-template does not template `externalSecrets.*.target`, so wireguard's backtick escape must go. → Task 6 compares `wg0.conf` with the baseline.
4. **ApplicationSet handover on the running sandbox:** `root` deletes the five apps' resources, then `apps` recreates them. → Task 9 checks Applications and reruns `make dev`.
5. **Production-only apps** (blog, jellyfin, ollama, paperless, tailscale, wireguard) are covered only by render comparison. → expected differences are listed per task; anything else is a bug.

---

## File Structure

| File | Responsibility |
|---|---|
| `system/argocd/values.yaml` | ApplicationSet `apps` (prod), `&syncPolicy` anchor on `root` |
| `system/argocd/values-dev.yaml` | sandbox `apps` generator and parameters; `root` allowlist without apps |
| `system/argocd/values-seed.yaml` | first-install `apps` repository |
| `scripts/render-chart` | also renders `apps/<name>.yaml` |
| `apps/<name>.yaml` (11 files) | app-template values |
| `apps/<name>/` (11 dirs) | deleted |
| `docs/installation/sandbox.md`, `docs/getting-started/vpn-setup.md`, `CLAUDE.md` | docs |

---

### Task 1: Baseline renders and comparison script

**Files:**
- Create (workspace, not committed): `.superpowers/sdd/2026-10-14-apps-layout/baseline/<app>-<env>.yaml`, `.superpowers/sdd/2026-10-14-apps-layout/norm.py`

**Interfaces:**
- Produces: baseline renders of the 11 wrapper charts for `prod` and `dev`; `norm.py <file>` prints the documents sorted by kind and name with `helm.sh/chart` labels removed.

- [ ] **Step 1: Baselines** (before touching `apps/`):

```bash
W=.superpowers/sdd/2026-10-14-apps-layout; mkdir -p $W/baseline
for a in actualbudget blog excalidraw homepage jellyfin ollama pairdrop paperless speedtest tailscale wireguard; do
  for e in prod dev; do scripts/render-chart apps/$a $e > $W/baseline/$a-$e.yaml; done
done
ls $W/baseline | wc -l
```
Expected: `22`.

- [ ] **Step 2: Comparison script** `$W/norm.py`:

```python
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
def strip(o):
    if isinstance(o, dict):
        return {k: strip(v) for k, v in o.items() if k != "helm.sh/chart"}
    if isinstance(o, list):
        return [strip(v) for v in o]
    return o
docs = sorted((strip(d) for d in docs), key=lambda d: (d.get("kind", ""), d.get("metadata", {}).get("name", "")))
print(yaml.safe_dump_all(docs, sort_keys=True, width=300))
```

Check: `python3 $W/norm.py $W/baseline/jellyfin-prod.yaml | grep -c '^kind:'` prints the number of objects (≥ 9).

---

### Task 2: `scripts/render-chart` renders app files

**Files:**
- Modify: `scripts/render-chart`

**Interfaces:**
- Consumes: `.argocd-apps.applicationsets.apps.template.spec.sources[0]` in `system/argocd/values.yaml` / `values-dev.yaml` (Task 3).
- Produces: `scripts/render-chart apps/<name>.yaml [env]`.

- [ ] **Step 1: Show it fails today:** `scripts/render-chart apps/excalidraw/values.yaml` → Expected: an error (it expects a chart directory).

- [ ] **Step 2: Implement.** Replace `scripts/render-chart` with:

```sh
#!/bin/sh
# Render a component the same way ArgoCD does.
# Usage: scripts/render-chart <chart-dir> [env]   wrapper chart: values.yaml, then values-<env>.yaml if it exists
#        scripts/render-chart apps/<name>.yaml [env]   app file: app-template with the ApplicationSet "apps" parameters
set -eu
target="$1"
env="${2:-prod}"
argocd=system/argocd
set --
# ArgoCD passes the cluster's API versions; charts pick Gateway API versions from them
set -- "$@" --api-versions gateway.networking.k8s.io/v1
for kind in Gateway HTTPRoute TCPRoute BackendTLSPolicy ReferenceGrant; do
  set -- "$@" --api-versions "gateway.networking.k8s.io/v1/$kind"
done

if [ -f "$target" ]; then
  name="$(basename "$target" .yaml)"
  # The sandbox restates the ApplicationSet source in values-dev.yaml
  source_of() {
    query=".argocd-apps.applicationsets.apps.template.spec.sources[0]$1"
    if [ "$env" != prod ] && [ "$(yq "$query" "$argocd/values-$env.yaml" 2>/dev/null)" != null ]; then
      yq "$query" "$argocd/values-$env.yaml"
    else
      yq "$query" "$argocd/values.yaml"
    fi
  }
  for parameter in $(source_of '.helm.parameters[] | .name + "=" + .value'); do
    set -- "$@" --set-string "$parameter"
  done
  exec helm template "$name" "$(source_of .chart)" --repo "$(source_of .repoURL)" \
    --version "$(source_of .targetRevision)" --namespace "$name" --include-crds -f "$target" "$@"
fi

name="$(basename "$target")"
[ -f "$target/values.yaml" ] && set -- "$@" -f "$target/values.yaml"
[ "$env" != prod ] && [ -f "$target/values-$env.yaml" ] && set -- "$@" -f "$target/values-$env.yaml"
helm dependency update "$target" >/dev/null
helm template "$name" "$target" --namespace "$name" --include-crds "$@"
```

- [ ] **Step 3: Verify the chart path still works:** `scripts/render-chart system/gateway dev | yq 'select(.kind=="Gateway") | .metadata.name'` → `homelab`, `homelab-public`; `shellcheck scripts/render-chart` clean. (The file path is verified after Task 3.)

- [ ] **Step 4: Commit:** `git commit -m "build: render-chart renders app-template app files"`.

---

### Task 3: ApplicationSet `apps`

**Files:**
- Modify: `system/argocd/values.yaml`, `system/argocd/values-dev.yaml`, `system/argocd/values-seed.yaml`

**Interfaces:**
- Produces: ApplicationSet `apps` (goTemplate) generating Applications named after `apps/*.yaml`.

- [ ] **Step 1: Prod.** In `system/argocd/values.yaml`, change `root`'s `syncPolicy:` to `syncPolicy: &syncPolicy`, and append under `argocd-apps.applicationsets`:

```yaml
    # One Application per app-template values file apps/<name>.yaml (apps/matrix/ is a chart, synced by root)
    apps:
      namespace: argocd
      goTemplate: true
      goTemplateOptions:
        - missingkey=error
      generators:
        - git:
            repoURL: *repoURL
            revision: *revision
            files:
              - path: apps/*.yaml
      template:
        metadata:
          name: '{{ .path.filename | trimSuffix ".yaml" }}'
        spec:
          destination:
            name: in-cluster
            namespace: '{{ .path.filename | trimSuffix ".yaml" }}'
          project: default  # TODO
          sources:
            - repoURL: https://bjw-s-labs.github.io/helm-charts
              chart: app-template
              targetRevision: 5.3.0
              helm:
                releaseName: '{{ .path.filename | trimSuffix ".yaml" }}'
                valueFiles:
                  - '$values/apps/{{ .path.filename }}'
                # App files template hosts as <app>.{{ .Values.global.domain }}; public apps attach to
                # the Gateway named by global.publicGateway
                parameters:
                  - name: global.domain
                    value: khuedoan.com
                  - name: global.publicGateway
                    value: homelab-public
            - repoURL: *repoURL
              targetRevision: *revision
              ref: values
          syncPolicy: *syncPolicy
```

- [ ] **Step 2: Sandbox.** In `system/argocd/values-dev.yaml`, remove the five `- path: apps/...` lines from `root`'s allowlist and append under `argocd-apps.applicationsets` (lists are restated; `syncPolicy` and the rest merge from `values.yaml`):

```yaml
    apps:
      generators:
        - git:
            repoURL: *repoURL
            revision: *revision
            files:
              - path: apps/actualbudget.yaml
              - path: apps/excalidraw.yaml
              - path: apps/homepage.yaml
              - path: apps/pairdrop.yaml
              - path: apps/speedtest.yaml
      template:
        spec:
          sources:
            - repoURL: https://bjw-s-labs.github.io/helm-charts
              chart: app-template
              targetRevision: 5.3.0
              helm:
                releaseName: '{{ .path.filename | trimSuffix ".yaml" }}'
                valueFiles:
                  - '$values/apps/{{ .path.filename }}'
                parameters:
                  - name: global.domain
                    value: 127-0-0-1.nip.io
                  # No Cloudflare Tunnel in the sandbox: public apps use the LAN Gateway
                  - name: global.publicGateway
                    value: homelab
            - repoURL: *repoURL
              targetRevision: *revision
              ref: values
```

- [ ] **Step 3: Seed.** In `system/argocd/values-seed.yaml`, add under `argocd-apps.applicationsets` an `apps` entry whose generator and `ref: values` source use the seed's `*repoURL`/`*revision` (restate `generators` and `template.spec.sources` like Step 2, with the prod parameters).

- [ ] **Step 4: Verify.**

```bash
for e in prod dev; do scripts/render-chart system/argocd $e | yq 'select(.kind=="ApplicationSet") | [.metadata.name, (.spec.goTemplate // false | tostring), (.spec.generators[0].git.files // .spec.generators[0].git.directories | map(.path) | join(","))] | join(" | ")' | grep -v -- ---; done
scripts/render-chart system/argocd dev | yq 'select(.metadata.name=="apps") | .spec.template.spec.sources[0].helm.parameters[] | .name + "=" + .value'
```
Expected: prod `apps | true | apps/*.yaml` and `root | false | system/*,platform/*,apps/*`; dev `apps` lists the five files and `root` no `apps/` entry; dev parameters `global.domain=127-0-0-1.nip.io`, `global.publicGateway=homelab`; `apps` has the automated `syncPolicy`.

- [ ] **Step 5: Commit:** `git commit -m "feat(argocd): ApplicationSet apps for app-template values files"`.

---

### Task 4: Convert the sandbox apps

**Files:**
- Create: `apps/{actualbudget,excalidraw,homepage,pairdrop,speedtest}.yaml`
- Delete: `apps/{actualbudget,excalidraw,homepage,pairdrop,speedtest}/`

- [ ] **Step 1: Extract.** For each app: `yq '.app-template' apps/<app>/values.yaml > apps/<app>.yaml` (removes the `app-template:` level; comments are kept).

- [ ] **Step 2: Template the environment** (rules in Global Constraints):
  - route hostnames: `budget.`, `draw.`, `home.`, `drop.`, `speedtest.` + `{{ .Values.global.domain }}`
  - excalidraw: `parentRefs[0].name: '{{ .Values.global.publicGateway }}'` (keep the `cloudflare-proxied` annotation)
  - homepage: `HOMEPAGE_ALLOWED_HOSTS: 'home.{{ .Values.global.domain }}'`; in `configMaps.config.data` every `https://<host>.khuedoan.com` link of this homelab (`chat`, `jellyfin`, `jellyseerr`, `paperless`, `transmission`, `prowlarr`, `radarr`, `sonarr`, `auth` including its logo URL, `git`, `ci`, `registry`, …) → `https://<host>.{{ .Values.global.domain }}`; keep `homelab.khuedoan.com` and the GitHub link. `configMaps` data is templated by default (no extra key).
  - Quote every value that starts with `{{` (`'...'`).

- [ ] **Step 3: Remove the wrapper charts:** `git rm -r apps/actualbudget apps/excalidraw apps/homepage apps/pairdrop apps/speedtest`, then remove the git-ignored leftovers with literal paths, e.g. `rm -r apps/actualbudget` (repeat per app).

- [ ] **Step 4: Compare with the baselines.**

```bash
W=.superpowers/sdd/2026-10-14-apps-layout
for a in actualbudget excalidraw homepage pairdrop speedtest; do for e in prod dev; do
  scripts/render-chart apps/$a.yaml $e > $W/$a-$e.new.yaml
  diff <(python3 $W/norm.py $W/baseline/$a-$e.yaml) <(python3 $W/norm.py $W/$a-$e.new.yaml) > $W/$a-$e.diff
  echo "$a $e $(grep -c '^[<>]' $W/$a-$e.diff)"
done; done
```
Expected: `0` everywhere except
- `excalidraw dev 2`: the sandbox `cloudflare-proxied` annotation is now `"true"` instead of `""` (inert without external-dns)
- `homepage dev` (about 16 lines): the links in `services.yaml`/`bookmarks.yaml` now use the sandbox domain; the old sandbox render kept `khuedoan.com` links. Read the diff: only those links may differ.

- [ ] **Step 5: Commit:** `git commit -m "refactor(apps): sandbox apps as app-template values files"`.

---

### Task 5: Convert blog, jellyfin, ollama

**Files:**
- Create: `apps/{blog,jellyfin,ollama}.yaml`; Delete: `apps/{blog,jellyfin,ollama}/`

- [ ] **Step 1: Extract** as in Task 4 Step 1.
- [ ] **Step 2: Template:** blog `www.`, `parentRefs[0].name: '{{ .Values.global.publicGateway }}'`, image repository stays `registry.khuedoan.com/blog`; jellyfin `jellyfin.`, `transmission.`, `prowlarr.`, `radarr.`, `sonarr.`, `jellyseerr.`; ollama `ollama.`, `ai.`.
- [ ] **Step 3: Remove** the three directories (`git rm -r`, then literal `rm -r` per directory).
- [ ] **Step 4: Compare** (Task 4 Step 4 loop over `blog jellyfin ollama`). Expected: prod `0`; dev differs only by the sandbox domain and, for blog, `parentRefs` `homelab` (these apps had no `values-dev.yaml`, so their old dev render equalled prod; they are not in the sandbox): blog `4`, jellyfin `12`, ollama `4`.
- [ ] **Step 5: Commit:** `git commit -m "refactor(apps): blog, jellyfin and ollama as app-template values files"`.

---

### Task 6: Convert paperless, tailscale, wireguard

**Files:**
- Create: `apps/{paperless,tailscale,wireguard}.yaml`; Delete: `apps/{paperless,tailscale,wireguard}/`

- [ ] **Step 1: paperless.** Extract; template `paperless.` in the route and `PAPERLESS_URL: 'https://paperless.{{ .Values.global.domain }}'`; append (replaces `templates/secret.yaml`):

```yaml
externalSecrets:
  secret:
    forceRename: paperless-secret
    secretStoreRef:
      kind: ClusterSecretStore
      name: global-secrets
    dataFrom:
      - extract:
          key: paperless.admin
    target:
      deletionPolicy: Retain
```

- [ ] **Step 2: tailscale (app-template 3.1 → 5.3).** Extract; replace the top-level `serviceAccount: {name: tailscale}` and the four templates with:

```yaml
serviceAccount:
  tailscale: {}
rbac:
  # https://github.com/tailscale/tailscale/blob/main/docs/k8s/role.yaml
  roles:
    tailscale:
      type: Role
      rules:
        - apiGroups: [""]
          resources: ["secrets"]
          verbs: ["create"]
        - apiGroups: [""]
          resourceNames: ["tailscale"]
          resources: ["secrets"]
          verbs: ["get", "update", "patch"]
  bindings:
    tailscale:
      type: RoleBinding
      roleRef:
        identifier: tailscale
      subjects:
        - identifier: tailscale
externalSecrets:
  auth:
    forceRename: tailscale-auth
    secretStoreRef:
      kind: ClusterSecretStore
      name: global-secrets
    data:
      - secretKey: TS_AUTHKEY
        remoteRef:
          key: external
          property: tailscale-auth-key
    target:
      deletionPolicy: Retain
```

and under `controllers.tailscale` add:

```yaml
    # tailscale stores its state in the Secret tailscale (TS_KUBE_SECRET); app-template 5 disables the token by default
    pod:
      automountServiceAccountToken: true
    serviceAccount:
      identifier: tailscale
```

- [ ] **Step 3: wireguard (app-template 3.5 → 5.3).** Extract; replace `rawResources.secret` with `externalSecrets.secret`: `forceRename: wireguard-secret`, the same `secretStoreRef`, `data` and `target` moved up from `rawResources.secret.spec.spec` (dedented), plus `target.deletionPolicy: Retain`. app-template does not template `externalSecrets`, so write the ESO template directly: `PrivateKey = {{ .WIREGUARD_PRIVATE_KEY }}` (drop the backtick escape). Keep `horus.khuedoan.com` and the peer comments literal. The `persistence.config.name: "{{ .Release.Name }}-secret"` stays.

- [ ] **Step 4: Remove** the three directories.

- [ ] **Step 5: Compare** (loop over `paperless tailscale wireguard`) and read every diff line. Allowed differences:
  - all three: `ExternalSecret` gains app-template labels and `target.deletionPolicy: Retain`
  - paperless dev: the sandbox domain
  - tailscale, wireguard (3 → 5): selector and pod labels `app.kubernetes.io/component` → `app.kubernetes.io/controller`, labels and `namespace` on objects; wireguard gets a ServiceAccount `wireguard` with `automountServiceAccountToken: false`
  - must hold: tailscale pod `automountServiceAccountToken: true`, `serviceAccountName: tailscale`; wireguard `wg0.conf` identical to the baseline (`yq 'select(.kind=="ExternalSecret") | .spec.target.template.data."wg0.conf"'` on both)

- [ ] **Step 6: Commit:** `git commit -m "refactor(apps): paperless, tailscale and wireguard as app-template values files"`.

---

### Task 7: Docs, CLAUDE.md and pre-PR checks

**Files:**
- Modify: `docs/installation/sandbox.md`, `docs/getting-started/vpn-setup.md`, `CLAUDE.md`

- [ ] **Step 1: Docs.**
  - `vpn-setup.md`: `./apps/wireguard/values.yaml` → `./apps/wireguard.yaml` (two places).
  - `sandbox.md`: "Make changes" — apps are `apps/<name>.yaml`, rendered with `scripts/render-chart apps/<name>.yaml [dev]`; to add an app to the sandbox, list its file in the `apps` generator of `system/argocd/values-dev.yaml`; hosts use `{{ .Values.global.domain }}`.
  - `CLAUDE.md` Sandbox section: replace "Components synced in the sandbox are listed in `system/argocd/values-dev.yaml`" and the `values-dev.yaml` bullet with: apps are app-template values files `apps/<name>.yaml` (ApplicationSet `apps`, app-template pinned there; environment through `global.domain` and `global.publicGateway`; `apps/matrix/` is the only chart); other components are wrapper charts with optional `values-dev.yaml`; both sandbox allowlists live in `system/argocd/values-dev.yaml`.

- [ ] **Step 2: Pre-PR checks.**

```bash
ls apps
grep -rn 'targetRevision: 5.3.0' system/argocd apps | wc -l   # prod + dev (+ seed) sources only
for f in apps/*.yaml; do for e in prod dev; do scripts/render-chart $f $e > /tmp/r.yaml || echo "FAIL $f $e"; grep -n '{{ .Values' /tmp/r.yaml | sed "s|^|$f $e |"; done; done
for c in system/argocd apps/matrix; do for e in prod dev; do scripts/render-chart $c $e > /dev/null || echo "FAIL $c $e"; done; done
(cd test && go vet ./... && go test -run 'TestSmokeAppsForEnv|TestIdentity(HasGroup|TOTPWaitsForNextStep)|TestGatewayRouteReady' ./...)
mkdocs build --strict && rm -r site
```
Expected: `apps` lists 11 `*.yaml` files and `matrix`; app-template `5.3.0` only in the ApplicationSet sources (`system/argocd/values*.yaml`); no `FAIL`, no unrendered `{{ .Values` in any render; tests `ok`; docs build.

- [ ] **Step 3: Commit:** `git commit -m "docs: apps/<name>.yaml layout"`.

---

### Task 8: Pull request

- [ ] Push `feat/apps-layout`, open a PR into `dev` (summary, the comparison results per app, the expected differences, the sandbox handover note; end with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`). Ask the user before merging.

---

### Task 9: Sandbox after the merge

- [ ] **Step 1:** `git switch dev && git pull --ff-only`; `export KUBECONFIG=$PWD/metal/kubeconfig.yaml`.
- [ ] **Step 2: Handover.** Wait until `kubectl -n argocd get applications` shows the five apps again, generated by `apps` (`kubectl -n argocd get application excalidraw -o jsonpath='{.metadata.ownerReferences[0].name}'` → `apps`). If an Application stays missing after 10 minutes, check `kubectl -n argocd get applicationset apps -o yaml` conditions.
- [ ] **Step 3:** `make dev` → 50/50.
- [ ] **Step 4:** `make dev-clean && make dev` → 50/50.
- [ ] **Step 5:** Mark roadmap step 4 done in `CLAUDE.md` on a new branch, PR into `dev`.
