# SSO directly with Kanidm (step 5a) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove Dex: Grafana, Argo CD and zot log in with their own Kanidm OAuth2 clients (roles unchanged), and Actual Budget and paperless get Kanidm SSO limited to `homelab-budget` and `homelab-documents`.

**Architecture:** Each app declares a kaniop `KanidmOAuth2Client` in its own namespace (wrapper chart template, or `rawResources` in an app file). The admin tools keep the `homelab_groups` claim map; the personal-data apps are gated by a `scopeMap` on their access group. kaniop's generated Secret `<client>-kanidm-oauth2-credentials` replaces the `dex.*` global secrets. Tests talk to Kanidm directly with the sandbox client `smoke` and PKCE.

**Tech Stack:** Kanidm 1.11.2, kaniop 0.19.0, Grafana 13.5.1, Argo CD (argo-cd 10.10.3), zot 0.1.128, Actual Budget 26.10.0, paperless-ngx 3.3.1, Go tests (chromedp v0.16.0, terratest).

**Spec:** `docs/superpowers/specs/2026-10-14-kanidm-sso-design.md`

## Global Constraints

- Branch `feat/kanidm-sso` (spec committed). PR into `dev`; merge only with the user's go-ahead. Commits end with:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01GZDRNK1oUcGXXSC3R6dhN2
  ```
- Dev shell; no `rm` with variables or in `sh -c`.
- Kanidm endpoints (domain `D` = `khuedoan.com` prod, `127-0-0-1.nip.io` sandbox): authorize `https://auth.D/ui/oauth2`, token `https://auth.D/oauth2/token`, issuer and discovery `https://auth.D/oauth2/openid/<client>` (`/.well-known/openid-configuration`), userinfo `https://auth.D/oauth2/openid/<client>/userinfo`. Kanidm puts claim-map claims in both the ID token and userinfo.
- Every client: `kanidmRef: {name: kanidm, namespace: kanidm}`, `disableConsentPrompt: true`, `preferShortUsername: true`, `strictRedirectUrl: true`. PKCE is mandatory for every client.
- Role groups `homelab-admins`, `homelab-editors`, `homelab-users`; access groups `homelab-budget`, `homelab-documents`. Claim `homelab_groups` (array of short names), as in today's Dex client.
- Secret of a confidential client: `<metadata.name>-kanidm-oauth2-credentials`, keys `CLIENT_ID`, `CLIENT_SECRET`.
- Client namespaces allowed by the `Kanidm` CR: `argocd`, `grafana`, `zot`, `actualbudget`, `paperless`, and `kanidm` (sandbox client `smoke`).
- Sandbox members: `homelab-budget` = `dev-admin`, `dev-viewer`; `homelab-documents` = `dev-admin`, `dev-editor`. Production groups set no `members`.
- App files (`apps/*.yaml`) get the sandbox CA through `global.devCA` (`"true"` in `system/argocd/values-dev.yaml`, `""` in prod and seed) and an optional `custom` volume of ConfigMap `homelab-dev-ca` (app-template rejects `optional` on `configMap` volumes).
- Offline test pattern: `TestSmokeAppsForEnv|TestIdentity(HasGroup|TOTPWaitsForNextStep|PKCE)|TestGatewayRouteReady`.

## Review Focus

1. **kaniop ignores clients outside its namespace selector** (no error on the client, just never reconciled, no Secret). → Task 2 renders the selector; the SSO tests (Task 11) fail fast through `waitForSecret`.
2. **Bootstrap renders `system/argocd` offline before kaniop's CRDs exist.** → Task 4 guards the Argo CD client with `.Capabilities.APIVersions.Has`, and checks the offline render has no `KanidmOAuth2Client`.
3. **Actual Budget makes the first OpenID user the server owner.** → sandbox test order logs in `dev-admin` first; CLAUDE.md production note.
4. **Group claim name drift:** apps that still read `groups` would see SPNs/UUIDs and deny everyone. → each app task greps its render for `groups` vs `homelab_groups`; SSO tests check every role.
5. **Kanidm refuses non-PKCE token requests:** the smoke client and every app must send PKCE. → `TestIdentityPKCE` (offline) checks the S256 challenge; app settings enable PKCE explicitly where it is optional (Grafana, Argo CD, paperless).

---

## File Structure

| File | Responsibility |
|---|---|
| `platform/kanidm/templates/{groups.yaml,kanidm.yaml}`, `templates/smoke-client.yaml` (new), `values.yaml`, `values-dev.yaml` | groups, client namespace selector, sandbox `smoke` client |
| `platform/grafana/{values.yaml,values-dev.yaml,templates/kanidm-client.yaml,templates/secret.yaml}` | Grafana client and OAuth settings |
| `system/argocd/{values.yaml,values-dev.yaml,templates/kanidm-client.yaml,templates/oidc-secret.yaml}` | Argo CD client, OIDC and RBAC; `apps` parameters `global.devCA`; allowlists |
| `platform/zot/{values.yaml,values-dev.yaml,templates/kanidm-client.yaml,templates/auth-secret.yaml}` | zot public client |
| `apps/actualbudget.yaml`, `apps/paperless.yaml` | clients (`rawResources`), OIDC settings, sandbox CA |
| `platform/dex/` | deleted |
| `platform/global-secrets/values.yaml` | drop `dex.*` |
| `test/identity_helpers_test.go`, `test/identity_test.go`, `test/sso_test.go`, `test/smoke_test.go`, `test/apps_test.go` | tests |
| `docs/**`, `CLAUDE.md` | docs |

---

### Task 1: Tests talk to Kanidm (TDD)

**Files:** `test/identity_helpers_test.go`, `test/identity_test.go`, `test/sso_test.go`, `test/smoke_test.go`, `test/apps_test.go`

**Interfaces:**
- Produces: `func pkcePair() (verifier, challenge string)`; `func waitForSecret(t, namespace, name string) map[string][]byte`; `browserAuthorize` and `exchangeCode` against Kanidm with PKCE; `func smokeClientSecret(t) string` reading `kanidm/smoke-kanidm-oauth2-credentials`; tests `TestIdentityPKCE` (offline), `TestSSOActualBudget`, `TestSSOPaperless`.

- [ ] **Step 1: Failing offline test.** Append to `test/identity_test.go`:

```go
func TestIdentityPKCE(t *testing.T) {
	verifier, challenge := pkcePair()
	assert.Regexp(t, `^[A-Za-z0-9_-]{43,128}$`, verifier)
	sum := sha256.Sum256([]byte(verifier))
	assert.Equal(t, base64.RawURLEncoding.EncodeToString(sum[:]), challenge)
	other, _ := pkcePair()
	assert.NotEqual(t, verifier, other)
}
```
(add imports `crypto/sha256`, `encoding/base64`). Run `cd test && go test -run TestIdentityPKCE ./...` → FAIL: `undefined: pkcePair`.

- [ ] **Step 2: Implement** in `test/identity_helpers_test.go`:

```go
// pkcePair returns a random PKCE verifier and its S256 challenge; Kanidm requires PKCE for every client.
func pkcePair() (string, string) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}
```
(`crypto/rand` is already imported; add `crypto/sha256`). Rerun → PASS.

- [ ] **Step 3: Kanidm instead of Dex.** In `test/identity_helpers_test.go`:
  - delete `dexURL`; keep `kanidmURL`, `smokeRedirect`.
  - `browserAuthorize`: build `verifier, challenge := pkcePair()`; authorize URL `kanidmURL + "/ui/oauth2?" + url.Values{client_id: smoke, redirect_uri: smokeRedirect, response_type: code, scope: "openid email profile groups", state: identity-test, code_challenge: challenge, code_challenge_method: S256}`; return the verifier as a third value (`(code, page, verifier string)`); update the doc comment ("logs in through Kanidm").
  - `exchangeCode(t, code, secret, verifier string)`: POST to `kanidmURL + "/oauth2/token"` with form `grant_type=authorization_code`, `code`, `redirect_uri`, `code_verifier`; basic auth `smoke`/secret.
  - `waitForIdentityStack`: wait for Secret `kanidm/smoke-kanidm-oauth2-credentials` and `kanidmURL + "/oauth2/openid/smoke/.well-known/openid-configuration"` (200); message "Kanidm is not ready"; update its comment.
  - `smokeClientSecret`: `string(waitForSecret(t, "kanidm", "smoke-kanidm-oauth2-credentials")["CLIENT_SECRET"])`.
  - Add:

```go
// waitForSecret waits for a Secret that an operator creates (kaniop client credentials) and returns its data.
func waitForSecret(t *testing.T, namespace, name string) map[string][]byte {
	options := k8s.NewKubectlOptions("", "", namespace)
	options.Logger = logger.Discard
	var data map[string][]byte
	require.Eventually(t, func() bool {
		secret, err := k8s.GetSecretE(t, options, name)
		if err != nil {
			return false
		}
		data = secret.Data
		return true
	}, 20*time.Minute, 15*time.Second, "Secret %s/%s does not exist", namespace, name)
	return data
}
```
  - `appLogin`'s comment: "logs in through Kanidm".
- [ ] **Step 4: Identity assertions.** In `TestIdentityLogin`: `code, page, verifier := browserAuthorize(...)`, `claims := exchangeCode(t, code, secret, verifier)`, log and assert `claims["homelab_groups"]` equals `[]any{account.group}`. Other callers of `browserAuthorize` (`TestIdentityDenied`, `TestIdentityDeniedNeedsLogin`) ignore the verifier.
- [ ] **Step 5: New SSO tests.** In `test/sso_test.go` add hosts `budgetHost = "budget.127-0-0-1.nip.io"`, `paperlessHost = "paperless.127-0-0-1.nip.io"` and:

```go
func TestSSOActualBudget(t *testing.T) {
	skipUnlessSandbox(t)
	base := "https://" + budgetHost
	waitForApp(t, base+"/account/login-methods")

	// dev-admin first: the first OpenID user becomes the server owner
	for _, c := range []struct {
		account string
		allowed bool
	}{{"dev-admin", true}, {"dev-viewer", true}, {"dev-editor", false}} {
		t.Run(c.account, func(t *testing.T) {
			var start struct {
				Data struct {
					ReturnURL string `json:"returnUrl"`
				} `json:"data"`
			}
			require.Equal(t, http.StatusOK, getJSON(t, "POST", base+"/account/login", nil, "", "",
				map[string]any{"loginMethod": "openid", "returnUrl": base}, &start))
			token, page := actualOpenIDLogin(t, c.account, ensureCredentials(t, c.account), start.Data.ReturnURL, base)
			if !c.allowed {
				assert.Empty(t, token)
				assert.Contains(t, page, "Access Denied")
				return
			}
			require.NotEmpty(t, token, "no Actual token for %s; page:\n%s", c.account, page)
			var me struct {
				Data struct {
					UserName string `json:"userName"`
				} `json:"data"`
			}
			assert.Equal(t, http.StatusOK, getJSON(t, "GET", base+"/account/validate", nil, "", "", nil, &me, "X-ACTUAL-TOKEN", token))
			assert.Equal(t, c.account, me.Data.UserName)
		})
	}
}

func TestSSOPaperless(t *testing.T) {
	skipUnlessSandbox(t)
	base := "https://" + paperlessHost
	waitForApp(t, base+"/accounts/login/")
	for _, c := range []struct {
		account          string
		allowed, superus bool
	}{{"dev-admin", true, true}, {"dev-editor", true, false}, {"dev-viewer", false, false}} {
		t.Run(c.account, func(t *testing.T) {
			start := base + "/accounts/oidc/kanidm/login/?process=login"
			if !c.allowed {
				_, page := browserDenied(t, c.account, ensureCredentials(t, c.account), start)
				assert.Contains(t, page, "Access Denied")
				return
			}
			cookies := appLogin(t, c.account, ensureCredentials(t, c.account), start, paperlessHost)
			var me struct {
				Username    string `json:"username"`
				IsSuperuser bool   `json:"is_superuser"`
			}
			require.Equal(t, http.StatusOK, getJSON(t, "GET", base+"/api/profile/", cookies, "", "", nil, &me))
			assert.Equal(t, c.account, me.Username)
			assert.Equal(t, c.superus, me.IsSuperuser)
		})
	}
}
```

  with helpers in `test/identity_helpers_test.go`:
  - `actualOpenIDLogin(t, account, c, idpURL, appBase string) (token, page string)`: new browser, navigate to `idpURL`, wait `input#username`, `kanidmLogin`, then wait (30 s, like `browserAuthorize`) for a request to `appBase + "/openid-cb"` (listen to `network.EventRequestWillBeSent`) and return its `token` query parameter, or the page text when Kanidm shows "Access Denied".
  - `browserDenied(t, account, c, startURL string) (string, string)`: navigate to `startURL`, wait `input#username`, `kanidmLogin`, poll the page text for "Access Denied" (30 s) and return it.
  - If paperless shows allauth's confirmation page before redirecting (a form with a submit button on `/accounts/oidc/kanidm/login/`), `appLogin` and `browserDenied` click `button[type=submit]` first when `input#username` is not visible within 5 s. Implement this as an optional step in a shared `openLogin(ctx, startURL)` helper used by both.
  - Superuser check: if `/api/profile/` does not return `is_superuser`, read it from `/api/ui_settings/` (`user.is_superuser`) instead; the assertion stays the same.
- [ ] **Step 6: Smoke lists.** `smokeApps("dev")`: drop `{"dex", "dex"}`, add `{"paperless", "paperless"}`; same in `TestSmokeAppsForEnv`.
- [ ] **Step 7: Verify offline:** `cd test && gofmt -l . && go vet ./... && go test -run 'TestSmokeAppsForEnv|TestIdentity(HasGroup|TOTPWaitsForNextStep|PKCE)|TestGatewayRouteReady' ./...` → ok.
- [ ] **Step 8: Commit:** `test: identity and SSO tests talk to Kanidm directly; Actual Budget and paperless SSO tests`.

---

### Task 2: Kanidm groups, client namespaces, sandbox `smoke` client

**Files:** `platform/kanidm/templates/groups.yaml`, `templates/kanidm.yaml`, `templates/smoke-client.yaml` (new), `values.yaml`, `values-dev.yaml`

- [ ] **Step 1: Groups.** In `groups.yaml`, range over `homelab-admins homelab-editors homelab-users homelab-budget homelab-documents`. Add to `values-dev.yaml` `groupMembers`: `homelab-budget: [dev-admin, dev-viewer]`, `homelab-documents: [dev-admin, dev-editor]`.
- [ ] **Step 2: Client namespaces.** In `kanidm.yaml`, replace the `oauth2ClientNamespaceSelector` (today only `dex`) with:

```yaml
  # Each app declares its own OAuth2 client in its namespace
  oauth2ClientNamespaceSelector:
    matchExpressions:
      - key: kubernetes.io/metadata.name
        operator: In
        values: {{ toYaml .Values.oauth2ClientNamespaces | nindent 10 }}
```
and in `values.yaml`:
```yaml
# Namespaces whose KanidmOAuth2Clients kaniop reconciles
oauth2ClientNamespaces: [argocd, grafana, zot, actualbudget, paperless]
```
`values-dev.yaml` restates the list with `kanidm` added (sandbox client `smoke`).
- [ ] **Step 3: `smoke` client.** Create `templates/smoke-client.yaml`, rendered only when `.Values.smokeClient` (true in `values-dev.yaml` only):

```yaml
{{- if .Values.smokeClient }}
# Sandbox-only client used by the identity tests (test/identity_test.go)
apiVersion: kaniop.rs/v1beta1
kind: KanidmOAuth2Client
metadata:
  name: smoke
  namespace: {{ .Release.Namespace }}
spec:
  kanidmRef:
    name: kanidm
  displayname: Smoke test
  origin: https://smoke.{{ .Values.kanidm.domain | trimPrefix "auth." }}
  redirectUrl:
    - https://smoke.{{ .Values.kanidm.domain | trimPrefix "auth." }}/callback
  disableConsentPrompt: true
  preferShortUsername: true
  strictRedirectUrl: true
  claimMap:
    - name: homelab_groups
      joinStrategy: array
      valuesMap:
        {{- range list "homelab-admins" "homelab-editors" "homelab-users" }}
        - group: {{ . }}
          values:
            - {{ . }}
        {{- end }}
  scopeMap:
    {{- range list "homelab-admins" "homelab-editors" "homelab-users" }}
    - group: {{ . }}
      scopes: [openid, profile, email, groups]
    {{- end }}
{{- end }}
```
- [ ] **Step 4: Verify:** `scripts/render-chart platform/kanidm dev | yq 'select(.kind=="KanidmGroup" or .kind=="KanidmOAuth2Client") | .kind + "/" + .metadata.name'` lists 5 groups and `smoke`; prod lists 5 groups, no client; `yq 'select(.kind=="Kanidm") | .spec.oauth2ClientNamespaceSelector'` shows the lists.
- [ ] **Step 5: Commit:** `feat(kanidm): access groups, per-app client namespaces, sandbox smoke client`.

---

### Task 3: Grafana

**Files:** `platform/grafana/values.yaml`, `values-dev.yaml`, `templates/kanidm-client.yaml` (new), `templates/secret.yaml`

- [ ] **Step 1: Client.** `templates/kanidm-client.yaml`: `KanidmOAuth2Client` `grafana` (common settings), `origin: https://{{ .Values.host }}`, `redirectUrl: [https://{{ .Values.host }}/login/generic_oauth]`, `claimMap` `homelab_groups` and `scopeMap` for the three role groups as in Task 2 Step 3. Add `host: grafana.khuedoan.com` to `values.yaml` and `host: grafana.127-0-0-1.nip.io` to `values-dev.yaml` (top level, next to `grafana:`).
- [ ] **Step 2: OAuth settings** (`grafana.grafana.ini.auth.generic_oauth` in both files; dev restates the URLs):

```yaml
      name: Kanidm
      client_id: grafana
      client_secret: $__env{CLIENT_SECRET}
      scopes: openid profile email groups
      auth_url: https://auth.khuedoan.com/ui/oauth2
      token_url: https://auth.khuedoan.com/oauth2/token
      api_url: https://auth.khuedoan.com/oauth2/openid/grafana/userinfo
      use_pkce: true
      role_attribute_path: "contains(homelab_groups[*], 'homelab-admins') && 'GrafanaAdmin' || contains(homelab_groups[*], 'homelab-editors') && 'Editor' || contains(homelab_groups[*], 'homelab-users') && 'Viewer'"
```
  (keep `enabled`, `allow_sign_up`, `role_attribute_strict`, `allow_assign_grafana_admin`, `login_attribute_path`). `envFromSecrets`: replace `grafana-secrets` with `grafana-kanidm-oauth2-credentials`; delete `templates/secret.yaml` if it only held the Dex secret (the admin password lives in `grafana-admin`).
- [ ] **Step 3: Verify:** both renders contain the client; `grep -c 'groups\[\*\]'` on the rendered `grafana.ini` is 0 and `homelab_groups[*]` appears; no `dex` string in either render.
- [ ] **Step 4: Commit:** `feat(grafana): log in with Kanidm directly`.

---

### Task 4: Argo CD

**Files:** `system/argocd/values.yaml`, `values-dev.yaml`, `templates/kanidm-client.yaml` (new), `templates/oidc-secret.yaml` (delete)

- [ ] **Step 1: Client** guarded for the offline bootstrap render:

```yaml
{{- /* Skipped by the offline bootstrap render, when kaniop does not exist yet */}}
{{- if .Capabilities.APIVersions.Has "kaniop.rs/v1beta1/KanidmOAuth2Client" }}
apiVersion: kaniop.rs/v1beta1
kind: KanidmOAuth2Client
metadata:
  name: argocd
  namespace: {{ .Release.Namespace }}
spec:
  # ...common settings, claimMap homelab_groups, scopeMap for the three role groups
  origin: https://{{ index .Values "argo-cd" "global" "domain" }}
  redirectUrl:
    - https://{{ index .Values "argo-cd" "global" "domain" }}/auth/callback
  secretTemplate:
    labels:
      # Argo CD only resolves $secret:key references in Secrets with this label
      app.kubernetes.io/part-of: argocd
{{- end }}
```
  (kaniop 0.19's `secretTemplate` has `labels` and `annotations` at its top level.)
- [ ] **Step 2: OIDC and RBAC.** `oidc.config` (prod and dev; dev uses the sandbox domain):

```yaml
        name: Kanidm
        issuer: https://auth.khuedoan.com/oauth2/openid/argocd
        clientID: argocd
        clientSecret: $argocd-kanidm-oauth2-credentials:CLIENT_SECRET
        enablePKCEAuthentication: true
        requestedScopes: [openid, profile, email, groups]
```
  `rbac.scopes: '[homelab_groups]'`. Delete `templates/oidc-secret.yaml`. Remove the dev `SSL_CERT_DIR` comment's mention of Dex ("for calls to Kanidm").
- [ ] **Step 3: Verify:** `scripts/render-chart system/argocd dev` has the client; `helm template argocd system/argocd -f system/argocd/values.yaml -f system/argocd/values-dev.yaml | grep -c KanidmOAuth2Client` (no `--api-versions`, as the bootstrap renders) is 0; `grep -c dex` on both renders counts only `dex.enabled: false` of the chart.
- [ ] **Step 4: Commit:** `feat(argocd): log in with Kanidm directly`.

---

### Task 5: zot (public client)

**Files:** `platform/zot/values.yaml`, `values-dev.yaml`, `templates/kanidm-client.yaml` (new), `templates/auth-secret.yaml`

- [ ] **Step 1: Client** `zot`: common settings, `public: true`, `origin: https://{{ .Values.host }}`, `redirectUrl: [https://{{ .Values.host }}/zot/auth/callback/oidc]`, `claimMap` `homelab_groups`, `scopeMap` for the role groups; `host` values as in Task 3.
- [ ] **Step 2: Provider** (prod and dev):

```yaml
            oidc:
              name: Kanidm
              issuer: https://auth.khuedoan.com/oauth2/openid/zot
              # Public client: no secret, zot uses PKCE
              clientid: zot
              scopes: [openid, profile, email, groups]
              claimMapping:
                username: preferred_username
                groups: homelab_groups
```
  Comment above `apikey`: "People log in to the UI through Kanidm and create API keys for docker/crane". In `templates/auth-secret.yaml` drop the `client_secret` data and the `oidc-credentials.json` key (keep `htpasswd`).
- [ ] **Step 3: Verify:** render has no `credentialsFile`, no `dex`, `groups: homelab_groups`; the client has `public: true`.
- [ ] **Step 4: Commit:** `feat(zot): public Kanidm client with PKCE`.

---

### Task 6: Sandbox CA parameter for app files

**Files:** `system/argocd/values.yaml`, `values-dev.yaml`, `values-seed.yaml`

- [ ] Add parameter `global.devCA` to the `apps` ApplicationSet source: `""` in prod and seed, `"true"` in dev (with a comment: app files trust the sandbox CA ConfigMap when set). Verify the three renders list it. Commit `feat(argocd): global.devCA parameter for app files`.

---

### Task 7: Actual Budget SSO

**Files:** `apps/actualbudget.yaml`

- [ ] **Step 1:** add

```yaml
rawResources:
  kanidm-client:
    forceRename: actualbudget
    manifest:
      apiVersion: kaniop.rs/v1beta1
      kind: KanidmOAuth2Client
      spec:
        kanidmRef:
          name: kanidm
          namespace: kanidm
        displayname: Actual Budget
        origin: 'https://budget.{{ .Values.global.domain }}'
        redirectUrl:
          - 'https://budget.{{ .Values.global.domain }}/openid/callback'
        disableConsentPrompt: true
        preferShortUsername: true
        strictRedirectUrl: true
        # Actual Budget does not verify ES256 id tokens
        jwtLegacyCryptoEnable: true
        scopeMap:
          - group: homelab-budget
            scopes: [openid, profile, email, groups]
```
  (`forceRename` sets the object name; checked with a render.)
- [ ] **Step 2:** container env:

```yaml
          ACTUAL_OPENID_DISCOVERY_URL: 'https://auth.{{ .Values.global.domain }}/oauth2/openid/actualbudget/.well-known/openid-configuration'
          ACTUAL_OPENID_SERVER_HOSTNAME: 'https://budget.{{ .Values.global.domain }}'
          ACTUAL_OPENID_CLIENT_ID: actualbudget
          ACTUAL_OPENID_CLIENT_SECRET:
            valueFrom:
              secretKeyRef:
                name: actualbudget-kanidm-oauth2-credentials
                key: CLIENT_SECRET
          # Only OpenID; members of homelab-budget get an account on first login
          ACTUAL_OPENID_ENFORCE: "true"
          ACTUAL_USER_CREATION_MODE: login
          NODE_EXTRA_CA_CERTS: '{{ if .Values.global.devCA }}/etc/homelab-dev-ca/ca.crt{{ end }}'
```
  and persistence:

```yaml
  dev-ca:
    # The sandbox CA from trust-manager; absent in production, so the volume is optional
    type: custom
    volumeSpec:
      configMap:
        name: homelab-dev-ca
        optional: true
    globalMounts:
      - path: /etc/homelab-dev-ca
        readOnly: true
```
- [ ] **Step 3: Verify:** prod and dev renders: client with `homelab-budget`, env with the right domain, `NODE_EXTRA_CA_CERTS` empty in prod and set in dev, no literal `{{ .Values`.
- [ ] **Step 4: Commit:** `feat(actualbudget): Kanidm SSO for homelab-budget`.

---

### Task 8: paperless SSO and sandbox

**Files:** `apps/paperless.yaml`, `system/argocd/values-dev.yaml`

- [ ] **Step 1:** client in `rawResources` like Task 7 (name `paperless`, redirect `https://paperless.{{ .Values.global.domain }}/accounts/oidc/kanidm/login/callback/`, `scopeMap` for `homelab-documents`, `claimMap` `homelab_groups` with values for `homelab-admins` and `homelab-documents`, no legacy crypto).
- [ ] **Step 2:** env (the provider id `kanidm` sets the callback path):

```yaml
          PAPERLESS_APPS: allauth.socialaccount.providers.openid_connect
          CLIENT_SECRET:
            valueFrom:
              secretKeyRef:
                name: paperless-kanidm-oauth2-credentials
                key: CLIENT_SECRET
          PAPERLESS_SOCIALACCOUNT_PROVIDERS: >-
            {"openid_connect": {"OAUTH_PKCE_ENABLED": true, "SCOPE": ["openid", "profile", "email", "groups"],
            "APPS": [{"provider_id": "kanidm", "name": "Kanidm", "client_id": "paperless",
            "secret": "$(CLIENT_SECRET)",
            "settings": {"server_url": "https://auth.{{ .Values.global.domain }}/oauth2/openid/paperless"}}]}}
          PAPERLESS_SOCIAL_AUTO_SIGNUP: "true"
          PAPERLESS_SOCIAL_ACCOUNT_SYNC_GROUPS: "true"
          PAPERLESS_SOCIAL_ACCOUNT_SYNC_GROUPS_CLAIM: homelab_groups
          PAPERLESS_SOCIAL_ACCOUNT_SYNC_SUPERUSER_GROUP: homelab-admins
          REQUESTS_CA_BUNDLE: '{{ if .Values.global.devCA }}/etc/homelab-dev-ca/ca.crt{{ end }}'
```
  Kubernetes expands `$(VAR)` only from earlier env entries; app-template renders an env map in alphabetical order, so `CLIENT_SECRET` precedes `PAPERLESS_SOCIALACCOUNT_PROVIDERS` (checked). Add the optional `dev-ca` volume of Task 7.
- [ ] **Step 3: Sandbox.** In `system/argocd/values-dev.yaml`, add `apps/paperless.yaml` to the `apps` generator files.
- [ ] **Step 4: Verify:** prod and dev renders: client, env order, JSON valid after substitution (`yq ... | .value` piped through `python3 -c 'import json,sys; json.loads(sys.stdin.read())'` with `$(CLIENT_SECRET)` present), `REQUESTS_CA_BUNDLE` empty in prod.
- [ ] **Step 5: Commit:** `feat(paperless): Kanidm SSO for homelab-documents, in the sandbox`.

---

### Task 9: Remove Dex

**Files:** `platform/dex/` (delete), `platform/global-secrets/values.yaml`, `system/argocd/values-dev.yaml`

- [ ] `git rm -r platform/dex` (then literal `rm -r platform/dex` for ignored leftovers); drop `dex.grafana`, `dex.gitea`, `dex.argocd`, `dex.zot` (and `dex.smoke` from dev extras if listed) from `global-secrets`; drop `platform/dex` from the sandbox `root` allowlist. Verify `grep -rn -i dex --include=*.yaml system platform apps metal | grep -v '/charts/'` shows only Argo CD's `dex.enabled: false`. Commit `chore: remove Dex`.

---

### Task 10: Docs and pre-PR checks

- [ ] **Docs:** `docs/getting-started/user-onboarding.md` (groups incl. `homelab-budget`/`homelab-documents`, grant with `scripts/onboard-user`); `docs/installation/sandbox.md` (Identity section without Dex; test accounts and groups; remove the `dex` RAM row; render examples use another chart); `docs/concepts/secrets-management.md` (the Dex restart example); `CLAUDE.md` (Sandbox: drop the Dex lines — `smoke` client is a Kanidm client, groups claim is `homelab_groups` from each app's client, no Dex restart; add the per-app client rule and access groups; production checklist: first Actual Budget OpenID user becomes the owner — add yourself to `homelab-budget` and log in first).
- [ ] **Checks:** render prod and dev of `platform/kanidm`, `platform/grafana`, `system/argocd` (also `seed`), `platform/zot`, `platform/global-secrets`, `apps/actualbudget.yaml`, `apps/paperless.yaml`; no `FAIL`, no literal `{{ .Values` in app renders, no `dex` outside `dex.enabled`; offline tests; `mkdocs build --strict`.
- [ ] Commit `docs: SSO directly with Kanidm`.

---

### Task 11: PR, merge, sandbox

- [ ] PR into `dev`; ask before merging.
- [ ] After the merge: `make dev` (expect the new SSO tests, 50 + new tests); if a client never gets a Secret, check `kubectl -n kanidm logs deploy/kanidm-kaniop` for namespace-selector messages.
- [ ] `make dev-clean && make dev`; measure memory (Prometheus per namespace, `docker stats`); report if above about 8 GiB.
- [ ] Mark step 5a done in `CLAUDE.md` (small PR).
