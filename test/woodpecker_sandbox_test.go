package test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// woodpeckerLogin signs account in to Forgejo through Kanidm, then to Woodpecker through Forgejo's OAuth (granting
// the application when Forgejo asks), and returns where the browser ended, its text and Woodpecker's cookies.
func woodpeckerLogin(t *testing.T, account string) (*url.URL, string, []*http.Cookie) {
	ctx, cancel := newBrowser(t)
	defer cancel()
	openLogin(t, ctx, account, "https://"+forgejoHost+"/user/oauth2/Kanidm")
	c := ensureCredentials(t, account)
	kanidmLogin(t, ctx, account, c, c.Password)
	var location string
	require.Eventually(t, func() bool {
		if chromedp.Run(ctx, chromedp.Location(&location)) != nil {
			return false
		}
		if acceptConsent(ctx) {
			return false
		}
		u, err := url.Parse(location)
		return err == nil && u.Host == forgejoHost && !strings.Contains(u.Path, "/login") && !strings.Contains(u.Path, "/oauth2")
	}, 30*time.Second, 500*time.Millisecond, "%s did not reach Forgejo; last at %s", account, location)

	browserStep(t, ctx, "woodpecker", account, chromedp.Navigate("https://"+ciHost+"/authorize"))
	var u *url.URL
	var text string
	require.Eventually(t, func() bool {
		if chromedp.Run(ctx, chromedp.Location(&location)) != nil {
			return false
		}
		var granted bool
		// Forgejo's grant page, shown once per account and application
		_ = chromedp.Run(ctx, chromedp.Evaluate(`(() => {
			const b = document.querySelector("#authorize-app") ||
				[...document.querySelectorAll("button")].find(b => b.innerText.trim() === "Authorize Application");
			if (b) b.click();
			return !!b;
		})()`, &granted))
		if granted {
			return false
		}
		var err error
		u, err = url.Parse(location)
		if err != nil || u.Host != ciHost || u.Path == "/authorize" {
			return false
		}
		var ready bool
		return chromedp.Run(ctx, chromedp.Evaluate(`document.readyState === "complete"`, &ready)) == nil && ready
	}, 30*time.Second, 500*time.Millisecond, "%s did not reach Woodpecker; last at %s", account, location)
	// Woodpecker's web UI renders after load
	time.Sleep(2 * time.Second)
	_ = chromedp.Run(ctx, chromedp.Evaluate(`document.body ? document.body.innerText.slice(0, 500) : ""`, &text))
	var cookies []*http.Cookie
	require.NoError(t, chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		got, err := network.GetCookies().WithURLs([]string{"https://" + ciHost + "/"}).Do(ctx)
		for _, c := range got {
			cookies = append(cookies, &http.Cookie{Name: c.Name, Value: c.Value})
		}
		return err
	})))
	return u, text, cookies
}

// woodpeckerRequest calls Woodpecker's API with a personal token or, without one, the session cookies and their
// CSRF token, and returns the status and body
func woodpeckerRequest(t *testing.T, method, path, token string, cookies []*http.Cookie, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, "https://"+ciHost+path, reader)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else {
		for _, c := range cookies {
			req.AddCookie(c)
		}
		if method != http.MethodGet {
			req.Header.Set("X-CSRF-TOKEN", woodpeckerCSRF(t, cookies))
		}
	}
	resp, err := insecureClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, raw
}

// woodpeckerCSRF is the session's CSRF token, which Woodpecker's web UI reads from /web-config.js
func woodpeckerCSRF(t *testing.T, cookies []*http.Cookie) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://"+ciHost+"/web-config.js", nil)
	require.NoError(t, err)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := insecureClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	m := regexp.MustCompile(`WOODPECKER_CSRF = "([^"]+)"`).FindSubmatch(raw)
	require.NotNil(t, m, "no CSRF token for this session")
	return string(m[1])
}

// woodpeckerToken signs account in and returns a personal token of theirs
func woodpeckerToken(t *testing.T, account string) string {
	t.Helper()
	u, text, cookies := woodpeckerLogin(t, account)
	require.NotEqual(t, "/login", u.Path, "%s was not signed in (%s):\n%s", account, u.Redacted(), text)
	status, raw := woodpeckerRequest(t, http.MethodPost, "/api/user/token", "", cookies, nil)
	require.Equal(t, http.StatusOK, status, "%s's personal token", account)
	token := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	require.NotEmpty(t, token)
	return token
}

// forgejoAs calls Forgejo's API as the site admin acting as user (Forgejo's Sudo header; "" acts as the admin)
func forgejoAs(t *testing.T, user, method, path string, body, out any) int {
	t.Helper()
	admin, password := secretValue(t, "forgejo", "forgejo-admin", "username"), secretValue(t, "forgejo", "forgejo-admin", "password")
	headers := []string{}
	if user != "" {
		headers = append(headers, "Sudo", user)
	}
	return getJSON(t, method, "https://"+forgejoHost+"/api/v1"+path, nil, admin, password, body, out, headers...)
}

// teamMembers lists the members of org ci's team
func teamMembers(t *testing.T, team string) []string {
	t.Helper()
	var teams []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	require.Equal(t, http.StatusOK, forgejoAs(t, "", "GET", "/orgs/ci/teams", nil, &teams))
	for _, tm := range teams {
		if tm.Name == team {
			var members []struct {
				Login string `json:"login"`
			}
			require.Equal(t, http.StatusOK, forgejoAs(t, "", "GET", fmt.Sprintf("/teams/%d/members", tm.ID), nil, &members))
			var names []string
			for _, m := range members {
				names = append(names, m.Login)
			}
			return names
		}
	}
	require.FailNow(t, "no team", "org ci has no team %s", team)
	return nil
}

const woodpeckerProbePipeline = `when:
  - event: push
clone:
  - name: clone
    image: docker.io/woodpeckerci/plugin-git:2.11.0
    settings:
      # The sandbox's CA cannot reach step pods; production verifies (docs/how-to-guides/woodpecker.md)
      skip_verify: true
steps:
  # What a pipeline may not do (docs/superpowers/specs/2026-10-21-woodpecker-design.md, decision 4): it asks for the
  # agent's service account and the namespace's Secret, and checks it got neither
  - name: escape
    image: docker.io/library/busybox:1.37.0
    backend_options:
      kubernetes:
        serviceAccountName: woodpecker-agent
        secrets:
          - name: woodpecker-secret
            key: WOODPECKER_AGENT_SECRET
            target:
              env: LEAKED
    commands:
      - test ! -e /var/run/secrets/kubernetes.io/serviceaccount/token
      - test -z "$LEAKED"
  - name: image
    image: quay.io/buildah/stable:v1.43.5
    environment:
      REGISTRY_USERNAME: {from_secret: registry_username}
      REGISTRY_PASSWORD: {from_secret: registry_password}
    commands:
      - image=` + registryHost + `/${CI_REPO_NAME}:${CI_COMMIT_SHA}
      - buildah --storage-driver vfs build --isolation chroot -t "$image" .
      - echo "$REGISTRY_PASSWORD" | buildah login --tls-verify=false -u "$REGISTRY_USERNAME" --password-stdin ` + registryHost + `
      - buildah --storage-driver vfs push --tls-verify=false "$image"
`

// TestWoodpecker: docs/superpowers/specs/2026-10-21-woodpecker-design.md (Tests, sandbox)
func TestWoodpecker(t *testing.T) {
	skipUnlessSandbox(t)
	waitForApp(t, "https://"+forgejoHost+"/api/healthz")
	lab(t, "infra", "forgejo", "sync", "--env", "sandbox")
	waitForApp(t, "https://"+ciHost+"/healthz")

	// Sign-in: only org ci's members (Kanidm homelab-admins → Owners, homelab-editors → developers)
	u, text, cookies := woodpeckerLogin(t, "dev-viewer")
	assert.Equal(t, "/login", u.Path)
	assert.Equal(t, "org_access_denied", u.Query().Get("error"), text)
	status, _ := woodpeckerRequest(t, http.MethodGet, "/api/user", "", cookies, nil)
	assert.Equal(t, http.StatusUnauthorized, status, "dev-viewer has a Woodpecker session")
	editor, admin := woodpeckerToken(t, "dev-editor"), woodpeckerToken(t, "dev-admin")
	assert.Contains(t, teamMembers(t, "developers"), "dev-editor")
	assert.NotContains(t, teamMembers(t, "developers"), "dev-viewer")
	assert.Contains(t, teamMembers(t, "Owners"), "dev-admin")

	// The probe repository, created by dev-editor in org ci and activated in Woodpecker
	name := fmt.Sprintf("woodpecker-probe-%d", time.Now().Unix())
	var repo struct {
		ID int64 `json:"id"`
	}
	require.Equal(t, http.StatusCreated, forgejoAs(t, "dev-editor", "POST", "/orgs/ci/repos",
		map[string]any{"name": name, "private": true, "auto_init": true, "default_branch": "master"}, &repo))
	t.Cleanup(func() { forgejoAs(t, "", "DELETE", "/repos/ci/"+name, nil, nil) })
	var active struct {
		ID    int64 `json:"id"`
		OrgID int64 `json:"org_id"`
	}
	status, raw := woodpeckerRequest(t, http.MethodPost, fmt.Sprintf("/api/repos?forge_remote_id=%d", repo.ID), editor, nil, nil)
	require.Equal(t, http.StatusOK, status, string(raw))
	require.NoError(t, json.Unmarshal(raw, &active))
	t.Cleanup(func() {
		woodpeckerRequest(t, http.MethodDelete, fmt.Sprintf("/api/repos/%d", active.ID), editor, nil, nil)
	})

	// The registry's credentials as org secrets, set by an owner (Woodpecker knows org ci once one of its repositories is active; its lookup by name answers 404)
	var org struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	status, raw = woodpeckerRequest(t, http.MethodGet, fmt.Sprintf("/api/orgs/%d", active.OrgID), admin, nil, nil)
	require.Equal(t, http.StatusOK, status, string(raw))
	require.NoError(t, json.Unmarshal(raw, &org))
	require.Equal(t, "ci", org.Name)
	for name, value := range map[string]string{"registry_username": "ci", "registry_password": baoValue(t, "zot/generated/ci", "password")} {
		path := fmt.Sprintf("/api/orgs/%d/secrets", org.ID)
		woodpeckerRequest(t, http.MethodDelete, path+"/"+name, admin, nil, nil)
		status, _ := woodpeckerRequest(t, http.MethodPost, path, admin, nil, map[string]any{"name": name, "value": value, "events": []string{"push"}})
		require.Equal(t, http.StatusOK, status, "org secret %s", name)
		t.Cleanup(func() { woodpeckerRequest(t, http.MethodDelete, path+"/"+name, admin, nil, nil) })
	}
	status, _ = woodpeckerRequest(t, http.MethodPost, fmt.Sprintf("/api/orgs/%d/secrets", org.ID), editor, nil,
		map[string]any{"name": "editor_probe", "value": "x", "events": []string{"push"}})
	assert.Equal(t, http.StatusForbidden, status, "a developer set an org secret")

	// The push that runs it
	files := []map[string]any{}
	for path, content := range map[string]string{
		"Dockerfile":       "FROM docker.io/library/busybox:1.37.0\nRUN echo probe > /probe.txt\n",
		".woodpecker.yaml": woodpeckerProbePipeline,
	} {
		files = append(files, map[string]any{"operation": "create", "path": path, "content": base64.StdEncoding.EncodeToString([]byte(content))})
	}
	var commit struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	require.Equal(t, http.StatusCreated, forgejoAs(t, "dev-editor", "POST", "/repos/ci/"+name+"/contents",
		map[string]any{"files": files, "message": "probe", "branch": "master"}, &commit))
	require.NotEmpty(t, commit.Commit.SHA)

	// Its step pods, inspected while they run: no privilege, a user namespace, not the agent's service account
	pods := k8s.NewKubectlOptions("", "", "woodpecker")
	pods.Logger = logger.Discard
	seen := map[string]string{}
	var pipeline struct {
		Number    int64  `json:"number"`
		Status    string `json:"status"`
		Commit    string `json:"commit"`
		Workflows []struct {
			Children []struct {
				ID    int64  `json:"id"`
				Name  string `json:"name"`
				State string `json:"state"`
			} `json:"children"`
		} `json:"workflows"`
	}
	done := assert.Eventually(t, func() bool {
		if out, err := k8s.RunKubectlAndGetOutputE(t, pods, "get", "pods", "-o",
			`jsonpath={range .items[?(@.metadata.name!="woodpecker-server-0")]}{.metadata.name} {.spec.hostUsers} {.spec.serviceAccountName} {.spec.containers[*].securityContext.privileged}{"\n"}{end}`); err == nil {
			for _, line := range strings.Split(out, "\n") {
				if f := strings.Fields(line); len(f) > 0 && strings.HasPrefix(f[0], "wp-") {
					seen[f[0]] = strings.Join(f[1:], " ")
				}
			}
		}
		var list []struct {
			Number int64  `json:"number"`
			Commit string `json:"commit"`
		}
		status, raw := woodpeckerRequest(t, http.MethodGet, fmt.Sprintf("/api/repos/%d/pipelines", active.ID), editor, nil, nil)
		if status != http.StatusOK || json.Unmarshal(raw, &list) != nil {
			return false
		}
		for _, p := range list {
			if p.Commit != commit.Commit.SHA {
				continue
			}
			status, raw := woodpeckerRequest(t, http.MethodGet, fmt.Sprintf("/api/repos/%d/pipelines/%d", active.ID, p.Number), editor, nil, nil)
			if status != http.StatusOK || json.Unmarshal(raw, &pipeline) != nil {
				return false
			}
			switch pipeline.Status {
			case "success", "failure", "error", "killed", "declined", "blocked":
				return true
			}
		}
		return false
	}, 10*time.Minute, 2*time.Second, "the probe's pipeline did not finish")
	require.True(t, done)
	if pipeline.Status != "success" {
		for _, w := range pipeline.Workflows {
			for _, step := range w.Children {
				_, raw := woodpeckerRequest(t, http.MethodGet, fmt.Sprintf("/api/repos/%d/logs/%d/%d", active.ID, pipeline.Number, step.ID), editor, nil, nil)
				var lines []struct {
					Data []byte `json:"data"`
				}
				_ = json.Unmarshal(raw, &lines)
				var log strings.Builder
				for _, l := range lines {
					log.Write(l.Data)
					log.WriteString("\n")
				}
				// Woodpecker masks from_secret values in logs
				t.Logf("step %s (%s):\n%s", step.Name, step.State, log.String())
			}
		}
		require.FailNow(t, "pipeline failed", "the probe's pipeline ended %s", pipeline.Status)
	}
	require.NotEmpty(t, seen, "no step pod was seen")
	t.Logf("step pods: %v", seen)
	for pod, spec := range seen {
		f := strings.Fields(spec)
		require.GreaterOrEqual(t, len(f), 2, "%s: %q", pod, spec)
		assert.Equal(t, "false", f[0], "%s: hostUsers", pod)
		assert.NotEqual(t, "woodpecker-agent", f[1], "%s runs as the agent's service account", pod)
		assert.NotContains(t, f[2:], "true", "%s has a privileged container", pod)
	}

	// zot serves the image
	assert.Equal(t, http.StatusOK, getJSON(t, "HEAD", "https://"+registryHost+"/v2/"+name+"/manifests/"+commit.Commit.SHA, nil,
		"ci", baoValue(t, "zot/generated/ci", "password"), nil, nil,
		"Accept", "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"))
}
