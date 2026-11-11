package e2e

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
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// woodpeckerLogin signs account in to Forgejo through Kanidm, then to Woodpecker through Forgejo's OAuth (granting
// the application when Forgejo asks), and returns where the browser ended, its text and Woodpecker's cookies.
func woodpeckerLogin(specCtx context.Context, account string) (*url.URL, string, []*http.Cookie) {
	GinkgoHelper()
	ctx, cancel := newBrowser(specCtx)
	defer cancel()
	openLogin(ctx, account, "https://"+forgejoHost+"/user/oauth2/Kanidm")
	c := ensureCredentials(specCtx, account)
	kanidmLogin(ctx, account, c, c.Password)
	var location string
	Eventually(ctx, func() bool {
		if chromedp.Run(ctx, chromedp.Location(&location)) != nil {
			return false
		}
		if acceptConsent(ctx) {
			return false
		}
		u, err := url.Parse(location)
		return err == nil && u.Host == forgejoHost && !strings.Contains(u.Path, "/login") && !strings.Contains(u.Path, "/oauth2")
	}).WithTimeout(30*time.Second).WithPolling(500*time.Millisecond).Should(BeTrue(), func() string {
		return fmt.Sprintf("%s did not reach Forgejo; last at %s", account, location)
	})

	browserStep(ctx, "woodpecker", account, chromedp.Navigate("https://"+ciHost+"/authorize"))
	var u *url.URL
	var text string
	Eventually(ctx, func() bool {
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
	}).WithTimeout(30*time.Second).WithPolling(500*time.Millisecond).Should(BeTrue(), func() string {
		return fmt.Sprintf("%s did not reach Woodpecker; last at %s", account, location)
	})
	// Woodpecker's web UI renders after load
	sleep(ctx, 2*time.Second)
	_ = chromedp.Run(ctx, chromedp.Evaluate(`document.body ? document.body.innerText.slice(0, 500) : ""`, &text))
	var cookies []*http.Cookie
	Expect(chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		got, err := network.GetCookies().WithURLs([]string{"https://" + ciHost + "/"}).Do(ctx)
		for _, c := range got {
			cookies = append(cookies, &http.Cookie{Name: c.Name, Value: c.Value})
		}
		return err
	}))).To(Succeed())
	return u, text, cookies
}

// woodpeckerRequest calls Woodpecker's API with a personal token or, without one, the session cookies and their
// CSRF token, and returns the status and body
func woodpeckerRequest(ctx context.Context, method, path, token string, cookies []*http.Cookie, body any) (int, []byte) {
	GinkgoHelper()
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		Expect(err).NotTo(HaveOccurred())
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://"+ciHost+path, reader)
	Expect(err).NotTo(HaveOccurred())
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
			req.Header.Set("X-CSRF-TOKEN", woodpeckerCSRF(ctx, cookies))
		}
	}
	resp, err := insecureClient.Do(req)
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())
	return resp.StatusCode, raw
}

// woodpeckerCSRF is the session's CSRF token, which Woodpecker's web UI reads from /web-config.js
func woodpeckerCSRF(ctx context.Context, cookies []*http.Cookie) string {
	GinkgoHelper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+ciHost+"/web-config.js", nil)
	Expect(err).NotTo(HaveOccurred())
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := insecureClient.Do(req)
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())
	m := regexp.MustCompile(`WOODPECKER_CSRF = "([^"]+)"`).FindSubmatch(raw)
	Expect(m).NotTo(BeNil(), "no CSRF token for this session")
	return string(m[1])
}

// woodpeckerToken signs account in and returns a personal token of theirs
func woodpeckerToken(ctx context.Context, account string) string {
	GinkgoHelper()
	u, text, cookies := woodpeckerLogin(ctx, account)
	Expect(u.Path).NotTo(Equal("/login"), "%s was not signed in (%s):\n%s", account, u.Redacted(), text)
	status, raw := woodpeckerRequest(ctx, http.MethodPost, "/api/user/token", "", cookies, nil)
	Expect(status).To(Equal(http.StatusOK), "%s's personal token", account)
	token := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	Expect(token).NotTo(BeEmpty())
	return token
}

// forgejoAs calls Forgejo's API as the site admin acting as user (Forgejo's Sudo header; "" acts as the admin)
func forgejoAs(ctx context.Context, user, method, path string, body, out any) int {
	GinkgoHelper()
	admin, password := secretValue(ctx, "forgejo", "forgejo-admin", "username"), secretValue(ctx, "forgejo", "forgejo-admin", "password")
	headers := []string{}
	if user != "" {
		headers = append(headers, "Sudo", user)
	}
	return getJSON(ctx, method, "https://"+forgejoHost+"/api/v1"+path, nil, admin, password, body, out, headers...)
}

// teamMembers lists the members of org ci's team
func teamMembers(ctx context.Context, team string) []string {
	GinkgoHelper()
	var teams []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	Expect(forgejoAs(ctx, "", "GET", "/orgs/ci/teams", nil, &teams)).To(Equal(http.StatusOK))
	for _, tm := range teams {
		if tm.Name == team {
			var members []struct {
				Login string `json:"login"`
			}
			Expect(forgejoAs(ctx, "", "GET", fmt.Sprintf("/teams/%d/members", tm.ID), nil, &members)).To(Equal(http.StatusOK))
			var names []string
			for _, m := range members {
				names = append(names, m.Login)
			}
			return names
		}
	}
	Fail("no team: org ci has no team " + team)
	return nil
}

var woodpeckerProbePipeline = `when:
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

// Woodpecker: docs/superpowers/specs/2026-10-21-woodpecker-design.md (Tests, sandbox)
var _ = Describe("Woodpecker", Label("sandbox", "testbed", "woodpecker"), func() {
	waitForIdentity()

	It("runs a probe pipeline confined", func(ctx SpecContext) {
		waitForApp(ctx, "https://"+forgejoHost+"/api/healthz")
		lab(ctx, "infra", "forgejo", "sync", "--env", labEnv)
		waitForApp(ctx, "https://"+ciHost+"/healthz")

		// Sign-in: only org ci's members (Kanidm homelab-admins → Owners, homelab-editors → developers)
		u, text, cookies := woodpeckerLogin(ctx, "dev-viewer")
		Expect(u.Path).To(Equal("/login"))
		Expect(u.Query().Get("error")).To(Equal("org_access_denied"), text)
		status, _ := woodpeckerRequest(ctx, http.MethodGet, "/api/user", "", cookies, nil)
		Expect(status).To(Equal(http.StatusUnauthorized), "dev-viewer has a Woodpecker session")
		editor, admin := woodpeckerToken(ctx, "dev-editor"), woodpeckerToken(ctx, "dev-admin")
		Expect(teamMembers(ctx, "developers")).To(ContainElement("dev-editor"))
		Expect(teamMembers(ctx, "developers")).NotTo(ContainElement("dev-viewer"))
		Expect(teamMembers(ctx, "Owners")).To(ContainElement("dev-admin"))

		// The probe repository, created by dev-editor in org ci and activated in Woodpecker
		name := fmt.Sprintf("woodpecker-probe-%d", time.Now().Unix())
		var repo struct {
			ID int64 `json:"id"`
		}
		Expect(forgejoAs(ctx, "dev-editor", "POST", "/orgs/ci/repos",
			map[string]any{"name": name, "private": true, "auto_init": true, "default_branch": "master"}, &repo)).To(Equal(http.StatusCreated))
		DeferCleanup(func(ctx SpecContext) { forgejoAs(ctx, "", "DELETE", "/repos/ci/"+name, nil, nil) }, NodeTimeout(time.Minute))
		var active struct {
			ID    int64 `json:"id"`
			OrgID int64 `json:"org_id"`
		}
		status, raw := woodpeckerRequest(ctx, http.MethodPost, fmt.Sprintf("/api/repos?forge_remote_id=%d", repo.ID), editor, nil, nil)
		Expect(status).To(Equal(http.StatusOK), string(raw))
		Expect(json.Unmarshal(raw, &active)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) {
			woodpeckerRequest(ctx, http.MethodDelete, fmt.Sprintf("/api/repos/%d", active.ID), editor, nil, nil)
		}, NodeTimeout(time.Minute))

		// The registry's credentials as org secrets, set by an owner (Woodpecker knows org ci once one of its repositories is active; its lookup by name answers 404)
		var org struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}
		status, raw = woodpeckerRequest(ctx, http.MethodGet, fmt.Sprintf("/api/orgs/%d", active.OrgID), admin, nil, nil)
		Expect(status).To(Equal(http.StatusOK), string(raw))
		Expect(json.Unmarshal(raw, &org)).To(Succeed())
		Expect(org.Name).To(Equal("ci"))
		for name, value := range map[string]string{"registry_username": "ci", "registry_password": baoValue(ctx, "zot/generated/ci", "password")} {
			path := fmt.Sprintf("/api/orgs/%d/secrets", org.ID)
			woodpeckerRequest(ctx, http.MethodDelete, path+"/"+name, admin, nil, nil)
			status, _ := woodpeckerRequest(ctx, http.MethodPost, path, admin, nil, map[string]any{"name": name, "value": value, "events": []string{"push"}})
			Expect(status).To(Equal(http.StatusOK), "org secret %s", name)
			DeferCleanup(func(ctx SpecContext) { woodpeckerRequest(ctx, http.MethodDelete, path+"/"+name, admin, nil, nil) }, NodeTimeout(time.Minute))
		}
		status, _ = woodpeckerRequest(ctx, http.MethodPost, fmt.Sprintf("/api/orgs/%d/secrets", org.ID), editor, nil,
			map[string]any{"name": "editor_probe", "value": "x", "events": []string{"push"}})
		Expect(status).To(Equal(http.StatusForbidden), "a developer set an org secret")

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
		Expect(forgejoAs(ctx, "dev-editor", "POST", "/repos/ci/"+name+"/contents",
			map[string]any{"files": files, "message": "probe", "branch": "master"}, &commit)).To(Equal(http.StatusCreated))
		Expect(commit.Commit.SHA).NotTo(BeEmpty())

		// Its step pods, inspected while they run: no privilege, a user namespace, not the agent's service account
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
		Eventually(ctx, func() bool {
			if out, err := kube.Kubectl(ctx, "woodpecker", "get", "pods", "-o",
				`jsonpath={range .items[?(@.metadata.name!="woodpecker-server-0")]}{.metadata.name}|{.spec.hostUsers}|{.spec.serviceAccountName}|{.spec.containers[0].image}|{.spec.securityContext.seccompProfile.type}/{.spec.securityContext.seccompProfile.localhostProfile}|{.spec.containers[*].securityContext.privileged}{"\n"}{end}`); err == nil {
				for _, line := range strings.Split(out, "\n") {
					if name, rest, ok := strings.Cut(line, "|"); ok && strings.HasPrefix(name, "wp-") {
						seen[name] = rest
					}
				}
			}
			var list []struct {
				Number int64  `json:"number"`
				Commit string `json:"commit"`
			}
			status, raw := woodpeckerRequest(ctx, http.MethodGet, fmt.Sprintf("/api/repos/%d/pipelines", active.ID), editor, nil, nil)
			if status != http.StatusOK || json.Unmarshal(raw, &list) != nil {
				return false
			}
			for _, p := range list {
				if p.Commit != commit.Commit.SHA {
					continue
				}
				status, raw := woodpeckerRequest(ctx, http.MethodGet, fmt.Sprintf("/api/repos/%d/pipelines/%d", active.ID, p.Number), editor, nil, nil)
				if status != http.StatusOK || json.Unmarshal(raw, &pipeline) != nil {
					return false
				}
				switch pipeline.Status {
				case "success", "failure", "error", "killed", "declined", "blocked":
					return true
				}
			}
			return false
		}).WithTimeout(10*time.Minute).WithPolling(2*time.Second).Should(BeTrue(), "the probe's pipeline did not finish")
		if pipeline.Status != "success" {
			for _, w := range pipeline.Workflows {
				for _, step := range w.Children {
					_, raw := woodpeckerRequest(ctx, http.MethodGet, fmt.Sprintf("/api/repos/%d/logs/%d/%d", active.ID, pipeline.Number, step.ID), editor, nil, nil)
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
					GinkgoWriter.Printf("step %s (%s):\n%s\n", step.Name, step.State, log.String())
				}
			}
			Fail("pipeline failed: the probe's pipeline ended " + pipeline.Status)
		}
		Expect(seen).NotTo(BeEmpty(), "no step pod was seen")
		GinkgoWriter.Printf("step pods: %v\n", seen)
		buildah := 0
		for pod, spec := range seen {
			f := strings.Split(spec, "|")
			Expect(f).To(HaveLen(5), "%s: %q", pod, spec)
			Expect(f[0]).To(Equal("false"), "%s: hostUsers", pod)
			Expect(f[1]).NotTo(Equal("woodpecker-agent"), "%s runs as the agent's service account", pod)
			Expect(strings.Fields(f[4])).NotTo(ContainElement("true"), "%s has a privileged container", pod)
			// Buildah's steps alone get its Localhost profile (platform/woodpecker/templates/seccomp-policy.yaml); the
			// others name none, and the kubelet's seccomp-default gives them RuntimeDefault (WoodpeckerSeccomp)
			if strings.HasPrefix(f[2], "quay.io/buildah/stable:") {
				buildah++
				Expect(f[3]).To(Equal("Localhost/homelab/buildah.json"), "%s: Buildah's step has no profile of its own", pod)
			} else {
				Expect(f[3]).To(Equal("/"), "%s (%s) names a seccomp profile", pod, f[2])
			}
		}
		Expect(buildah).To(BeNumerically(">", 0), "no Buildah step pod was seen")

		// zot serves the image
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://"+registryHost+"/v2/"+name+"/manifests/"+commit.Commit.SHA, nil)
		Expect(err).NotTo(HaveOccurred())
		req.SetBasicAuth("ci", baoValue(ctx, "zot/generated/ci", "password"))
		req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
		resp, err := insecureClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusOK), "zot does not serve the probe's image")
		digest := resp.Header.Get("Docker-Content-Digest")
		Expect(digest).NotTo(BeEmpty())

		// The probe image removed again: zot's ci cannot delete, an admin's API key can
		cookies = appLogin(ctx, "dev-admin", ensureCredentials(ctx, "dev-admin"), "https://"+registryHost+"/zot/auth/login?provider=oidc&callback_ui=/", registryHost)
		var key struct {
			APIKey string `json:"apiKey"`
			UUID   string `json:"uuid"`
		}
		Expect(getJSON(ctx, "POST", "https://"+registryHost+"/zot/auth/apikey", cookies, "", "",
			map[string]any{"label": fmt.Sprintf("woodpecker-test-%d", time.Now().UnixNano()), "scopes": []string{}}, &key,
			"X-ZOT-API-CLIENT", "zot-ui")).To(Equal(http.StatusCreated))
		DeferCleanup(func(ctx SpecContext) {
			getJSON(ctx, "DELETE", "https://"+registryHost+"/zot/auth/apikey?id="+key.UUID, cookies, "", "", nil, nil, "X-ZOT-API-CLIENT", "zot-ui")
		}, NodeTimeout(time.Minute))
		Expect(getJSON(ctx, "DELETE", "https://"+registryHost+"/v2/"+name+"/manifests/"+digest, nil,
			"dev-admin", key.APIKey, nil, nil)).To(Equal(http.StatusAccepted), "the probe's image was not deleted")
	}, SpecTimeout(65*time.Minute))
})
