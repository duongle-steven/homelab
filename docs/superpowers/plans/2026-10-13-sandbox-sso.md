# Sandbox SSO (1c) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Grafana, ArgoCD and zot log people in through Dex → Kanidm, grant permissions from the `homelab-*` groups, keep a local break-glass account each, and the sandbox tests prove every role.

**Architecture:**
- **Groups.** Kanidm emits a custom `homelab_groups` claim with short group names. Dex maps it to `groups`, so every app sees `["homelab-<x>"]` in both sandbox and production. The Kanidm client also prefers short usernames.
- **Generated secrets.** New client and admin secrets come from the 1b global-secrets generator.
- **Dev CA trust.** In the sandbox, each Go app trusts the dev CA through `SSL_CERT_DIR` plus its own ingress certificate's `ca.crt`.
- **Tests.** A shared chromedp login helper drives each app's SSO entry point; the tests then call the app's API with the session cookies.

**Tech Stack:** Grafana chart 13.5.0 (Grafana 13.2.3, repo `https://grafana-community.github.io/helm-charts`), argo-cd chart 10.10.3 (Argo CD 3.5.5), zot chart 0.1.128 (zot v2.1.22), kaniop 0.18.0, Dex 2.46.0, ESO 2.12.0, Go + chromedp v0.16.0.

**Spec:** `docs/superpowers/specs/2026-10-13-sandbox-sso-design.md`

## Global Constraints

- **Workflow:**
  - Work on branch `dev`, and push to `origin dev` when a task says so (the sandbox syncs from it).
  - One concern per commit. Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
  - Run everything inside the dev shell. For `kubectl`, `export KUBECONFIG=$PWD/metal/kubeconfig.yaml`.
  - `Chart.lock` is gitignored.
- **Versions:** current stable, pinned exactly (verified 2026-10-13): Grafana chart `13.5.0` from `https://grafana-community.github.io/helm-charts`, zot chart `0.1.128`, argo-cd chart `10.10.3` (unchanged).
- **Helm values:** Helm replaces lists and cannot delete keys across values files; dev overrides restate whole lists. A `null` only removes a **chart default**.
- **Groups:**
  - Names: `homelab-admins`, `homelab-editors`, `homelab-users`.
  - Claim: the `groups` claim from Dex must be exactly `["homelab-<x>"]`.
  - Username: Kanidm short name, e.g. `dev-admin`, via `preferShortUsername: true`.
- **Dev hosts:** `grafana.127-0-0-1.nip.io`, `argocd.127-0-0-1.nip.io`, `registry.127-0-0-1.nip.io`, `dex.127-0-0-1.nip.io`, `auth.127-0-0-1.nip.io`. Production keeps the domains already in `values.yaml`.
- **Dex static clients:**
  - `grafana-sso`, `argocd`, `zot`, plus `smoke` (dev only)
  - secrets `dex.grafana`, `dex.argocd`, `dex.zot`, `dex.smoke` (key `client_secret`)
  - env vars `GRAFANA_SSO_CLIENT_SECRET`, `ARGOCD_CLIENT_SECRET`, `ZOT_CLIENT_SECRET`, `SMOKE_CLIENT_SECRET`
- **Local accounts:**
  - Grafana `admin`, password from `grafana.admin` (key `password`)
  - ArgoCD `admin` (initial admin secret)
  - zot htpasswd `ci`, password from `registry.ci` (key `password`)
- **Dev CA trust (dev values only):** mount the app's ingress TLS secret key `ca.crt` at `/etc/homelab-dev-ca/ca.crt`, and set `SSL_CERT_DIR=/etc/ssl/certs:/etc/homelab-dev-ca`.
- **Test conventions:**
  - Identity and SSO tests connect to `sandboxIngress` (`172.30.255.200`) directly.
  - Credentials and tokens never go to test logs (`quietKanidmNamespace` or similar).
  - Every `TestSSO*` calls `waitForIdentityStack` first.
- **Make:** `make dev` runs the smoke filter `Smoke|Identity|SSO`.

## Review Focus

1. **Fresh-cluster bootstrap.** The ArgoCD chart now contains an `ExternalSecret`, but ESO's CRDs do not exist when `system/bootstrap.yml` applies ArgoCD. → Task 4 guards the template with `.Capabilities.APIVersions.Has` and checks an offline render has no ExternalSecret; Task 7 rebuilds from scratch.
2. **zot pods with auth enabled.** Probes must stay green (they use `/livez`/`/readyz`), and anonymous registry calls must get 401 rather than 200 from a lingering anonymous policy. → Task 5 waits for rollout and asserts anonymous 401.
3. **Rerun `make dev`.** zot API-key creation runs every time, so labels must not collide; Grafana and ArgoCD logins must work with stored credentials. → Task 5 uses a timestamped key label; Task 7 reruns.
4. **Production RBAC defaults.** A typo in `policy.csv` or a non-empty `policy.default` would silently give every SSO user access. → Task 4 asserts the prod render has `policy.default: ''` and the exact `g,` lines.
5. **Dex static client lists.** Dev must restate `grafana-sso`, `argocd`, `zot` and `smoke`; forgetting one breaks that app only in the sandbox. → Task 2 asserts the client ids in both renders.

---

## File Structure

| File | Responsibility |
|---|---|
| `platform/dex/templates/kanidm-client.yaml` | `claimMap` `homelab_groups`, `preferShortUsername` |
| `platform/dex/values.yaml`, `values-dev.yaml` | connector `claimMapping`, static clients `argocd`, `zot` |
| `platform/dex/templates/secret.yaml` | `ARGOCD_CLIENT_SECRET`, `ZOT_CLIENT_SECRET` |
| `platform/global-secrets/values.yaml` | `dex.argocd`, `dex.zot`, `grafana.admin`, `registry.ci` |
| `platform/grafana/{Chart.yaml,values.yaml,values-dev.yaml,templates/admin-secret.yaml}` | Grafana SSO and roles |
| `system/argocd/{values.yaml,values-dev.yaml,templates/oidc-secret.yaml}` | ArgoCD OIDC, RBAC, exec |
| `platform/zot/{Chart.yaml,values.yaml,values-dev.yaml,templates/auth-secret.yaml}` | zot OIDC, API keys, htpasswd, access control |
| `system/argocd/values-dev.yaml` (allowlist) | `platform/grafana`, `platform/zot` |
| `test/identity_helpers_test.go` | shared browser login (`newBrowser`, `kanidmLogin`, `appLogin`) |
| `test/identity_test.go` | exact `groups` assertion |
| `test/sso_test.go` (new) | `TestSSOGrafana`, `TestSSOArgoCD`, `TestSSOZot` |
| `test/smoke_test.go`, `test/apps_test.go` | dev smoke list adds grafana, zot |
| `Makefile` | `Smoke|Identity|SSO` |
| `docs/installation/sandbox.md`, `docs/getting-started/user-onboarding.md`, `CLAUDE.md` | docs |

---

### Task 1: Short group names and usernames from Kanidm

**Files:**
- Modify: `platform/dex/templates/kanidm-client.yaml`, `platform/dex/values.yaml`, `platform/dex/values-dev.yaml`
- Modify: `test/identity_test.go`

**Interfaces:**
- Produces: the Dex ID token for a `dev-*` account has `groups == ["homelab-<x>"]` and `preferred_username == "<account>"`.

- [ ] **Step 1: Write the failing assertion**

In `test/identity_test.go`, inside `TestIdentityLogin`'s subtest, replace

```go
			assert.True(t, hasGroup(claims, account.group), "groups %v lack %s", claims["groups"], account.group)
```

with

```go
			assert.Equal(t, []any{account.group}, claims["groups"])
			assert.Equal(t, account.name, claims["preferred_username"])
```

- [ ] **Step 2: Run it against the sandbox and watch it fail**

Run: `cd test && ENV=dev go test -run 'TestIdentityLogin/dev-admin' -count=1 ./...`
Expected: FAIL. `groups` holds SPNs, UUIDs and `idm_*` entries, and `preferred_username` is `dev-admin@auth.127-0-0-1.nip.io`.

- [ ] **Step 3: Kanidm client: claim map and short usernames**

In `platform/dex/templates/kanidm-client.yaml`, add under `spec:` (after `disableConsentPrompt: true`):

```yaml
  # Short usernames (dev-admin rather than dev-admin@<domain>)
  preferShortUsername: true
  # Short, domain-free group names for every app, mapped to `groups` by Dex
  claimMap:
    - name: homelab_groups
      joinStrategy: array
      valuesMap:
        {{- range list "homelab-admins" "homelab-editors" "homelab-users" }}
        - group: {{ . }}
          values:
            - {{ . }}
        {{- end }}
```

- [ ] **Step 4: Dex connector: read groups from `homelab_groups`**

In both `platform/dex/values.yaml` and `platform/dex/values-dev.yaml`, add to the Kanidm connector's `config:` (next to `getUserInfo: true`):

```yaml
          overrideClaimMapping: true
          claimMapping:
            groups: homelab_groups
```

- [ ] **Step 5: Verify renders**

Run:
```sh
for e in prod dev; do scripts/render-chart platform/dex $e | yq 'select(.kind=="Secret" and .metadata.name=="dex") | .data."config.yaml"' | base64 -d | yq '.connectors[0].config.claimMapping.groups'; done
scripts/render-chart platform/dex dev | yq 'select(.kind=="KanidmOAuth2Client") | .spec.preferShortUsername, (.spec.claimMap[0].valuesMap | map(.values[0]) | join(","))'
```
Expected: `homelab_groups` twice; then `true` and `homelab-admins,homelab-editors,homelab-users`.

- [ ] **Step 6: Commit, push, wait for Dex and the client**

```bash
git add platform/dex test/identity_test.go
git commit -m "feat(dex): short group names and usernames from Kanidm

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git push
```

Run:
```sh
kubectl -n argocd wait --for=jsonpath='{.status.sync.revision}'="$(git rev-parse HEAD)" application/dex --timeout=10m
kubectl -n dex rollout status deploy/dex --timeout=5m
kubectl -n dex wait --for=jsonpath='{.status.ready}'=true kanidmoauth2client/dex --timeout=5m
```

- [ ] **Step 7: Watch the test pass**

Run: `cd test && ENV=dev go test -run 'TestIdentity' -count=1 ./...`
Expected: PASS, including `TestIdentityDenied` and `TestIdentityDeniedNeedsLogin`.

If `groups` arrives as a string, read the ID token payload (log it once with `t.Logf("%v", claims)`, then remove the log) and fix `joinStrategy`; record a ruling.

---

### Task 2: Secrets and Dex clients for ArgoCD and zot

**Files:**
- Modify: `platform/global-secrets/values.yaml`
- Modify: `platform/dex/values.yaml`, `platform/dex/values-dev.yaml`, `platform/dex/templates/secret.yaml`

**Interfaces:**
- Produces:
  - secrets `dex.argocd`, `dex.zot`, `grafana.admin`, `registry.ci` in namespace `global-secrets`
  - Dex static clients `argocd` (redirect `https://<argocd host>/auth/callback`) and `zot` (redirect `https://<registry host>/zot/auth/callback/oidc`)

- [ ] **Step 1: Failing check (Review Focus 5)**

Run: `for e in prod dev; do scripts/render-chart platform/dex $e | yq 'select(.kind=="Secret" and .metadata.name=="dex") | .data."config.yaml"' | base64 -d | yq '.staticClients | map(.id) | join(",")'; done`
Expected: `grafana-sso`, then `grafana-sso,smoke`.

- [ ] **Step 2: New generated secrets**

Append to `secrets:` in `platform/global-secrets/values.yaml`:

```yaml
  - name: dex.argocd
    key: client_secret
  - name: dex.zot
    key: client_secret
  - name: grafana.admin
    key: password
    special: true
  - name: registry.ci
    key: password
```

- [ ] **Step 3: Static clients in `platform/dex/values.yaml`**

Replace `staticClients:` with:

```yaml
    staticClients:
      - id: grafana-sso
        name: Grafana
        redirectURIs:
          - 'https://grafana.khuedoan.com/login/generic_oauth'
        secretEnv: GRAFANA_SSO_CLIENT_SECRET
      - id: argocd
        name: Argo CD
        redirectURIs:
          - 'https://argocd.khuedoan.com/auth/callback'
        secretEnv: ARGOCD_CLIENT_SECRET
      - id: zot
        name: Registry
        redirectURIs:
          - 'https://registry.khuedoan.com/zot/auth/callback/oidc'
        secretEnv: ZOT_CLIENT_SECRET
```

- [ ] **Step 4: Static clients in `platform/dex/values-dev.yaml`**

Replace `staticClients:` with:

```yaml
    staticClients:
      - id: grafana-sso
        name: Grafana
        redirectURIs:
          - 'https://grafana.127-0-0-1.nip.io/login/generic_oauth'
        secretEnv: GRAFANA_SSO_CLIENT_SECRET
      - id: argocd
        name: Argo CD
        redirectURIs:
          - 'https://argocd.127-0-0-1.nip.io/auth/callback'
        secretEnv: ARGOCD_CLIENT_SECRET
      - id: zot
        name: Registry
        redirectURIs:
          - 'https://registry.127-0-0-1.nip.io/zot/auth/callback/oidc'
        secretEnv: ZOT_CLIENT_SECRET
      - id: smoke
        name: Smoke test
        redirectURIs:
          - 'https://smoke.127-0-0-1.nip.io/callback'
        secretEnv: SMOKE_CLIENT_SECRET
```

- [ ] **Step 5: `platform/dex/templates/secret.yaml`**

After the `GRAFANA_SSO_CLIENT_SECRET` entry, add:

```yaml
    - secretKey: ARGOCD_CLIENT_SECRET
      remoteRef:
        key: dex.argocd
        property: client_secret
    - secretKey: ZOT_CLIENT_SECRET
      remoteRef:
        key: dex.zot
        property: client_secret
```

- [ ] **Step 6: Verify renders**

Run Step 1's command again, then:
```sh
scripts/render-chart platform/global-secrets prod | yq 'select(.kind=="ExternalSecret") | .spec.target.name' | grep -v '^---' | wc -l
scripts/render-chart platform/dex prod | yq 'select(.kind=="ExternalSecret") | .spec.data[].secretKey' | grep -v '^---'
```
Expected: `grafana-sso,argocd,zot` and `grafana-sso,argocd,zot,smoke`; then `10`; then `GRAFANA_SSO_CLIENT_SECRET`, `ARGOCD_CLIENT_SECRET`, `ZOT_CLIENT_SECRET`.

- [ ] **Step 7: Commit, push, verify in the sandbox**

```bash
git add platform/global-secrets/values.yaml platform/dex
git commit -m "feat(dex): add ArgoCD and zot clients, generate their secrets

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git push
```

Run:
```sh
kubectl -n argocd wait --for=jsonpath='{.status.sync.revision}'="$(git rev-parse HEAD)" application/global-secrets application/dex --timeout=10m
kubectl -n global-secrets wait --for=condition=Ready externalsecret dex.argocd dex.zot grafana.admin registry.ci --timeout=5m
kubectl -n dex rollout status deploy/dex --timeout=5m
curl -sk --resolve dex.127-0-0-1.nip.io:443:172.30.255.200 -o /dev/null -w '%{http_code}\n' 'https://dex.127-0-0-1.nip.io/auth?client_id=argocd&redirect_uri=https%3A%2F%2Fargocd.127-0-0-1.nip.io%2Fauth%2Fcallback&response_type=code&scope=openid'
```
Expected: the four secrets are Ready, Dex rolls out, and the `/auth` request for client `argocd` returns `302` (redirect to Kanidm) rather than `400` (unknown client).

---

### Task 3: Grafana SSO with roles

**Files:**
- Create: `test/sso_test.go`
- Modify: `test/identity_helpers_test.go` (shared browser login)
- Modify: `platform/grafana/Chart.yaml`, `platform/grafana/values.yaml`
- Create: `platform/grafana/values-dev.yaml`, `platform/grafana/templates/admin-secret.yaml`
- Modify: `system/argocd/values-dev.yaml` (allowlist)

**Interfaces:**
- Consumes: Task 1 groups, Task 2 `grafana.admin`; 1b helpers (`ensureCredentials`, `clock`, `insecureClient`, `sandboxIngress`, `waitForIdentityStack`, `quietKanidmNamespace`, `testAccounts`).
- Produces:
  - `newBrowser(t) (context.Context, func())`
  - `kanidmLogin(t, ctx, account string, c credentials, password string)`
  - `appLogin(t, account string, c credentials, startURL, appHost string) []*http.Cookie`
  - `getJSON(t, method, target string, cookies []*http.Cookie, user, pass string, body, out any) int`
  - `TestSSOGrafana`

- [ ] **Step 1: Refactor the browser plumbing in `test/identity_helpers_test.go`**

Replace the whole `browserAuthorize` function with these four functions (behaviour of `browserAuthorize` is unchanged):

```go
// newBrowser starts headless chromium that resolves sandbox hosts to the ingress LB IP.
func newBrowser(t *testing.T) (context.Context, func()) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("ignore-certificate-errors", true),
		chromedp.Flag("host-resolver-rules", "MAP *.127-0-0-1.nip.io "+sandboxIngress),
		// NixOS-WSL exports an LD_LIBRARY_PATH built for a newer glibc than the dev shell's chromium
		chromedp.Env("LD_LIBRARY_PATH="),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancelBrowser := chromedp.NewContext(allocCtx)
	ctx, cancelTimeout := context.WithTimeout(ctx, 3*time.Minute)
	// Start the browser on the long-lived context; the first Run owns the browser's lifetime
	require.NoError(t, chromedp.Run(ctx, network.Enable()))
	return ctx, func() { cancelTimeout(); cancelBrowser(); cancelAlloc() }
}

// browserStep runs actions with a 30 s timeout and reports the page on failure.
func browserStep(t *testing.T, ctx context.Context, name, account string, actions ...chromedp.Action) {
	stepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := chromedp.Run(stepCtx, actions...); err != nil {
		var location, text string
		_ = chromedp.Run(ctx, chromedp.Location(&location), chromedp.Evaluate(`document.body ? document.body.innerText.slice(0, 500) : ""`, &text))
		require.NoError(t, err, "login step %q for %s at %s:\n%s", name, account, location, text)
	}
}

// kanidmLogin completes Kanidm's login form, which the browser must already show.
// Kanidm asks for the TOTP code before the password.
func kanidmLogin(t *testing.T, ctx context.Context, account string, c credentials, password string) {
	browserStep(t, ctx, "username", account, chromedp.SendKeys(`input#username`, account+"\n"), chromedp.WaitVisible(`input#totp`))
	browserStep(t, ctx, "totp", account, chromedp.SendKeys(`input#totp`, clock.code(t, account, c)+"\n"), chromedp.WaitVisible(`input#password`))
	browserStep(t, ctx, "password", account, chromedp.SendKeys(`input#password`, password+"\n"))
}

// browserAuthorize logs in through Dex and Kanidm with the given password. It returns the code sent to
// the smoke client, or an empty code and the text of the page the browser ended on.
func browserAuthorize(t *testing.T, account string, c credentials, password string) (string, string) {
	ctx, cancel := newBrowser(t)
	defer cancel()

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
	browserStep(t, ctx, "open", account, chromedp.Navigate(authURL), chromedp.WaitVisible(`input#username`))
	kanidmLogin(t, ctx, account, c, password)

	select {
	case code := <-codes:
		return code, ""
	case <-time.After(30 * time.Second):
		var text string
		require.NoError(t, chromedp.Run(ctx, chromedp.Evaluate(`document.body ? document.body.innerText : ""`, &text)))
		return "", text
	}
}

// appLogin opens an app's SSO entry point, logs in through Dex and Kanidm, waits until the browser is back
// on appHost (outside any callback or login path) and returns the cookies the app set.
func appLogin(t *testing.T, account string, c credentials, startURL, appHost string) []*http.Cookie {
	ctx, cancel := newBrowser(t)
	defer cancel()

	browserStep(t, ctx, "open", account, chromedp.Navigate(startURL), chromedp.WaitVisible(`input#username`))
	kanidmLogin(t, ctx, account, c, c.Password)

	require.Eventually(t, func() bool {
		var location string
		if chromedp.Run(ctx, chromedp.Location(&location)) != nil {
			return false
		}
		u, err := url.Parse(location)
		return err == nil && u.Host == appHost && !strings.Contains(u.Path, "callback") && !strings.Contains(u.Path, "/login")
	}, 30*time.Second, 500*time.Millisecond, "%s did not return to %s", account, appHost)

	var cookies []*http.Cookie
	require.NoError(t, chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		got, err := network.GetCookies().WithURLs([]string{"https://" + appHost + "/"}).Do(ctx)
		for _, c := range got {
			cookies = append(cookies, &http.Cookie{Name: c.Name, Value: c.Value})
		}
		return err
	})))
	require.NotEmpty(t, cookies, "no session cookies from %s", appHost)
	return cookies
}

// getJSON sends a request with cookies or basic auth, decodes a JSON body into out (if not nil) and returns the status.
func getJSON(t *testing.T, method, target string, cookies []*http.Cookie, user, pass string, body any, out any) int {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, target, reader)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := insecureClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	if out != nil && resp.StatusCode < 300 {
		require.NoError(t, json.Unmarshal(raw, out), "%s %s: %s", method, target, raw)
	}
	return resp.StatusCode
}
```

Run: `cd test && go vet ./... && ENV=dev go test -run 'TestIdentity' -count=1 ./...`
Expected: vet clean; identity tests still PASS (refactor only).

- [ ] **Step 2: Write `TestSSOGrafana` in `test/sso_test.go`**

```go
package test

import (
	"net/http"
	"os"
	"testing"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	grafanaHost  = "grafana.127-0-0-1.nip.io"
	argocdHost   = "argocd.127-0-0-1.nip.io"
	registryHost = "registry.127-0-0-1.nip.io"
)

func skipUnlessSandbox(t *testing.T) {
	if os.Getenv("ENV") != "dev" {
		t.Skip("SSO tests run against the sandbox only")
	}
	waitForIdentityStack(t)
}

// waitForApp blocks until the app answers on its sandbox host.
func waitForApp(t *testing.T, target string) {
	require.Eventually(t, func() bool {
		resp, err := insecureClient.Get(target)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode < 500
	}, 20*time.Minute, 15*time.Second, "%s is not ready", target)
}

// secretValue reads one key of a Secret without logging it.
func secretValue(t *testing.T, namespace, name, key string) string {
	options := k8s.NewKubectlOptions("", "", namespace)
	options.Logger = logger.Discard
	return string(k8s.GetSecret(t, options, name).Data[key])
}

func TestSSOGrafana(t *testing.T) {
	skipUnlessSandbox(t)
	base := "https://" + grafanaHost
	waitForApp(t, base+"/api/health")

	expected := map[string]struct {
		role         string
		grafanaAdmin bool
	}{
		"dev-admin":  {"Admin", true},
		"dev-editor": {"Editor", false},
		"dev-viewer": {"Viewer", false},
	}
	for account, want := range expected {
		t.Run(account, func(t *testing.T) {
			cookies := appLogin(t, account, ensureCredentials(t, account), base+"/login/generic_oauth", grafanaHost)

			var user struct {
				Login          string `json:"login"`
				IsGrafanaAdmin bool   `json:"isGrafanaAdmin"`
			}
			require.Equal(t, http.StatusOK, getJSON(t, "GET", base+"/api/user", cookies, "", "", nil, &user))
			assert.Equal(t, account, user.Login)
			assert.Equal(t, want.grafanaAdmin, user.IsGrafanaAdmin)

			var orgs []struct {
				Role string `json:"role"`
			}
			require.Equal(t, http.StatusOK, getJSON(t, "GET", base+"/api/user/orgs", cookies, "", "", nil, &orgs))
			require.Len(t, orgs, 1)
			assert.Equal(t, want.role, orgs[0].Role)
		})
	}

	t.Run("local-admin", func(t *testing.T) {
		password := secretValue(t, "grafana", "grafana-admin", "admin-password")
		assert.Equal(t, http.StatusOK, getJSON(t, "GET", base+"/api/user", nil, "admin", password, nil, nil))
	})
}
```

Add `"time"` to the imports.

- [ ] **Step 3: Watch it fail**

Run: `cd test && go vet ./... && ENV=dev go test -run 'TestSSOGrafana' -count=1 ./... 2>&1 | tail -5`
Expected: FAIL. Grafana is not in the sandbox (`waitForApp` times out). To avoid waiting 20 minutes, temporarily run with `-timeout 2m` and expect the timeout panic that names `TestSSOGrafana`.

- [ ] **Step 4: Chart and values**

`platform/grafana/Chart.yaml`: the dependency becomes

```yaml
  - name: grafana
    version: 13.5.0
    repository: https://grafana-community.github.io/helm-charts
```

In `platform/grafana/values.yaml`, under `grafana:`:
- add the admin secret:

```yaml
  admin:
    existingSecret: grafana-admin
```

- in `grafana.ini.auth.generic_oauth`, add:

```yaml
      role_attribute_path: "contains(groups[*], 'homelab-admins') && 'Admin' || contains(groups[*], 'homelab-editors') && 'Editor' || contains(groups[*], 'homelab-users') && 'Viewer'"
      role_attribute_strict: true
      allow_assign_grafana_admin: true
      login_attribute_path: preferred_username
```

- [ ] **Step 5: `platform/grafana/templates/admin-secret.yaml`**

```yaml
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: grafana-admin
  namespace: {{ .Release.Namespace }}
spec:
  secretStoreRef:
    kind: ClusterSecretStore
    name: global-secrets
  target:
    name: grafana-admin
    template:
      engineVersion: v2
      data:
        admin-user: admin
        admin-password: "{{ `{{ .password }}` }}"
  data:
    - secretKey: password
      remoteRef:
        key: grafana.admin
        property: password
```

- [ ] **Step 6: `platform/grafana/values-dev.yaml`**

```yaml
grafana:
  ingress:
    hosts:
      - &host grafana.127-0-0-1.nip.io
    tls:
      - secretName: grafana-general-tls
        hosts:
          - *host
  grafana.ini:
    server:
      root_url: https://grafana.127-0-0-1.nip.io
    auth.generic_oauth:
      auth_url: https://dex.127-0-0-1.nip.io/auth
      token_url: https://dex.127-0-0-1.nip.io/token
      api_url: https://dex.127-0-0-1.nip.io/userinfo
  # Trust the sandbox CA for calls to Dex
  env:
    SSL_CERT_DIR: /etc/ssl/certs:/etc/homelab-dev-ca
  extraSecretMounts:
    - name: dev-ca
      secretName: grafana-general-tls
      mountPath: /etc/homelab-dev-ca
      readOnly: true
      items:
        - key: ca.crt
          path: ca.crt
```

- [ ] **Step 7: Verify renders**

Run:
```sh
for e in prod dev; do scripts/render-chart platform/grafana $e | yq 'select(.kind=="ConfigMap" and .metadata.name=="grafana") | .data."grafana.ini"' | grep -E '^(root_url|role_attribute_strict|auth_url)'; done
scripts/render-chart platform/grafana dev | yq 'select(.kind=="Deployment") | .spec.template.spec.containers[] | select(.name=="grafana") | (.env[] | select(.name=="SSL_CERT_DIR") | .value), (.env[] | select(.name=="GF_SECURITY_ADMIN_PASSWORD") | .valueFrom.secretKeyRef.name)'
scripts/render-chart platform/grafana dev | yq 'select(.kind=="Ingress") | .spec.rules[0].host'
```
Expected:
- prod: `root_url = https://grafana.khuedoan.com`, `role_attribute_strict = true` and the khuedoan `auth_url`
- dev: the nip.io URLs
- `/etc/ssl/certs:/etc/homelab-dev-ca` and `grafana-admin`
- `grafana.127-0-0-1.nip.io`

- [ ] **Step 8: Allowlist, commit, push**

In `system/argocd/values-dev.yaml`, add `- path: platform/grafana` after `- path: platform/dex`.

```bash
git add platform/grafana system/argocd/values-dev.yaml test/identity_helpers_test.go test/sso_test.go
git commit -m "feat(grafana): SSO roles from homelab groups, chart from grafana-community

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git push
```

- [ ] **Step 9: Watch the test pass**

Run:
```sh
kubectl -n argocd wait --for=jsonpath='{.status.health.status}'=Healthy application/grafana --timeout=20m
cd test && ENV=dev go test -run 'TestSSOGrafana' -count=1 -v ./... 2>&1 | grep -E '^(=== RUN|--- |ok|FAIL)|Error'
```
Expected: `TestSSOGrafana/{dev-admin,dev-editor,dev-viewer,local-admin}` PASS.

If Grafana rejects the login with "role not found" or similar, check that `groups` reaches Grafana. Add `skip_org_role_sync: false` only if Grafana 13 defaults changed it, and record a ruling.

---

### Task 4: ArgoCD SSO, RBAC and exec

**Files:**
- Modify: `system/argocd/values.yaml`, `system/argocd/values-dev.yaml`
- Create: `system/argocd/templates/oidc-secret.yaml`
- Modify: `test/sso_test.go`

**Interfaces:**
- Consumes: Task 2 client `argocd` and secret `dex.argocd`; Task 3 helpers.
- Produces: `TestSSOArgoCD`.

- [ ] **Step 1: Write `TestSSOArgoCD` (append to `test/sso_test.go`)**

```go
func TestSSOArgoCD(t *testing.T) {
	skipUnlessSandbox(t)
	base := "https://" + argocdHost
	waitForApp(t, base+"/api/version")

	type check struct{ resource, action, object string }
	sync := check{"applications", "sync", "default/excalidraw"}
	del := check{"applications", "delete", "default/excalidraw"}
	exec := check{"exec", "create", "default/excalidraw"}
	expected := map[string]map[check]string{
		"dev-admin":  {sync: "yes", del: "yes", exec: "yes"},
		"dev-editor": {sync: "yes", del: "no", exec: "yes"},
		"dev-viewer": {sync: "no", del: "no", exec: "no"},
	}
	for account, checks := range expected {
		t.Run(account, func(t *testing.T) {
			cookies := appLogin(t, account, ensureCredentials(t, account), base+"/auth/login", argocdHost)
			for c, want := range checks {
				var answer struct {
					Value string `json:"value"`
				}
				require.Equal(t, http.StatusOK, getJSON(t, "GET", base+"/api/v1/account/can-i/"+c.resource+"/"+c.action+"/"+c.object, cookies, "", "", nil, &answer))
				assert.Equal(t, want, answer.Value, "%s %s %s", c.resource, c.action, c.object)
			}
		})
	}

	t.Run("local-admin", func(t *testing.T) {
		password := secretValue(t, "argocd", "argocd-initial-admin-secret", "password")
		var session struct {
			Token string `json:"token"`
		}
		require.Equal(t, http.StatusOK, getJSON(t, "POST", base+"/api/v1/session", nil, "", "", map[string]string{"username": "admin", "password": password}, &session))
		assert.NotEmpty(t, session.Token)
	})
}
```

- [ ] **Step 2: Watch it fail**

Run: `cd test && ENV=dev go test -run 'TestSSOArgoCD' -count=1 ./... 2>&1 | grep -E 'login step|did not return|--- FAIL' | head -5`
Expected: FAIL at the `open` step: `/auth/login` does not reach Kanidm, because SSO is not configured.

- [ ] **Step 3: `system/argocd/templates/oidc-secret.yaml`**

```yaml
{{- /* Skipped by the offline bootstrap render, when external-secrets does not exist yet */}}
{{- if .Capabilities.APIVersions.Has "external-secrets.io/v1/ExternalSecret" }}
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: argocd-oidc
  namespace: {{ .Release.Namespace }}
spec:
  secretStoreRef:
    kind: ClusterSecretStore
    name: global-secrets
  target:
    name: argocd-oidc
    template:
      metadata:
        labels:
          # Argo CD only resolves $secret:key references in Secrets with this label
          app.kubernetes.io/part-of: argocd
  data:
    - secretKey: clientSecret
      remoteRef:
        key: dex.argocd
        property: client_secret
{{- end }}
```

- [ ] **Step 4: `system/argocd/values.yaml`**

Under `argo-cd: configs: cm:` add:

```yaml
      # Needed for the editor role; also lets admins open a terminal in the UI
      exec.enabled: "true"
      oidc.config: |
        name: Kanidm
        issuer: https://dex.khuedoan.com
        clientID: argocd
        clientSecret: $argocd-oidc:clientSecret
        requestedScopes:
          - openid
          - profile
          - email
          - groups
```

Under `argo-cd: configs:` add:

```yaml
    rbac:
      # No access unless a group grants it
      policy.default: ''
      scopes: '[groups]'
      policy.csv: |
        p, role:editor, applications, get, */*, allow
        p, role:editor, applications, sync, */*, allow
        p, role:editor, applications, action/*, */*, allow
        p, role:editor, logs, get, */*, allow
        p, role:editor, exec, create, */*, allow
        p, role:editor, projects, get, *, allow
        p, role:editor, clusters, get, *, allow
        p, role:editor, repositories, get, *, allow
        g, homelab-admins, role:admin
        g, homelab-editors, role:editor
        g, homelab-users, role:readonly
```

- [ ] **Step 5: `system/argocd/values-dev.yaml`**

Under the existing `argo-cd:` key add:

```yaml
  configs:
    cm:
      oidc.config: |
        name: Kanidm
        issuer: https://dex.127-0-0-1.nip.io
        clientID: argocd
        clientSecret: $argocd-oidc:clientSecret
        requestedScopes:
          - openid
          - profile
          - email
          - groups
  server:
    # Trust the sandbox CA for calls to Dex
    env:
      - name: SSL_CERT_DIR
        value: /etc/ssl/certs:/etc/homelab-dev-ca
    volumes:
      - name: dev-ca
        secret:
          secretName: argocd-server-tls
          optional: true
          items:
            - key: ca.crt
              path: ca.crt
    volumeMounts:
      - name: dev-ca
        mountPath: /etc/homelab-dev-ca
        readOnly: true
```

(`optional: true`: `argocd-server-tls` is issued by cert-manager after ArgoCD's first start during bootstrap; the server restarts when the RBAC/CM sync rolls it out.)

- [ ] **Step 6: Verify renders (Review Focus 1, 4)**

Run:
```sh
scripts/render-chart system/argocd prod | yq 'select(.kind=="ConfigMap" and .metadata.name=="argocd-rbac-cm") | .data."policy.default", .data.scopes' 
scripts/render-chart system/argocd prod | yq 'select(.kind=="ConfigMap" and .metadata.name=="argocd-rbac-cm") | .data."policy.csv"' | grep '^g,'
scripts/render-chart system/argocd dev | yq 'select(.kind=="ConfigMap" and .metadata.name=="argocd-cm") | .data."oidc.config"' | grep issuer
scripts/render-chart system/argocd dev | yq 'select(.kind=="ExternalSecret") | .metadata.name' | grep -v '^---' | wc -l
helm template argocd system/argocd -n argocd --api-versions external-secrets.io/v1/ExternalSecret | yq 'select(.kind=="ExternalSecret") | .metadata.name' | grep -v '^---'
scripts/render-chart system/argocd prod | yq 'select(.kind=="ClusterRole" or .kind=="Role") | select(.metadata.name | test("server")) | .rules[] | select(.resources[]? == "pods/exec") | .verbs' | head -3
```
Expected:
- `''` (empty), `[groups]`
- three `g,` lines
- issuer `https://dex.127-0-0-1.nip.io`
- `0` (the offline render skips the ExternalSecret)
- `argocd-oidc` when the API version is passed
- a `create` verb for `pods/exec`

If the server role has no `pods/exec` rule, add one through `server.clusterRoleRules` and record a ruling.

- [ ] **Step 7: Commit, push, watch the test pass**

```bash
git add system/argocd test/sso_test.go
git commit -m "feat(argocd): SSO through Dex with admin, editor and viewer roles

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git push
```

Run:
```sh
kubectl -n argocd wait --for=jsonpath='{.status.sync.revision}'="$(git rev-parse HEAD)" application/argocd --timeout=15m
kubectl -n argocd rollout status deploy/argocd-server --timeout=10m
kubectl -n argocd get secret argocd-oidc -o jsonpath='{.metadata.labels.app\.kubernetes\.io/part-of}'; echo
cd test && ENV=dev go test -run 'TestSSOArgoCD' -count=1 -v ./... 2>&1 | grep -E '^(=== RUN|--- |ok|FAIL)|Error'
```
Expected: `argocd`, then `TestSSOArgoCD/{dev-admin,dev-editor,dev-viewer,local-admin}` PASS.

---

### Task 5: zot with OIDC, API keys, htpasswd and access control

**Files:**
- Modify: `platform/zot/Chart.yaml`, `platform/zot/values.yaml`
- Create: `platform/zot/values-dev.yaml`, `platform/zot/templates/auth-secret.yaml`
- Modify: `system/argocd/values-dev.yaml` (allowlist), `test/sso_test.go`

**Interfaces:**
- Consumes: Task 2 client `zot`, secrets `dex.zot`, `registry.ci`; Task 3 helpers.
- Produces: `TestSSOZot`; zot at `https://registry.127-0-0-1.nip.io`.

- [ ] **Step 1: Write `TestSSOZot` (append to `test/sso_test.go`)**

```go
func TestSSOZot(t *testing.T) {
	skipUnlessSandbox(t)
	base := "https://" + registryHost
	waitForApp(t, base+"/livez")
	upload := base + "/v2/sso-test/blobs/uploads/"

	expectedUpload := map[string]int{
		"dev-admin":  http.StatusAccepted,
		"dev-editor": http.StatusAccepted,
		"dev-viewer": http.StatusForbidden,
	}
	for account, want := range expectedUpload {
		t.Run(account, func(t *testing.T) {
			cookies := appLogin(t, account, ensureCredentials(t, account), base+"/zot/auth/login?provider=oidc", registryHost)

			var key struct {
				APIKey string `json:"apiKey"`
			}
			label := fmt.Sprintf("sso-test-%d", time.Now().UnixNano())
			require.Equal(t, http.StatusCreated, getJSON(t, "POST", base+"/zot/auth/apikey", cookies, "", "", map[string]any{"label": label, "scopes": []string{}}, &key))
			require.NotEmpty(t, key.APIKey)

			assert.Equal(t, want, getJSON(t, "POST", upload, nil, account, key.APIKey, nil, nil))
			assert.Equal(t, http.StatusOK, getJSON(t, "GET", base+"/v2/_catalog", nil, account, key.APIKey, nil, nil))
		})
	}

	t.Run("ci", func(t *testing.T) {
		password := secretValue(t, "global-secrets", "registry.ci", "password")
		assert.Equal(t, http.StatusAccepted, getJSON(t, "POST", upload, nil, "ci", password, nil, nil))
	})

	t.Run("anonymous", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, getJSON(t, "GET", base+"/v2/_catalog", nil, "", "", nil, nil))
	})
}
```

Add `"fmt"` to the imports.

- [ ] **Step 2: Watch it fail**

Run: `cd test && ENV=dev go test -run 'TestSSOZot' -count=1 -timeout 2m ./... 2>&1 | tail -3`
Expected: FAIL, a timeout in `waitForApp` (zot is not in the sandbox).

- [ ] **Step 3: Probe the API-key contract before relying on it**

Read zot's handler to confirm the request body fields, the response field name and the status code:

```sh
curl -sL https://raw.githubusercontent.com/project-zot/zot/v2.1.22/pkg/api/routes.go | grep -n -A40 'func (rh \*RouteHandler) CreateAPIKey' | grep -nE 'json|Status|Label|Scopes|APIKey' | head -20
```
Expected: a `label` field, an `apiKey` response field and `http.StatusCreated`. If the names differ, adjust the struct and body in Step 1 and record a ruling.

- [ ] **Step 4: Chart bump**

`platform/zot/Chart.yaml`: `version: 0.1.128`.

- [ ] **Step 5: Replace `platform/zot/values.yaml`**

```yaml
zot:
  ingress:
    enabled: true
    annotations:
      cert-manager.io/cluster-issuer: letsencrypt-prod
      nginx.ingress.kubernetes.io/proxy-body-size: "0"
    className: nginx
    hosts:
      - host: &host registry.khuedoan.com
        paths:
          - path: /
    tls:
      - secretName: zot-tls-certificate
        hosts:
          - *host
  persistence: true
  pvc:
    create: true
    storage: 10Gi
  # The config below replaces the chart's sample config.json
  configFiles:
    config.json: null
  config:
    distSpecVersion: "1.1.1"
    storage:
      rootDirectory: /var/lib/registry
    http:
      address: 0.0.0.0
      port: "5000"
      externalUrl: https://registry.khuedoan.com
      auth:
        # Machine account for CI
        htpasswd:
          path: /secret/zot-auth/htpasswd
        # People log in to the UI through Dex and create API keys for docker/crane
        apikey: true
        openid:
          providers:
            oidc:
              name: Kanidm
              issuer: https://dex.khuedoan.com
              credentialsFile: /secret/zot-auth/oidc-credentials.json
              scopes:
                - openid
                - profile
                - email
                - groups
              claimMapping:
                username: preferred_username
                groups: groups
      accessControl:
        repositories:
          "**":
            policies:
              - groups: [homelab-admins]
                actions: [read, create, update, delete]
              - groups: [homelab-editors]
                actions: [read, create, update]
              - groups: [homelab-users]
                actions: [read]
              - users: [ci]
                actions: [read, create, update]
            defaultPolicy: []
    log:
      level: info
    extensions:
      search:
        enable: true
      ui:
        enable: true
  externalSecrets:
    - secretName: zot-auth
      mountPath: /secret/zot-auth
```

- [ ] **Step 6: `platform/zot/templates/auth-secret.yaml`**

```yaml
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: zot-auth
  namespace: {{ .Release.Namespace }}
spec:
  secretStoreRef:
    kind: ClusterSecretStore
    name: global-secrets
  target:
    name: zot-auth
    template:
      engineVersion: v2
      data:
        htpasswd: "{{ `{{ htpasswd \"ci\" .ci_password }}` }}"
        oidc-credentials.json: "{{ `{\"clientid\":\"zot\",\"clientsecret\":\"{{ .client_secret }}\"}` }}"
  data:
    - secretKey: ci_password
      remoteRef:
        key: registry.ci
        property: password
    - secretKey: client_secret
      remoteRef:
        key: dex.zot
        property: client_secret
```

- [ ] **Step 7: `platform/zot/values-dev.yaml`**

```yaml
zot:
  ingress:
    hosts:
      - host: &host registry.127-0-0-1.nip.io
        paths:
          - path: /
    tls:
      - secretName: zot-tls-certificate
        hosts:
          - *host
  config:
    http:
      externalUrl: https://registry.127-0-0-1.nip.io
      auth:
        openid:
          providers:
            oidc:
              issuer: https://dex.127-0-0-1.nip.io
  # Trust the sandbox CA for calls to Dex
  env:
    - name: SSL_CERT_DIR
      value: /etc/ssl/certs:/etc/homelab-dev-ca
  extraVolumes:
    - name: dev-ca
      secret:
        secretName: zot-tls-certificate
        items:
          - key: ca.crt
            path: ca.crt
  extraVolumeMounts:
    - name: dev-ca
      mountPath: /etc/homelab-dev-ca
      readOnly: true
```

- [ ] **Step 8: Verify renders**

Run:
```sh
for e in prod dev; do scripts/render-chart platform/zot $e | yq 'select(.kind=="Secret" and .metadata.name=="zot-config") | .data."config.json"' | base64 -d | jq -c '[.http.externalUrl, .http.auth.openid.providers.oidc.issuer, .http.auth.apikey, (.http.accessControl.repositories["**"].policies | length)]'; done
scripts/render-chart platform/zot dev | yq 'select(.kind=="StatefulSet") | .spec.template.spec.volumes[].name' | grep -v '^---'
scripts/render-chart platform/zot prod | yq 'select(.kind=="ConfigMap") | .metadata.name' | grep -v '^---'; echo configmaps-checked
```
Expected:
- prod: `["https://registry.khuedoan.com","https://dex.khuedoan.com",true,4]`
- dev: the nip.io pair, with `true,4`
- the StatefulSet volumes include `zot-config`, `zot-auth` and `dev-ca`
- no sample `config.json` ConfigMap, only `configmaps-checked`

If `zot-config` is not mounted in the StatefulSet, the template differs from the Deployment one (Task 5 research looked at `deployment.yaml`). Mount it through `extraVolumes`/`extraVolumeMounts` instead and record a ruling.

- [ ] **Step 9: Allowlist, commit, push**

In `system/argocd/values-dev.yaml`, add `- path: platform/zot` after `- path: platform/grafana`.

```bash
git add platform/zot system/argocd/values-dev.yaml test/sso_test.go
git commit -m "feat(zot): OIDC login, API keys, a CI account and group-based access

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git push
```

- [ ] **Step 10: Watch the test pass (Review Focus 2, 3)**

Run:
```sh
kubectl -n argocd wait --for=jsonpath='{.status.health.status}'=Healthy application/zot --timeout=20m
kubectl -n zot rollout status statefulset/zot --timeout=10m
cd test && ENV=dev go test -run 'TestSSOZot' -count=1 -v ./... 2>&1 | grep -E '^(=== RUN|--- |ok|FAIL)|Error'
cd test && ENV=dev go test -run 'TestSSOZot' -count=1 ./... 2>&1 | tail -1
```
Expected:
- the rollout is complete (probes are green with auth on)
- `TestSSOZot/{dev-admin,dev-editor,dev-viewer,ci,anonymous}` PASS
- the second run PASSes too (new key labels)

If the API-key basic auth fails with 401, read the username zot stored: in the UI session call `GET /v2/_zot/ext/userprefs` or check `kubectl -n zot logs statefulset/zot | grep -i identity`. Align `claimMapping.username`, and record a ruling.

---

### Task 6: Smoke list and `make dev`

**Files:**
- Modify: `test/smoke_test.go`, `test/apps_test.go`, `Makefile`

- [ ] **Step 1: Failing unit test**

In `test/apps_test.go`, extend the `dev` slice with:

```go
		{"grafana", "grafana"},
		{"zot", "zot"},
```

Run: `cd test && go test -run TestSmokeAppsForEnv ./...`
Expected: FAIL (smokeApps lacks them).

- [ ] **Step 2: Implement**

In `test/smoke_test.go`, add the same two entries to the dev branch of `smokeApps`. Then run `kubectl -n grafana get ingress; kubectl -n zot get ingress` and confirm the ingress names are `grafana` and `zot`; if they differ, use the real names in both files.

In `Makefile`, change `smoke_filter='Smoke|Identity'` to `smoke_filter='Smoke|Identity|SSO'` in the `dev` recipe.

- [ ] **Step 3: Run the whole sandbox suite**

Run: `make env=dev ENV=dev INSECURE_SKIP_VERIFY=1 smoke_filter='Smoke|Identity|SSO' smoke-test 2>&1 | grep -E '^(FAIL|DONE)'`
Expected: `DONE … tests` with no FAIL.

- [ ] **Step 4: Commit, push**

```bash
git add test/smoke_test.go test/apps_test.go Makefile
git commit -m "test: run the SSO tests in make dev, smoke-check Grafana and zot

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git push
```

---

### Task 7: Acceptance from scratch and RAM

**Files:** none (fixes go to the owning task's files)

- [ ] **Step 1: From scratch (Review Focus 1)**

Run: `make dev-clean && time make dev` (in the background, 25+ minutes)
Expected: exit 0; all smoke, identity and SSO tests PASS (criterion 1). If the bootstrap fails on the ArgoCD `ExternalSecret`, the Capabilities guard is wrong.

- [ ] **Step 2: Rerun (Review Focus 3)**

Run: `make dev`
Expected: exit 0 in a few minutes.

- [ ] **Step 3: Applications, renders, targets**

Run:
```sh
kubectl -n argocd get applications --no-headers | awk '{print $1, $2, $3}'
for d in platform/dex platform/global-secrets platform/grafana platform/zot system/argocd; do for e in prod dev; do scripts/render-chart $d $e >/dev/null || echo "FAIL $d $e"; done; done; echo renders-checked
kubectl -n monitoring-system port-forward svc/monitoring-system-kube-pro-prometheus 9090 >/dev/null 2>&1 &
sleep 4
curl -s 'http://127.0.0.1:9090/api/v1/targets?state=active' | jq -r '.data.activeTargets[] | select(.health!="up") | .labels.job + " " + .lastError'
curl -s 'http://127.0.0.1:9090/api/v1/query' --data-urlencode 'query=sort_desc(sum by (namespace) (container_memory_working_set_bytes{container!=""}))' | jq -r '.data.result[] | "| \(.metric.namespace) | \((.value[1] | tonumber) / 1048576 | floor) MiB |"'
kill %1
docker stats --no-stream homelab-dev-control-plane --format '{{.MemUsage}}'
```
Expected:
- 11 apps, all `Synced Healthy`
- `renders-checked` only
- no down targets
- the RAM table, kept for Task 8

---

### Task 8: Docs

**Files:**
- Modify: `docs/installation/sandbox.md`, `docs/getting-started/user-onboarding.md`, `CLAUDE.md`

- [ ] **Step 1: `docs/installation/sandbox.md`**

In `## Explore`, add to the link list:

```markdown
- <https://grafana.127-0-0-1.nip.io> ("Sign in with Dex", or user `admin` with `kubectl -n grafana get secret grafana-admin -o jsonpath='{.data.admin-password}' | base64 -d`)
- <https://registry.127-0-0-1.nip.io> (zot; log in with Kanidm, then create an API key under your user menu and `docker login registry.127-0-0-1.nip.io -u <username> -p <key>`)
```

After the `### Identity` table, add:

```markdown
What each group may do:

| Group | Grafana | Argo CD | zot |
|---|---|---|---|
| `homelab-admins` | Admin, server admin | admin | read, push, delete |
| `homelab-editors` | Editor | view, sync, actions, logs, exec | read, push |
| `homelab-users` | Viewer | read-only | read |

Argo CD and Grafana keep their local `admin` account; zot has a `ci` account for automation (password in `global-secrets/registry.ci`).
```

Replace the `## Resource usage` table with the numbers from Task 7 Step 3 (keep the kube-system label and the node total row), and update the prerequisite line's RAM figure if the node total moved by more than 0.5 GiB.

- [ ] **Step 2: `docs/getting-started/user-onboarding.md`**

In the "For admin" tab, after the reset-link sentence, add:

```markdown
    The group decides access everywhere: `admins` administer Grafana, Argo CD and the registry; `editors` edit Grafana dashboards, sync Argo CD apps and push images; `users` view and pull.
```

- [ ] **Step 3: `CLAUDE.md`**

- Roadmap line 1: change `1c SSO for Grafana, ArgoCD and zot` to `1c SSO for Grafana, ArgoCD and zot (done)`.
- Under `## Sandbox`, replace the sentence ``The `groups` claim carries SPNs (`homelab-admins@<auth domain>`) plus UUIDs.`` with ``Kanidm sends short group names in `homelab_groups`, which Dex maps to `groups`; apps match `homelab-admins|editors|users`.`` and add:

```markdown
- Go apps (Grafana, Argo CD, zot) trust the sandbox CA through `SSL_CERT_DIR` and their own ingress certificate's `ca.crt`, set only in `values-dev.yaml`.
```

- Under `## Before the first production deploy`, add:

```markdown
- Argo CD has `exec.enabled: "true"` (needed for the editor role), so admins and editors can open terminals in pods.
- The Grafana chart now comes from `https://grafana-community.github.io/helm-charts`.
```

- [ ] **Step 4: Check and commit**

Run: `mkdocs build --strict -d "$(mktemp -d)" 2>&1 | grep -E 'WARNING|ERROR'; echo docs-checked`
Expected: only `docs-checked`.

```bash
git add docs/installation/sandbox.md docs/getting-started/user-onboarding.md CLAUDE.md
git commit -m "docs: SSO permissions per group, Grafana and zot in the sandbox

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git push
```
