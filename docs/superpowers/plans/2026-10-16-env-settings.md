# Environment Settings (Step 7b) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Domain, time zone and seed repository come from `infra/<env>/cluster/config.json`; no chart or app file names a domain; `scripts/configure` is gone.

**Architecture:** `config.json` gains `domain`, `timezone`, `seed_repository`; both ApplicationSets pass `global.baseDomain`, `global.timezone`, `global.seedRepository` (next to `global.addresses`). Each component uses them by one of three routes found in the spike: its own templates read the global; a subchart that runs `tpl` gets `{{ .Values.global.baseDomain }}` in its values; a subchart that does not gets the piece from a wrapper-rendered object through its existing-object input. `scripts/check-network-config` becomes `scripts/check-env-config` and guards all of it.

**Tech Stack:** Helm (wrapper charts, `tpl`), Argo CD ApplicationSets, Python check script, Terragrunt bootstrap module, Go tests.

**Spec:** `docs/superpowers/specs/2026-10-16-env-settings-design.md`

## Global Constraints

- Global names: `global.baseDomain`, `global.timezone`, `global.seedRepository` (never `global.domain`: argo-cd owns it).
- Production `config.json`: `domain` `khuedoan.com` (placeholder), `timezone` `Asia/Ho_Chi_Minh`, `seed_repository` `https://github.com/duongle-steven/homelab`. Sandbox: `127-0-0-1.nip.io`, `Asia/Ho_Chi_Minh`, `https://github.com/duongle-steven/homelab`.
- Host names are `<label>.<baseDomain>`; labels unchanged (`argocd`, `grafana`, `git`, `registry`, `auth`, `ci`, `chat`, `matrix`, `home`, …).
- `values-dev.yaml` keeps only sandbox differences; a file left empty is deleted.
- Every changed component renders in production and the sandbox (`scripts/render-chart <dir> [dev]`).

## Review Focus

1. A subchart value that looks templated in one place but is copied raw elsewhere (e.g. argo-cd's `global.domain` also feeding a URL) — the guard's literal-`{{` check must catch it. Pinned in Task 1's check (no rendered `{{ .Values.global`).
2. A wrapper object that replaces a subchart's (argocd-cm, zot config, Forgejo `app.ini` section) must keep every setting the subchart rendered before, not only the domain ones. Pinned in Tasks 4 (render diff before/after shows only the intended differences).
3. Argo CD self-management: replacing `argocd-cm` by a wrapper template must not drop the chart's own keys (`url`, `admin.enabled`, `exec.enabled`, ignoreDifferences customizations). Pinned in Task 4 (diff) and Task 7 (SSO tests).
4. The seed placeholder in `values-seed.yaml` must never reach a cluster unreplaced. Pinned in Task 6 (bootstrap render check).
5. Production-only components (woodpecker, renovate, matrix, cloudflared) are only rendered. Pinned in Task 5 (renders) and the production checklist.

---

### Task 1: Config, globals and the guard

**Files:**
- Modify: `infra/production/cluster/config.json`, `infra/sandbox/cluster/config.json`, `system/argocd/values.yaml`, `system/argocd/values-dev.yaml`, `system/argocd/values-seed.yaml` (the `valuesObject` blocks), `scripts/check-network-config` → `scripts/check-env-config` (`git mv`), `CLAUDE.md`/docs references to the script name

**Interfaces:**
- Produces: `.Values.global.{baseDomain,timezone,seedRepository}` in every component and app; `scripts/check-env-config`.

- [ ] **Step 1: Rename and extend the check (RED)**

`git mv scripts/check-network-config scripts/check-env-config`; update its docstring's first line to "No chart or app file holds an environment setting (address, domain)." and every reference (`grep -rln check-network-config --exclude-dir=site --exclude-dir=docs/superpowers .`; replace in CLAUDE.md, docs, Makefiles).

Add near the top:

```python
# Domains that belong in infra/<env>/cluster/config.json, never in a chart or app file
DOMAINS = {"prod": "khuedoan.com", "dev": "127-0-0-1.nip.io"}
# Globals we pass to every chart (system/argocd ApplicationSets): no subchart may define them in its own global
OUR_GLOBALS = {"addresses", "baseDomain", "timezone", "seedRepository"}
```

and in `main()`, before printing failures:

```python
    # No domain literal in charts and app files (comments excepted)
    grep = subprocess.run(["git", "grep", "-n", "-E", r"khuedoan\.com|127-0-0-1\.nip\.io", "--", "system", "platform", "apps"],
                          capture_output=True, text=True).stdout
    for line in grep.splitlines():
        path, _, text = line.split(":", 2)
        if not text.lstrip().startswith("#"):
            failures.append(f"{path}: names a domain: {text.strip()[:80]}")
    # A render never carries the other environment's domain, nor an unrendered global
    for env, other in (("prod", DOMAINS["dev"]), ("dev", DOMAINS["prod"])):
        for target in targets():
            text = "".join(yaml.safe_dump(d) for d in render(target, env) if d.get("kind") != "CustomResourceDefinition")
            if other in text:
                failures.append(f"{target} ({env}): renders {other}")
            if "Values.global." in text:
                failures.append(f"{target} ({env}): renders an untemplated .Values.global reference")
    # Our globals must not collide with a subchart's own global values (argo-cd has global.domain, Istio global.network)
    for chart in sorted(glob.glob("system/*/charts/*.tgz") + glob.glob("platform/*/charts/*.tgz") + glob.glob("apps/*/charts/*.tgz")):
        out = subprocess.run(["helm", "show", "values", chart], capture_output=True, text=True).stdout
        own = set(((yaml.load(out, Loader=Loader) or {}).get("global") or {}).keys())
        for key in sorted(own & OUR_GLOBALS):
            failures.append(f"{chart}: its global.{key} collides with ours")
```

(The render loop reuses `render()`; merge it into the existing per-target loop if that avoids rendering twice — renders are the slow part.) `targets()` must render after `helm dependency update` so `charts/*.tgz` exist; `render-chart` already runs it.

Run: `scripts/check-env-config > .superpowers/sdd/2026-10-16-env-settings/check-red.log; tail -1 .superpowers/sdd/2026-10-16-env-settings/check-red.log; grep -c 'names a domain' .superpowers/sdd/2026-10-16-env-settings/check-red.log`
Expected: failures, about 50 `names a domain` lines (the inventory in the spec), no collision lines.

- [ ] **Step 2: Config and globals**

Add to both `config.json` files (values per Global Constraints):

```json
  "domain": "khuedoan.com",
  "timezone": "Asia/Ho_Chi_Minh",
  "seed_repository": "https://github.com/duongle-steven/homelab",
```

In every `valuesObject.global` block of `system/argocd/values.yaml` (`&values` and the apps' `*addresses` neighbour), `values-dev.yaml` and `values-seed.yaml`, add beside `addresses`:

```yaml
                    baseDomain: '{{ .domain }}'
                    timezone: '{{ .timezone }}'
                    seedRepository: '{{ .seed_repository }}'
```

(In `values.yaml` the apps source reuses `*values`'s map if it is an anchor; otherwise restate. Keep the apps' `global.domain` parameter for now: Task 3 moves the app files.)

Run: `scripts/env-values prod root | sort; scripts/env-values dev apps | sort`
Expected: the three new keys with the environment's values (plus the five addresses keys).

- [ ] **Step 3: Commit**

```bash
git add -A scripts infra system/argocd CLAUDE.md docs
git commit -m "feat: domain, time zone and seed repository in infra/<env>/cluster/config.json; check-env-config"
```

### Task 2: Components whose own templates use the domain

**Files:**
- Modify: `system/istio-system/{values.yaml,values-dev.yaml,templates/gateway.yaml,templates/certificate.yaml,templates/coredns-custom.yaml}`, `platform/kanidm/{values.yaml,values-dev.yaml,templates/kanidm.yaml}`, `platform/{grafana,forgejo,zot}/templates/kanidm-client.yaml` and their `values.yaml`/`values-dev.yaml` `host:` keys

- [ ] **Step 1:** istio-system: every `.Values.domain` → `.Values.global.baseDomain`; delete `domain:` from `values.yaml` and `values-dev.yaml`; `coredns-custom.yaml`'s rewrite uses `.Values.global.baseDomain` (its comment may keep the example name). 
- [ ] **Step 2:** kanidm: `kanidm.domain: auth.khuedoan.com` → the template builds `auth.{{ .Values.global.baseDomain }}` (replace the value with `host: auth` and use `{{ .Values.host }}.{{ .Values.global.baseDomain }}` wherever `.Values.kanidm.domain` is read; `grep -rn 'kanidm.domain' platform/kanidm`).
- [ ] **Step 3:** grafana, forgejo, zot: `host: <name>.khuedoan.com` → `host: grafana` / `git` / `registry`; their `kanidm-client.yaml` uses `https://{{ .Values.host }}.{{ .Values.global.baseDomain }}`; delete `host:` from their `values-dev.yaml`.
- [ ] **Step 4: Verify**

Run: `for c in system/istio-system platform/kanidm platform/grafana platform/forgejo platform/zot; do for e in prod dev; do scripts/render-chart $c $e >/dev/null 2>&1 || echo "render FAIL $c $e"; done; done; scripts/render-chart system/istio-system dev | grep -c '127-0-0-1.nip.io'; scripts/render-chart platform/grafana | yq 'select(.kind=="KanidmOAuth2Client") | .spec.origin'`
Expected: no `render FAIL`; a non-zero count; `https://grafana.khuedoan.com`.

- [ ] **Step 5: Commit** — `git commit -am "feat: istio-system, kanidm and Kanidm clients build host names from global.baseDomain"`

### Task 3: Subcharts with `tpl`, and the app files

**Files:**
- Modify: `platform/grafana/{values.yaml,values-dev.yaml}`, `system/cloudflared/values.yaml`, `platform/forgejo/values.yaml` (route only), `apps/*.yaml`, `system/argocd/{values.yaml,values-dev.yaml,values-seed.yaml}` (apps parameter)

- [ ] **Step 1:** grafana: `grafana.ini` URLs and route hostnames → `grafana.{{ .Values.global.baseDomain }}`, `https://auth.{{ .Values.global.baseDomain }}/…`; delete those keys from `values-dev.yaml` (keep its sandbox-only settings).
- [ ] **Step 2:** cloudflared: `'*.khuedoan.com'` → `'*.{{ .Values.global.baseDomain }}'`. forgejo: route hostname `git.khuedoan.com` → `git.{{ .Values.global.baseDomain }}` (delete from `values-dev.yaml`).
- [ ] **Step 3:** apps: `sed -i 's/\.Values\.global\.domain/.Values.global.baseDomain/g' apps/*.yaml`; `apps/blog.yaml` image `registry.khuedoan.com/blog` → `registry.{{ .Values.global.baseDomain }}/blog` (if app-template does not template `image.repository`, move the image to a field it does or ledger the limitation and leave blog for 7c's domain); `apps/homepage.yaml` `https://homelab.khuedoan.com` (upstream's docs site) → `https://github.com/duongle-steven/homelab`. Remove the `global.domain` parameter from the apps ApplicationSet in `values.yaml`, `values-dev.yaml`, `values-seed.yaml` (`baseDomain` now comes from `valuesObject`).
- [ ] **Step 4: Verify**

Run: `scripts/render-chart platform/grafana dev | grep -E 'root_url|auth_url'; scripts/render-chart apps/homepage.yaml dev | grep -c 'home.127-0-0-1.nip.io'; scripts/render-chart system/cloudflared | grep hostname`
Expected: sandbox URLs for grafana; non-zero; `'*.khuedoan.com'`.

- [ ] **Step 5: Commit** — `git commit -am "feat: grafana, cloudflared, Forgejo's route and the apps read global.baseDomain"`

### Task 4: Subcharts without `tpl` (sandbox components)

**Files:**
- Modify/Create: `system/argocd/{values.yaml,values-dev.yaml,templates/argocd-cm.yaml,templates/httproute.yaml}`, `platform/forgejo/{values.yaml,values-dev.yaml,templates/app-ini.yaml}`, `platform/zot/{values.yaml,values-dev.yaml,templates/config.yaml}`

For each component, before editing save the current render (`scripts/render-chart <dir> > before.yaml`, also `dev`) in the workspace; after, diff the renders: only the domain-carrying objects may differ, and only by moving into the wrapper object.

- [ ] **Step 1: argocd**
  - `argo-cd.global.domain: 'argocd.{{ .Values.global.baseDomain }}'` (the chart templates it; check the render's `url:`).
  - `argo-cd.configs.cm.create: false`; new `templates/argocd-cm.yaml` renders ConfigMap `argocd-cm` (labels `app.kubernetes.io/name: argocd-cm`, `app.kubernetes.io/part-of: argocd`) with `data: {{ tpl (toYaml .Values.cm) . | nindent 2 }}`; move the content of `argo-cd.configs.cm` into a top-level `cm:` map, adding the keys the chart set itself (copy them from `before.yaml`'s `argocd-cm`: `url`, `admin.enabled`, `application.instanceLabelKey`, `exec.enabled`, … whatever it rendered); `oidc.config`'s issuer → `https://auth.{{ .Values.global.baseDomain }}/oauth2/openid/argocd`; delete the sandbox's `oidc.config` override from `values-dev.yaml`.
  - `argo-cd.server.httproute.enabled: false`; new `templates/httproute.yaml` with the same spec the chart rendered (from `before.yaml`), hostname `argocd.{{ .Values.global.baseDomain }}`.
  - Diff: `argocd-cm` keys identical except the issuer's host; the HTTPRoute identical except owner labels.
- [ ] **Step 2: forgejo**
  - `[server]` (`DOMAIN`, `ROOT_URL`, `SSH_DOMAIN`): remove from `gitea.config.server` and render a ConfigMap `forgejo-app-ini-server` in `templates/app-ini.yaml` with key `server` and value `DOMAIN=git.<baseDomain>\nROOT_URL=https://git.<baseDomain>\nSSH_DOMAIN=git.<baseDomain>` (keep the other server keys in `gitea.config.server`); add `gitea.additionalConfigSources: [{configMap: {name: forgejo-app-ini-server}}]`.
  - OAuth `autoDiscoverUrl`: the chart writes it into its `configure-gitea` script inside double quotes. Probe: set `autoDiscoverUrl: https://auth.${HOMELAB_DOMAIN}/oauth2/openid/forgejo/.well-known/openid-configuration` and `deployment.env: [{name: HOMELAB_DOMAIN, valueFrom: {configMapKeyRef: {name: forgejo-app-ini-server, key: domain}}}]` (add `domain: <baseDomain>` to that ConfigMap), then check the render: the init container `configure-gitea` gets the env (if `deployment.env` reaches only the main container, look for the chart's init-container env input in `helm show values platform/forgejo/charts/forgejo-*.tgz`; if none, ledger and use the chart's `gitea.oauth` with an explicit sandbox override kept in `values-dev.yaml` as the only remaining domain there).
  - Delete the moved keys from `values-dev.yaml`.
- [ ] **Step 3: zot**
  - zot reads `config.json` from the chart's `config:` (a Secret `zot-config`). Render that Secret in `templates/config.yaml` instead (`stringData.config.json: {{ tpl (toJson .Values.zotConfig) . }}` where `zotConfig` is today's `zot.config` content with `externalUrl` and the OIDC issuer templated), set `zot.config: {}` and point the chart at the existing Secret (check `helm show values` for the existing-secret key, e.g. `existingSecret`/`secretName`; if the chart has none, mount the Secret with `extraVolumes`/`extraVolumeMounts` at the path the chart used and ledger it).
  - Delete the moved keys from `values-dev.yaml`.
- [ ] **Step 4: Verify**

Run: `scripts/check-env-config | grep -E 'argocd|forgejo|zot' | head; for c in system/argocd platform/forgejo platform/zot; do for e in prod dev; do scripts/render-chart $c $e > /dev/null || echo "FAIL $c $e"; done; done`
Expected: no failure lines for these three; renders succeed.

- [ ] **Step 5: Commit** — `git commit -am "feat: Argo CD, Forgejo and zot get their domain settings from wrapper templates"`

### Task 5: Production-only components and the time zone

**Files:**
- Modify: `platform/woodpecker`, `platform/renovate`, `apps/matrix`, `system/kured`

- [ ] **Step 1: woodpecker** — `WOODPECKER_HOST`, `WOODPECKER_FORGEJO_URL`: move to a wrapper ConfigMap read through the chart's env-from input (`helm show values platform/woodpecker/charts/woodpecker-*.tgz | grep -n -i 'envfrom\|extraSecretNamesForEnvFrom'`); its HTTPRoute template (already ours) uses `ci.{{ .Values.global.baseDomain }}`.
- [ ] **Step 2: renovate** — the `endpoint` in its config: render the config in the wrapper (`templates/secret.yaml` already exists) or use the chart's existing-config input; `https://git.{{ .Values.global.baseDomain }}/api/v1`.
- [ ] **Step 3: matrix** — its own `templates/httproute.yaml` uses the global for `chat.`/`matrix.`; dendrite's `server_name` and `base_url`: if dendrite's chart does not template them, render its config Secret in the wrapper (check `helm show values apps/matrix/charts/dendrite-*.tgz | grep -n -i existing`) or ledger it as the remaining production-only literal with a checklist entry.
- [ ] **Step 4: kured** — `configuration.timeZone: '{{ .Values.global.timezone }}'` if the chart templates it (render shows the zone); otherwise ledger and keep the value, noting it in the checklist.
- [ ] **Step 5: Verify and commit**

Run: `scripts/check-env-config | grep -E 'woodpecker|renovate|matrix|kured'; for c in platform/woodpecker platform/renovate apps/matrix system/kured; do scripts/render-chart $c >/dev/null || echo "FAIL $c"; done`
Expected: no lines (or only ledgered, explained literals); renders succeed.

`git commit -am "feat: woodpecker, renovate, matrix and kured read the environment's settings"`

### Task 6: Seed repository, configure script, docs

**Files:**
- Modify: `system/argocd/values-seed.yaml`, `infra/_modules/argocd/{bootstrap.sh,main.tf}`, `infra/production/argocd/terragrunt.hcl`, `platform/forgejo` (seed Job reads `global.seedRepository`), `CLAUDE.md`, `docs/installation/production/configuration.md`
- Delete: `scripts/configure`, empty `values-dev.yaml` files, the root `Makefile`'s `configure` target

- [ ] **Step 1:** `values-seed.yaml`: `repoURL: &repoURL https://github.com/khuedoan/homelab` → `repoURL: &repoURL https://seed-repository.invalid` with a comment "replaced by infra/_modules/argocd with config.json's seed_repository". `bootstrap.sh`: when `$values` is the seed file, pipe the rendered manifests through `sed "s|https://seed-repository.invalid|$SEED_REPOSITORY|g"` and fail if the placeholder remains (`grep -q seed-repository.invalid` → exit 1). The module takes `seed_repository` (variable, default ""), the production unit reads it from `config.json` (`jsondecode(file("${get_terragrunt_dir()}/../cluster/config.json")).seed_repository`) and passes it as `SEED_REPOSITORY`.
- [ ] **Step 2:** forgejo's seed Job: `seed.repository` → `{{ .Values.global.seedRepository }}` (delete `seed.repository` from `values.yaml` and `values-dev.yaml`).
- [ ] **Step 3:** `git rm scripts/configure`; remove the `configure` target from the root `Makefile`; `docs/installation/production/configuration.md`: replace `make configure` with "edit `infra/production/cluster/config.json` (domain, time zone, seed repository, addresses)"; the Terraform Cloud workspace in `external/versions.tf` gets a sentence "change `organization` to yours (until step 7c)".
- [ ] **Step 4:** delete every `values-dev.yaml` that is now empty (`for f in system/*/values-dev.yaml platform/*/values-dev.yaml; do [ -z "$(yq 'del(.. | select(tag == "!!null"))' $f | grep -v '^#' | tr -d '[:space:]{}')" ] && git rm $f; done`, then re-render those components in dev).
- [ ] **Step 5:** `CLAUDE.md`: roadmap 7b done; rules: "Domain, time zone and seed repository live only in `infra/<env>/cluster/config.json` (`global.baseDomain`, `global.timezone`, `global.seedRepository`); `scripts/check-env-config` checks it"; replace `scripts/check-network-config` everywhere; the production checklist item "Run `make configure` …" becomes "Set `infra/production/cluster/config.json` (your domain, time zone, seed repository) and `external/versions.tf`'s Terraform Cloud organization (until 7c)"; list any ledgered literal left in production-only components.
- [ ] **Step 6: Verify and commit**

Run: `scripts/check-env-config | tail -1; mkdocs build --strict 2>&1 | tail -1; KUBECONFIG=/dev/null sh -c 'cd infra/production/argocd && terragrunt validate-inputs --non-interactive' 2>&1 | tail -1`
Expected: `0 failures`; `Documentation built`; inputs valid.

`git commit -am "feat: seed repository from config.json at bootstrap; drop scripts/configure"`

### Task 7: Prove it

- [ ] **Step 1:** basic checks: `make -C infra check && scripts/check-env-config | tail -1 && scripts/check-mesh-policies >/dev/null && echo mesh-ok && (cd test && go vet ./... && go test -run 'TestSmokeAppsForEnv|TestIdentity(TOTPWaitsForNextStep|PKCE)|TestGatewayRouteReady|TestGatewayNodeAddressFromInspect' ./...) | tail -1`
Expected: all pass.
- [ ] **Step 2:** `make dev-clean; make dev > .superpowers/sdd/2026-10-16-env-settings/make-dev.log 2>&1; echo exit=$?; grep -E '^(--- FAIL|FAIL)|^DONE' .superpowers/sdd/2026-10-16-env-settings/make-dev.log`
Expected: `exit=0`, `DONE 98 tests`.
- [ ] **Step 3:** `git grep -n -E 'khuedoan\.com|127-0-0-1\.nip\.io' -- system platform apps | grep -v ':\s*#'`
Expected: no output (or only ledgered production-only literals).
