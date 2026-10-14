# OpenCloud with Kanidm OIDC Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** OpenCloud runs as `apps/opencloud.yaml` at `drive.<domain>`, logs in with a public Kanidm client, admits only `homelab-files` (`homelab-admins` → Admin, others → User), and the sandbox tests prove login, roles, refusal and file storage.

**Architecture:** One app-template values file adapted from upstream `nixos` (`52c32ab`), with a ConfigMap that adds OpenCloud's `proxy.yaml` role mapping and a `KanidmOAuth2Client` in `rawResources`. Kanidm gets the access group `homelab-files`. A Go/chromedp test logs in through the web UI, captures the bearer token the UI sends to `/graph`, and checks the role and a WebDAV round trip.

**Tech Stack:** app-template 5.3.0, OpenCloud 8.1.0 (`opencloud-rolling`), kaniop `KanidmOAuth2Client`, Go + chromedp + terratest, mkdocs.

**Spec:** `docs/superpowers/specs/2026-10-14-opencloud-design.md`

## Global Constraints

- Image `docker.io/opencloudeu/opencloud-rolling:8.1.0`, pinned exactly; app-template stays 5.3.0 (pinned by the `apps` ApplicationSet).
- Host `drive.{{ .Values.global.domain }}` on `gateway/homelab`, section `https`.
- Kanidm client `opencloud`, `public: true`, redirects `/`, `/oidc-callback.html`, `/oidc-silent-redirect.html`; `scopeMap` only `homelab-files`; claim `homelab_groups` with `homelab-admins` and `homelab-files`.
- Role mapping order: `admin` ← `homelab-admins`, then `user` ← `homelab-files`.
- Sandbox members: `homelab-files: [dev-admin, dev-editor]`; production `KanidmGroup`s never set `members`.
- PVCs `opencloud-config` (1Gi) and `opencloud-data` (20Gi).
- Branch `feat/opencloud` from `dev`, PR into `dev`; ask before merging.
- Basic checks before the PR: `scripts/render-chart` (prod and dev) for `apps/opencloud.yaml`, `apps/homepage.yaml`, `platform/kanidm`, `system/argocd`; `cd test && go vet ./... && go test -run 'TestSmokeAppsForEnv|TestIdentity(HasGroup|TOTPWaitsForNextStep|PKCE)|TestGatewayRouteReady' ./...`; `mkdocs build --strict`.
- Shell: `export KUBECONFIG=$PWD/metal/kubeconfig.yaml` for anything that talks to the sandbox.

## Review Focus

1. `dev-editor` (role User) listing `/graph/v1.0/applications`: the test resolves role names through it; if OpenCloud restricts it to admins, the editor case fails for a test reason, not a product one. Expected: readable by every user (no `requireAdmin` on the route in 8.1.0). Pinned by the editor case in Task 3.
2. Second login of the same person: must reuse the account (same Graph `id`, since the username is Kanidm's `sub`) and keep the role. Pinned in Task 3 (two logins per allowed account).
3. The personal drive does not exist yet right after the first login: `/graph/v1.0/me/drive` may answer 404 for a moment. Expected: the test waits for it instead of failing. Pinned in Task 3 (`require.Eventually`).
4. The config PVC's `opencloud.yaml` from `opencloud init` must survive restarts and never be regenerated (regenerating changes the internal secrets). Expected: the init container only runs `init` when the file is missing or empty. Pinned by the render check in Task 2 (the `-s` guard is in the rendered init args).
5. Production render: `SSL_CERT_DIR` must be empty and the `dev-ca` volume optional, so production pods start without the sandbox ConfigMap. Pinned by the prod render check in Task 2.

---

### Task 1: Kanidm access group `homelab-files`

**Files:**
- Modify: `platform/kanidm/templates/groups.yaml:1`
- Modify: `platform/kanidm/values.yaml:14`
- Modify: `platform/kanidm/values-dev.yaml` (`groupMembers`, `oauth2ClientNamespaces`)
- Modify: `scripts/onboard-user`

**Interfaces:**
- Produces: `KanidmGroup` `homelab-files` (namespace `kanidm`); kaniop accepts clients in namespace `opencloud`; `scripts/onboard-user ... files`.

- [ ] **Step 1: Run the checks before the change (they must fail)**

```bash
scripts/render-chart platform/kanidm dev | yq 'select(.kind == "KanidmGroup" and .metadata.name == "homelab-files") | .spec.members | join(",")'
scripts/render-chart platform/kanidm | yq 'select(.kind == "KanidmGroup" and .metadata.name == "homelab-files") | .metadata.name'
scripts/render-chart platform/kanidm dev | grep -c 'opencloud' || true
scripts/onboard-user u "U" u@example.com users files; echo "exit=$?"
```

Expected: the first two print nothing, the grep prints `0`, onboard-user prints `access group must be one of: budget, documents` and `exit=1`.

- [ ] **Step 2: Add the group, the namespace, the sandbox members and the script option**

`platform/kanidm/templates/groups.yaml` line 1:

```yaml
{{- range $group := list "homelab-admins" "homelab-editors" "homelab-users" "homelab-budget" "homelab-documents" "homelab-files" }}
```

`platform/kanidm/values.yaml` line 14:

```yaml
oauth2ClientNamespaces: [argocd, grafana, zot, actualbudget, paperless, forgejo, opencloud]
```

`platform/kanidm/values-dev.yaml`: add after `homelab-documents: [dev-admin, dev-editor]`:

```yaml
  homelab-files: [dev-admin, dev-editor]
```

and replace the namespaces line:

```yaml
oauth2ClientNamespaces: [argocd, grafana, zot, actualbudget, paperless, forgejo, opencloud, kanidm]
```

`scripts/onboard-user`: usage line becomes

```sh
# Usage: scripts/onboard-user <username> <fullname> <mail> <admins|editors|users> [budget] [documents] [files]
```

and the access check becomes

```sh
        budget|documents|files) ;;
        *) echo "access group must be one of: budget, documents, files" >&2; exit 1 ;;
```

- [ ] **Step 3: Run the checks again**

Same commands as Step 1, but the onboard-user line with an invalid group: `scripts/onboard-user u "U" u@example.com users nope; echo "exit=$?"`.

Expected: `dev-admin,dev-editor`; `homelab-files`; a count of at least `1`; `access group must be one of: budget, documents, files` and `exit=1`. Also `scripts/render-chart platform/kanidm | yq 'select(.kind == "KanidmGroup" and .metadata.name == "homelab-files") | .spec.members'` prints `null` (production sets no members).

- [ ] **Step 4: Commit**

```bash
git add platform/kanidm scripts/onboard-user
git commit -m "feat(kanidm): access group homelab-files for OpenCloud"
```

### Task 2: `apps/opencloud.yaml` and its wiring

**Files:**
- Create: `apps/opencloud.yaml`
- Modify: `system/argocd/values-dev.yaml` (`apps` generator files)
- Modify: `system/argocd/values.yaml` (editor exec policy)
- Modify: `apps/homepage.yaml` (after the Paperless entry)

**Interfaces:**
- Consumes: Task 1's group `homelab-files` and namespace `opencloud` in `oauth2ClientNamespaces`.
- Produces: Deployment `opencloud` (namespace `opencloud`), Service `opencloud:9200`, HTTPRoute host `drive.<domain>`, `KanidmOAuth2Client` `opencloud`, PVCs `opencloud-config` and `opencloud-data`.

- [ ] **Step 1: Run the render check before the file exists (must fail)**

```bash
scripts/render-chart apps/opencloud.yaml dev > /dev/null; echo "exit=$?"
```

Expected: an error that `apps/opencloud.yaml` does not exist (`exit` non-zero).

- [ ] **Step 2: Create `apps/opencloud.yaml`**

```yaml
# Adapted from upstream nixos apps/opencloud.yaml (52c32ab): Cilium Gateway, Kanidm client and role mapping
controllers:
  main:
    strategy: Recreate
    pod:
      securityContext:
        fsGroup: 1000
        fsGroupChangePolicy: OnRootMismatch
    defaultContainerOptions:
      image:
        repository: docker.io/opencloudeu/opencloud-rolling
        tag: 8.1.0
    initContainers:
      config:
        # Generates opencloud.yaml with OpenCloud's internal secrets once; they live on the config PVC
        command: [/bin/sh, -ec]
        args:
          - |
            if [ ! -s /etc/opencloud/opencloud.yaml ]; then
              exec opencloud init --quiet --insecure=false
            fi
        env:
          OC_URL: 'https://drive.{{ .Values.global.domain }}'
        resources:
          requests:
            cpu: 10m
            memory: 32Mi
          limits:
            memory: 128Mi
    containers:
      main:
        env:
          OC_URL: 'https://drive.{{ .Values.global.domain }}'
          OC_EXCLUDE_RUN_SERVICES: idp
          OC_OIDC_ISSUER: 'https://auth.{{ .Values.global.domain }}/oauth2/openid/opencloud'
          OC_OIDC_CLIENT_ID: opencloud
          OC_LDAP_SERVER_WRITE_ENABLED: 'true'
          PROXY_TLS: 'false'
          PROXY_OIDC_AUDIENCES: opencloud
          PROXY_OIDC_REWRITE_WELLKNOWN: 'true'
          PROXY_AUTOPROVISION_ACCOUNTS: 'true'
          PROXY_USER_OIDC_CLAIM: sub
          PROXY_AUTOPROVISION_CLAIM_USERNAME: sub
          GRAPH_USERNAME_MATCH: none
          PROXY_CSP_CONFIG_FILE_LOCATION: /etc/opencloud/csp.yaml
          # Roles from the Kanidm claim homelab_groups (read from userinfo); the mapping is in proxy.yaml
          PROXY_ROLE_ASSIGNMENT_DRIVER: oidc
          PROXY_ROLE_ASSIGNMENT_OIDC_CLAIM: homelab_groups
          # The sandbox CA (global.devCA); empty in production, where Go uses the system store
          SSL_CERT_DIR: '{{ if .Values.global.devCA }}/etc/ssl/certs:/etc/homelab-dev-ca{{ end }}'
        resources:
          requests:
            cpu: 100m
            memory: 512Mi
          limits:
            memory: 1536Mi
        probes:
          startup:
            enabled: true
            custom: true
            spec:
              tcpSocket:
                port: 9200
              periodSeconds: 5
              failureThreshold: 60
          readiness:
            enabled: true
            custom: true
            spec:
              exec:
                command: [curl, -fsS, http://127.0.0.1:9205/readyz]
service:
  main:
    controller: main
    ports:
      http:
        port: 9200
        protocol: HTTP
route:
  main:
    hostnames:
      - drive.{{ .Values.global.domain }}
    parentRefs:
      - name: homelab
        namespace: gateway
        sectionName: https
persistence:
  config:
    suffix: config
    accessMode: ReadWriteOnce
    size: 1Gi
    globalMounts:
      - path: /etc/opencloud
  data:
    suffix: data
    accessMode: ReadWriteOnce
    size: 20Gi
    globalMounts:
      - path: /var/lib/opencloud
  settings:
    # Over the config PVC: OpenCloud reads <service>.yaml from /etc/opencloud
    type: configMap
    identifier: settings
    advancedMounts:
      main:
        main:
          - path: /etc/opencloud/csp.yaml
            subPath: csp.yaml
            readOnly: true
          - path: /etc/opencloud/proxy.yaml
            subPath: proxy.yaml
            readOnly: true
  dev-ca:
    # The sandbox CA from trust-manager; absent in production, so the volume is optional
    type: custom
    volumeSpec:
      configMap:
        name: homelab-dev-ca
        optional: true
    advancedMounts:
      main:
        main:
          - path: /etc/homelab-dev-ca
            readOnly: true
configMaps:
  settings:
    data:
      csp.yaml: |
        directives:
          connect-src:
            - 'https://auth.{{ .Values.global.domain }}/'
      proxy.yaml: |
        # Only the config file can map claim values to roles; the first matching entry wins
        role_assignment:
          driver: oidc
          oidc_role_mapper:
            role_claim: homelab_groups
            role_mapping:
              - role_name: admin
                claim_value: homelab-admins
              - role_name: user
                claim_value: homelab-files
rawResources:
  kanidm-client:
    forceRename: opencloud
    manifest:
      apiVersion: kaniop.rs/v1beta1
      kind: KanidmOAuth2Client
      spec:
        kanidmRef:
          name: kanidm
          namespace: kanidm
        displayname: OpenCloud
        # No client secret: the web UI runs the code flow in the browser with PKCE
        public: true
        origin: 'https://drive.{{ .Values.global.domain }}'
        redirectUrl:
          - 'https://drive.{{ .Values.global.domain }}/'
          - 'https://drive.{{ .Values.global.domain }}/oidc-callback.html'
          - 'https://drive.{{ .Values.global.domain }}/oidc-silent-redirect.html'
        # Kanidm does not allow disabling the consent prompt on public clients: users confirm once
        preferShortUsername: true
        strictRedirectUrl: true
        claimMap:
          - name: homelab_groups
            joinStrategy: array
            valuesMap:
              - group: homelab-admins
                values: [homelab-admins]
              - group: homelab-files
                values: [homelab-files]
        scopeMap:
          - group: homelab-files
            scopes: [openid, profile, email, groups]
```

- [ ] **Step 3: Wire it in**

`system/argocd/values-dev.yaml`, `apps` generator `files` (keep alphabetical):

```yaml
              - path: apps/homepage.yaml
              - path: apps/opencloud.yaml
              - path: apps/paperless.yaml
```

`system/argocd/values.yaml`, after the `default/ollama` line:

```
        p, role:editor, exec, create, default/opencloud, allow
```

`apps/homepage.yaml`, after the Paperless entry:

```yaml
          - OpenCloud:
              href: https://drive.{{ .Values.global.domain }}
              description: File storage and sharing
              icon: open-cloud.svg
```

- [ ] **Step 4: Run the render checks**

```bash
out=$(scripts/render-chart apps/opencloud.yaml dev) && echo "$out" | yq 'select(.kind == "Deployment") | .spec.template.spec.containers[0].env[] | select(.name == "SSL_CERT_DIR") | .value'
echo "$out" | yq 'select(.kind == "HTTPRoute") | .spec.hostnames[0] + " " + .spec.parentRefs[0].namespace + "/" + .spec.parentRefs[0].name'
echo "$out" | yq 'select(.kind == "KanidmOAuth2Client") | .metadata.name + " public=" + (.spec.public | tostring) + " " + (.spec.scopeMap[].group)'
echo "$out" | yq 'select(.kind == "PersistentVolumeClaim") | .metadata.name'
echo "$out" | yq 'select(.kind == "Deployment") | .spec.template.spec.initContainers[0].args[0]' | grep -c '! -s /etc/opencloud/opencloud.yaml'
prod=$(scripts/render-chart apps/opencloud.yaml) && echo "$prod" | yq 'select(.kind == "Deployment") | .spec.template.spec.containers[0].env[] | select(.name == "SSL_CERT_DIR") | .value | length'
echo "$prod" | yq 'select(.kind == "Deployment") | .spec.template.spec.volumes[] | select(.name == "dev-ca") | .configMap.optional'
scripts/render-chart apps/homepage.yaml dev | grep -c 'drive.127-0-0-1.nip.io'
scripts/render-chart system/argocd dev | grep -c 'apps/opencloud.yaml'
scripts/render-chart system/argocd | grep -c 'default/opencloud'
```

Expected, in order: `/etc/ssl/certs:/etc/homelab-dev-ca`; `drive.127-0-0-1.nip.io gateway/homelab`; `opencloud public=true homelab-files`; `opencloud-config` and `opencloud-data`; `1`; `0`; `true`; `1`; at least `1`; `1`.

- [ ] **Step 5: Commit**

```bash
git add apps/opencloud.yaml apps/homepage.yaml system/argocd
git commit -m "feat(opencloud): OpenCloud with a public Kanidm client and role mapping"
```

### Task 3: Sandbox tests

**Files:**
- Modify: `test/apps_test.go` (dev list)
- Modify: `test/smoke_test.go` (dev list)
- Modify: `test/identity_helpers_test.go` (extract `acceptConsent`, add `openCloudLogin`, `openCloudAuthorizeURL`)
- Modify: `test/sso_test.go` (`opencloudHost`, `TestSSOOpenCloud`, `davRequest`)

**Interfaces:**
- Consumes: host `drive.127-0-0-1.nip.io`, client `opencloud`, roles from Task 2; existing `newBrowser`, `browserStep`, `kanidmLogin`, `browserDenied`, `ensureCredentials`, `getJSON`, `pkcePair`, `insecureClient`, `waitForApp`, `skipUnlessSandbox`.
- Produces: `acceptConsent(ctx context.Context) bool`; `openCloudLogin(t *testing.T, account string, c credentials, appBase string) string` (bearer token); `openCloudAuthorizeURL(appBase string) string`; `davRequest(t *testing.T, method, target, token, body string) (int, string)`.

- [ ] **Step 1: Write the failing offline test**

In `test/apps_test.go`, append to the `dev` list after `{"forgejo", "forgejo"},`:

```go
		{"opencloud", "opencloud"},
```

- [ ] **Step 2: Run it (must fail)**

Run: `cd test && go test -run TestSmokeAppsForEnv ./...`
Expected: FAIL, `smokeApps(dev) = [...] want [... {opencloud opencloud}]`.

- [ ] **Step 3: Add the app to the smoke list**

In `test/smoke_test.go`, dev list, after `{"forgejo", "forgejo"},`:

```go
			{"opencloud", "opencloud"},
```

- [ ] **Step 4: Run it (must pass)**

Run: `cd test && go test -run TestSmokeAppsForEnv ./...`
Expected: `ok`.

- [ ] **Step 5: Extract `acceptConsent` in `test/identity_helpers_test.go`**

Replace the consent block inside `appLogin`'s `assert.Eventually` callback:

```go
		// Public clients (zot) cannot skip Kanidm's consent page; accept it like a user would
		var consented bool
		_ = chromedp.Run(ctx, chromedp.Evaluate(`(() => {
			if (!document.body || !document.body.innerText.includes("Consent to Proceed")) return false;
			const b = [...document.querySelectorAll("button")].find(b => b.innerText.trim() === "Proceed");
			if (b) b.click();
			return !!b;
		})()`, &consented))
		if consented {
			return false
		}
```

with

```go
		if acceptConsent(ctx) {
			return false
		}
```

and add after `appLogin`:

```go
// acceptConsent clicks "Proceed" on Kanidm's consent page, which public clients (zot, OpenCloud) cannot skip,
// like a user would; it reports whether the page was there.
func acceptConsent(ctx context.Context) bool {
	var consented bool
	_ = chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		if (!document.body || !document.body.innerText.includes("Consent to Proceed")) return false;
		const b = [...document.querySelectorAll("button")].find(b => b.innerText.trim() === "Proceed");
		if (b) b.click();
		return !!b;
	})()`, &consented))
	return consented
}
```

- [ ] **Step 6: Add the OpenCloud login helpers after `actualOpenIDLogin`**

```go
// openCloudLogin opens OpenCloud's web UI, logs in through Kanidm (accepting the consent page of the public
// client) and returns the bearer token the web UI then sends to the Graph API. The web UI keeps its tokens in
// the browser, so there are no session cookies to reuse.
func openCloudLogin(t *testing.T, account string, c credentials, appBase string) string {
	ctx, cancel := newBrowser(t)
	defer cancel()
	tokens := make(chan string, 1)
	chromedp.ListenTarget(ctx, func(ev any) {
		e, ok := ev.(*network.EventRequestWillBeSent)
		if !ok || !strings.HasPrefix(e.Request.URL, appBase+"/graph/") {
			return
		}
		for name, value := range e.Request.Headers {
			if s, ok := value.(string); ok && strings.EqualFold(name, "Authorization") && strings.HasPrefix(s, "Bearer ") {
				select {
				case tokens <- strings.TrimPrefix(s, "Bearer "):
				default:
				}
			}
		}
	})

	browserStep(t, ctx, "open", account, chromedp.Navigate(appBase+"/"))
	// The web UI either redirects to Kanidm or first shows its own login button
	require.Eventually(t, func() bool {
		var ready bool
		_ = chromedp.Run(ctx, chromedp.Evaluate(`(() => {
			if (document.querySelector("input#username")) return true;
			const b = [...document.querySelectorAll("button, a")].find(b => /^(log ?in|sign in)$/i.test(b.innerText.trim()));
			if (b) b.click();
			return false;
		})()`, &ready))
		return ready
	}, 60*time.Second, 500*time.Millisecond, "%s: Kanidm's login form never showed", account)
	kanidmLogin(t, ctx, account, c, c.Password)

	deadline := time.After(60 * time.Second)
	for {
		select {
		case token := <-tokens:
			return token
		case <-deadline:
			var location, text string
			_ = chromedp.Run(ctx, chromedp.Location(&location), chromedp.Evaluate(`document.body ? document.body.innerText.slice(0, 500) : ""`, &text))
			require.FailNow(t, "no OpenCloud token", "%s: the web UI sent no bearer token to /graph; last at %s:\n%s", account, location, text)
		case <-time.After(500 * time.Millisecond):
			acceptConsent(ctx)
		}
	}
}

// openCloudAuthorizeURL is the authorization request OpenCloud's web UI sends to Kanidm, with a throwaway
// PKCE challenge: Kanidm refuses accounts outside homelab-files before redirecting back.
func openCloudAuthorizeURL(appBase string) string {
	_, challenge := pkcePair()
	return kanidmURL + "/ui/oauth2?" + url.Values{
		"client_id":             {"opencloud"},
		"redirect_uri":          {appBase + "/oidc-callback.html"},
		"response_type":         {"code"},
		"scope":                 {"openid profile email groups"},
		"state":                 {"denied-test"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}.Encode()
}
```

- [ ] **Step 7: Add `TestSSOOpenCloud` and `davRequest` to `test/sso_test.go`**

Add `opencloudHost = "drive.127-0-0-1.nip.io"` to the host constants and `"strings"` to the imports. Then, after `forgejoPage`:

```go
func TestSSOOpenCloud(t *testing.T) {
	skipUnlessSandbox(t)
	base := "https://" + opencloudHost
	waitForApp(t, base+"/")

	for _, c := range []struct {
		account string
		role    string // OpenCloud role; empty: Kanidm refuses the account (not in homelab-files)
	}{{"dev-admin", "Admin"}, {"dev-editor", "User"}, {"dev-viewer", ""}, {"dev-outsider", ""}} {
		t.Run(c.account, func(t *testing.T) {
			if c.role == "" {
				assert.Contains(t, browserDenied(t, c.account, ensureCredentials(t, c.account), openCloudAuthorizeURL(base)), "Access Denied")
				return
			}
			// Twice: the second login must reuse the account (keyed by Kanidm's sub) and keep the role
			var ids []string
			for range 2 {
				auth := []string{"Authorization", "Bearer " + openCloudLogin(t, c.account, ensureCredentials(t, c.account), base)}
				var me struct {
					ID                 string `json:"id"`
					DisplayName        string `json:"displayName"`
					AppRoleAssignments []struct {
						AppRoleID string `json:"appRoleId"`
					} `json:"appRoleAssignments"`
				}
				require.Equal(t, http.StatusOK, getJSON(t, "GET", base+"/graph/v1.0/me?$expand=appRoleAssignments", nil, "", "", nil, &me, auth...))
				assert.Equal(t, c.account, me.DisplayName)
				ids = append(ids, me.ID)

				var apps struct {
					Value []struct {
						AppRoles []struct {
							ID          string `json:"id"`
							DisplayName string `json:"displayName"`
						} `json:"appRoles"`
					} `json:"value"`
				}
				require.Equal(t, http.StatusOK, getJSON(t, "GET", base+"/graph/v1.0/applications", nil, "", "", nil, &apps, auth...))
				names := map[string]string{}
				for _, a := range apps.Value {
					for _, r := range a.AppRoles {
						names[r.ID] = r.DisplayName
					}
				}
				var roles []string
				for _, a := range me.AppRoleAssignments {
					roles = append(roles, names[a.AppRoleID])
				}
				assert.Equal(t, []string{c.role}, roles)
			}
			assert.Equal(t, ids[0], ids[1], "the second login created another account")

			// Files: a WebDAV round trip in the personal drive, which OpenCloud creates on first login
			token := openCloudLogin(t, c.account, ensureCredentials(t, c.account), base)
			auth := []string{"Authorization", "Bearer " + token}
			var drive struct {
				Root struct {
					WebDavURL string `json:"webDavUrl"`
				} `json:"root"`
			}
			require.Eventually(t, func() bool {
				return getJSON(t, "GET", base+"/graph/v1.0/me/drive", nil, "", "", nil, &drive, auth...) == http.StatusOK && drive.Root.WebDavURL != ""
			}, time.Minute, 2*time.Second, "no personal drive for %s", c.account)
			file := fmt.Sprintf("%s/homelab-test-%d.txt", strings.TrimSuffix(drive.Root.WebDavURL, "/"), time.Now().UnixNano())
			content := fmt.Sprintf("homelab test %s %d", c.account, time.Now().UnixNano())
			code, _ := davRequest(t, http.MethodPut, file, token, content)
			require.Equal(t, http.StatusCreated, code)
			code, got := davRequest(t, http.MethodGet, file, token, "")
			require.Equal(t, http.StatusOK, code)
			assert.Equal(t, content, got)
			code, _ = davRequest(t, http.MethodDelete, file, token, "")
			assert.Equal(t, http.StatusNoContent, code)
		})
	}
}

// davRequest sends a WebDAV request with a bearer token and returns the status and the body.
func davRequest(t *testing.T, method, target, token, body string) (int, string) {
	req, err := http.NewRequest(method, target, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := insecureClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(raw)
}
```

- [ ] **Step 8: Compile and run the offline checks**

Run: `cd test && go vet ./... && go test -run 'TestSmokeAppsForEnv|TestIdentity(HasGroup|TOTPWaitsForNextStep|PKCE)|TestGatewayRouteReady' ./...`
Expected: no vet output, `ok`.

- [ ] **Step 9: Check that the existing SSO tests still use the extracted consent helper (sandbox, zot)**

Run: `make env=dev ENV=dev INSECURE_SKIP_VERIFY=1 smoke_filter='TestSSOZot' smoke-test`
Expected: PASS (the sandbox runs `dev` without OpenCloud, so only the refactor is exercised here; `TestSSOOpenCloud` runs after the merge).

- [ ] **Step 10: Commit**

```bash
git add test
git commit -m "test(opencloud): SSO, roles and WebDAV through the web UI's token"
```

### Task 4: Docs

**Files:**
- Modify: `docs/installation/sandbox.md`
- Modify: `docs/getting-started/user-onboarding.md`
- Modify: `README.md:45`
- Modify: `CLAUDE.md`

**Interfaces:**
- Consumes: names from Tasks 1–3.

- [ ] **Step 1: `docs/installation/sandbox.md`**

After the budget/paperless link line add:

```markdown
- <https://drive.127-0-0-1.nip.io> (OpenCloud): log in with Kanidm and accept the consent page once
```

Replace the test account table:

```markdown
| Test account | Role group | Access groups |
|---|---|---|
| `dev-admin` | `homelab-admins` | `homelab-budget`, `homelab-documents`, `homelab-files` |
| `dev-editor` | `homelab-editors` | `homelab-documents`, `homelab-files` |
| `dev-viewer` | `homelab-users` | `homelab-budget` |
| `dev-outsider` | (none, refused) | (none) |
```

Replace the access sentence:

```markdown
Actual Budget admits only `homelab-budget`, paperless only `homelab-documents` (members of `homelab-admins` become paperless superusers), OpenCloud only `homelab-files` (members of `homelab-admins` get the Admin role, the others User).
```

Change the test sentence in "Make changes" to "the smoke, identity, SSO (including OpenCloud), Gateway and Forgejo tests".

- [ ] **Step 2: `docs/getting-started/user-onboarding.md`**

Add to the access group table:

```markdown
| `homelab-files` | OpenCloud | an account on first login (Kanidm asks once to consent); members of `homelab-admins` get the Admin role, the others User; the role follows the groups at each login |
```

Change the example to `... users budget files` and add to the offboarding paragraph: "OpenCloud keeps the files of removed people; delete their account in OpenCloud's user management."

- [ ] **Step 3: `README.md` line 45**

```markdown
- [x] Common applications: Forgejo, Jellyfin, Paperless, OpenCloud...
```

- [ ] **Step 4: `CLAUDE.md`**

- Roadmap item 5: `5c add opencloud (Kanidm OIDC, in the sandbox) (done)`.
- In the identity bullet, after the paperless sentence: `` `homelab-files` (OpenCloud, public client `opencloud`; its `proxy.yaml` ConfigMap maps the claim `homelab_groups` to roles, `homelab-admins` → Admin, others → User; OpenCloud reads the claim from Kanidm's userinfo, not the access token).``
- Sandbox CA bullet: add OpenCloud to the Go apps (`SSL_CERT_DIR`, only when `global.devCA` is set).
- "Before the first production deploy": `- OpenCloud only ran in the sandbox. `opencloud-config` holds the secrets `opencloud init` generated and the internal LDAP (accounts, shares); losing it loses them (backups in step 10). The desktop and mobile apps need their own public Kanidm clients.`

- [ ] **Step 5: Build the docs**

Run: `mkdocs build --strict 2>&1 | tail -3`
Expected: `Documentation built` and no warnings.

- [ ] **Step 6: Commit**

```bash
git add docs README.md CLAUDE.md
git commit -m "docs: OpenCloud with Kanidm"
```

### Task 5: After the merge into `dev` (sandbox)

Runs only after the PR is merged (ask before merging).

- [ ] **Step 1: Run the sandbox tests**

Run: `make dev 2>&1 | tee /tmp/claude-1000/make-dev-opencloud.log | tail -40`
Expected: all tests pass, including `TestSSOOpenCloud/dev-admin`, `dev-editor`, `dev-viewer`, `dev-outsider`; `opencloud` Synced/Healthy.

If `TestSSOOpenCloud` fails on the role, stop and ask (spec: fallback to the `default` driver). Other failures: fix forward on a new branch from `dev`.

- [ ] **Step 2: Measure RAM**

```bash
kubectl get --raw "/api/v1/namespaces/monitoring-system/services/monitoring-system-kube-pro-prometheus:9090/proxy/api/v1/query?query=sum%20by%20(namespace)%20(container_memory_working_set_bytes%7Bcontainer!%3D%22%22%7D)" | jq -r '.data.result[] | "\(.metric.namespace) \(.value[1] | tonumber / 1048576 | floor)"' | sort -k2 -n -r
docker stats --no-stream --format '{{.Name}} {{.MemUsage}}' homelab-dev-control-plane
```

Expected: an `opencloud` value; the node total below 8 GiB.

- [ ] **Step 3: Update the RAM table on a new branch and open a PR into `dev`**

Branch `docs/opencloud-ram` from `dev`; in `docs/installation/sandbox.md` add the `opencloud` row in order and update the other rows and the node total with the new measurements; `mkdocs build --strict`; commit `docs: sandbox RAM with OpenCloud`; PR into `dev`.
