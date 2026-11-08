package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	grafanaHost   = "grafana." + suiteDomain
	argocdHost    = "argocd." + suiteDomain
	registryHost  = "registry." + suiteDomain
	budgetHost    = "budget." + suiteDomain
	paperlessHost = "paperless." + suiteDomain
	forgejoHost   = "git." + suiteDomain
	opencloudHost = "drive." + suiteDomain
	ciHost        = "ci." + suiteDomain
)

// waitForAppBeforeEach is a container's BeforeEach waiting for its app (waitForApp)
func waitForAppBeforeEach(target string) {
	BeforeEach(func(ctx SpecContext) { waitForApp(ctx, target) }, NodeTimeout(25*time.Minute))
}

// waitForApp blocks until the app answers on its sandbox host.
func waitForApp(ctx context.Context, target string) {
	Eventually(ctx, func() bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return false
		}
		resp, err := insecureClient.Do(req)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode < 500 && resp.StatusCode != http.StatusNotFound
	}).WithTimeout(20*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "%s is not ready", target)
}

// secretValue reads one key of a Secret without logging it.
func secretValue(ctx context.Context, namespace, name, key string) string {
	var secret struct {
		Data map[string][]byte `json:"data"`
	}
	Expect(kube.GetJSON(ctx, namespace, &secret, "secret", name)).To(Succeed())
	return string(secret.Data[key])
}

func configMapValue(ctx context.Context, namespace, name, key string) string {
	var configMap struct {
		Data map[string]string `json:"data"`
	}
	Expect(kube.GetJSON(ctx, namespace, &configMap, "configmap", name)).To(Succeed())
	return configMap.Data[key]
}

var _ = Describe("SSOGrafana", Label("sandbox", "testbed", "sso", "grafana"), func() {
	base := "https://" + grafanaHost
	waitForIdentity()
	waitForAppBeforeEach(base + "/api/health")

	expected := map[string]struct {
		role         string
		grafanaAdmin bool
	}{
		"dev-admin":  {"Admin", true},
		"dev-editor": {"Editor", false},
		"dev-viewer": {"Viewer", false},
	}
	for _, account := range slices.Sorted(maps.Keys(expected)) {
		want := expected[account]
		It(account, func(ctx SpecContext) {
			cookies := appLogin(ctx, account, ensureCredentials(ctx, account), base+"/login/generic_oauth", grafanaHost)

			var user struct {
				Login          string `json:"login"`
				IsGrafanaAdmin bool   `json:"isGrafanaAdmin"`
			}
			Expect(getJSON(ctx, "GET", base+"/api/user", cookies, "", "", nil, &user)).To(Equal(http.StatusOK))
			Expect(user.Login).To(Equal(account))
			Expect(user.IsGrafanaAdmin).To(Equal(want.grafanaAdmin))

			var orgs []struct {
				Role string `json:"role"`
			}
			Expect(getJSON(ctx, "GET", base+"/api/user/orgs", cookies, "", "", nil, &orgs)).To(Equal(http.StatusOK))
			Expect(orgs).To(HaveLen(1))
			Expect(orgs[0].Role).To(Equal(want.role))
		}, SpecTimeout(15*time.Minute))
	}

	It("local-admin", func(ctx SpecContext) {
		password := secretValue(ctx, "grafana", "grafana-admin", "admin-password")
		Expect(getJSON(ctx, "GET", base+"/api/user", nil, "admin", password, nil, nil)).To(Equal(http.StatusOK))
	}, SpecTimeout(5*time.Minute))
})

var _ = Describe("SSOArgoCD", Label("sandbox", "testbed", "sso", "argocd"), func() {
	base := "https://" + argocdHost
	waitForIdentity()
	waitForAppBeforeEach(base + "/api/version")

	type check struct{ resource, action, object string }
	sync := check{"applications", "sync", "default/excalidraw"}
	del := check{"applications", "delete", "default/excalidraw"}
	exec := check{"exec", "create", "default/excalidraw"}
	// Exec into platform pods (the controller's service account is cluster-admin) must stay admin-only
	execSystem := check{"exec", "create", "default/argocd"}
	expected := map[string]map[check]string{
		"dev-admin":  {sync: "yes", del: "yes", exec: "yes", execSystem: "yes"},
		"dev-editor": {sync: "yes", del: "no", exec: "yes", execSystem: "no"},
		"dev-viewer": {sync: "no", del: "no", exec: "no", execSystem: "no"},
	}
	for _, account := range slices.Sorted(maps.Keys(expected)) {
		checks := expected[account]
		It(account, func(ctx SpecContext) {
			cookies := appLogin(ctx, account, ensureCredentials(ctx, account), base+"/auth/login", argocdHost)
			for c, want := range checks {
				var answer struct {
					Value string `json:"value"`
				}
				Expect(getJSON(ctx, "GET", base+"/api/v1/account/can-i/"+c.resource+"/"+c.action+"/"+c.object, cookies, "", "", nil, &answer)).To(Equal(http.StatusOK))
				Expect(answer.Value).To(Equal(want), "%s %s %s", c.resource, c.action, c.object)
			}
		}, SpecTimeout(15*time.Minute))
	}

	It("local-admin", func(ctx SpecContext) {
		password := secretValue(ctx, "argocd", "argocd-initial-admin-secret", "password")
		var session struct {
			Token string `json:"token"`
		}
		Expect(getJSON(ctx, "POST", base+"/api/v1/session", nil, "", "", map[string]string{"username": "admin", "password": password}, &session)).To(Equal(http.StatusOK))
		Expect(session.Token).NotTo(BeEmpty())
	}, SpecTimeout(5*time.Minute))
})

var _ = Describe("SSOZot", Label("sandbox", "testbed", "sso", "zot"), func() {
	base := "https://" + registryHost
	upload := base + "/v2/sso-test/blobs/uploads/"
	waitForIdentity()
	waitForAppBeforeEach(base + "/livez")

	expectedUpload := map[string]int{
		"dev-admin":  http.StatusAccepted,
		"dev-editor": http.StatusAccepted,
		"dev-viewer": http.StatusForbidden,
	}
	for _, account := range slices.Sorted(maps.Keys(expectedUpload)) {
		want := expectedUpload[account]
		It(account, func(ctx SpecContext) {
			cookies := appLogin(ctx, account, ensureCredentials(ctx, account), base+"/zot/auth/login?provider=oidc&callback_ui=/", registryHost)

			var key struct {
				APIKey string `json:"apiKey"`
				UUID   string `json:"uuid"`
			}
			label := fmt.Sprintf("sso-test-%d", time.Now().UnixNano())
			Expect(getJSON(ctx, "POST", base+"/zot/auth/apikey", cookies, "", "", map[string]any{"label": label, "scopes": []string{}}, &key,
				// zot only accepts its session cookie from its UI client
				"X-ZOT-API-CLIENT", "zot-ui")).To(Equal(http.StatusCreated))
			Expect(key.APIKey).NotTo(BeEmpty())
			// Each run creates a key: remove it so they do not pile up in zot
			DeferCleanup(func(ctx SpecContext) {
				Expect(getJSON(ctx, "DELETE", base+"/zot/auth/apikey?id="+key.UUID, cookies, "", "", nil, nil,
					"X-ZOT-API-CLIENT", "zot-ui")).To(Equal(http.StatusOK))
			}, NodeTimeout(time.Minute))

			Expect(getJSON(ctx, "POST", upload, nil, account, key.APIKey, nil, nil)).To(Equal(want))
			Expect(getJSON(ctx, "GET", base+"/v2/_catalog", nil, account, key.APIKey, nil, nil)).To(Equal(http.StatusOK))
		}, SpecTimeout(15*time.Minute))
	}

	It("ci", func(ctx SpecContext) {
		password := generatedValue(ctx, "zot/generated/ci", "password")
		Expect(getJSON(ctx, "POST", upload, nil, "ci", password, nil, nil)).To(Equal(http.StatusAccepted))
	}, SpecTimeout(25*time.Minute))

	It("anonymous", func(ctx SpecContext) {
		Expect(getJSON(ctx, "GET", base+"/v2/_catalog", nil, "", "", nil, nil)).To(Equal(http.StatusUnauthorized))
	}, SpecTimeout(5*time.Minute))
})

// Ordered: dev-admin first, the first OpenID user becomes the server owner
var _ = Describe("SSOActualBudget", Ordered, Label("sandbox", "testbed", "sso", "actual-budget"), func() {
	base := "https://" + budgetHost
	waitForIdentity()
	waitForAppBeforeEach(base + "/account/login-methods")

	for _, c := range []struct {
		account string
		allowed bool
	}{{"dev-admin", true}, {"dev-viewer", true}, {"dev-editor", false}} {
		It(c.account, func(ctx SpecContext) {
			var start struct {
				Data struct {
					ReturnURL string `json:"returnUrl"`
				} `json:"data"`
			}
			Expect(getJSON(ctx, "POST", base+"/account/login", nil, "", "",
				map[string]any{"loginMethod": "openid", "returnUrl": base}, &start)).To(Equal(http.StatusOK))
			Expect(start.Data.ReturnURL).NotTo(BeEmpty())
			token, page := actualOpenIDLogin(ctx, c.account, ensureCredentials(ctx, c.account), start.Data.ReturnURL, base)
			if !c.allowed {
				Expect(token).To(BeEmpty())
				Expect(page).To(ContainSubstring("Access Denied"))
				return
			}
			Expect(token).NotTo(BeEmpty(), "no Actual token for %s; page:\n%s", c.account, page)
			var me struct {
				Data struct {
					UserName string `json:"userName"`
				} `json:"data"`
			}
			Expect(getJSON(ctx, "GET", base+"/account/validate", nil, "", "", nil, &me, "X-ACTUAL-TOKEN", token)).To(Equal(http.StatusOK))
			Expect(me.Data.UserName).To(Equal(c.account))
		}, SpecTimeout(15*time.Minute))
	}
})

var _ = Describe("SSOPaperless", Label("sandbox", "testbed", "sso", "paperless"), func() {
	base := "https://" + paperlessHost
	start := base + "/accounts/oidc/kanidm/login/?process=login"
	waitForIdentity()
	BeforeEach(func(ctx SpecContext) {
		skipUnlessDeployed(ctx, "paperless")
		waitForApp(ctx, base+"/accounts/login/")
	}, NodeTimeout(25*time.Minute))

	for _, c := range []struct {
		account   string
		allowed   bool
		superuser bool
	}{{"dev-admin", true, true}, {"dev-editor", true, false}, {"dev-viewer", false, false}} {
		It(c.account, func(ctx SpecContext) {
			if !c.allowed {
				Expect(browserDenied(ctx, c.account, ensureCredentials(ctx, c.account), start)).To(ContainSubstring("Access Denied"))
				return
			}
			cookies := appLogin(ctx, c.account, ensureCredentials(ctx, c.account), start, paperlessHost)
			var settings struct {
				User struct {
					Username    string `json:"username"`
					IsSuperuser bool   `json:"is_superuser"`
				} `json:"user"`
			}
			Expect(getJSON(ctx, "GET", base+"/api/ui_settings/", cookies, "", "", nil, &settings)).To(Equal(http.StatusOK))
			Expect(settings.User.Username).To(Equal(c.account))
			Expect(settings.User.IsSuperuser).To(Equal(c.superuser))
			// Regular users get document permissions from the paperless group homelab-documents
			Expect(getJSON(ctx, "GET", base+"/api/documents/", cookies, "", "", nil, nil)).To(Equal(http.StatusOK))
		}, SpecTimeout(15*time.Minute))
	}
})

var _ = Describe("SSOForgejo", Label("sandbox", "testbed", "sso", "forgejo"), func() {
	base := "https://" + forgejoHost
	start := base + "/user/oauth2/Kanidm"
	waitForIdentity()
	waitForAppBeforeEach(base + "/api/healthz")

	for _, c := range []struct {
		account string
		allowed bool
		admin   bool
	}{{"dev-admin", true, true}, {"dev-editor", true, false}, {"dev-viewer", true, false}, {"dev-outsider", false, false}} {
		It(c.account, func(ctx SpecContext) {
			if !c.allowed {
				Expect(browserDenied(ctx, c.account, ensureCredentials(ctx, c.account), start)).To(ContainSubstring("Access Denied"))
				return
			}
			// Twice: the second login must reuse the account and keep the admin flag
			for range 2 {
				cookies := appLogin(ctx, c.account, ensureCredentials(ctx, c.account), start, forgejoHost)
				// The web pages, not the API: Forgejo's API does not accept the session cookie
				code, path, body := forgejoPage(ctx, base+"/user/settings", cookies)
				Expect(code).To(Equal(http.StatusOK))
				Expect(path).To(Equal("/user/settings"), "the session is not signed in")
				Expect(body).To(ContainSubstring(`value="` + c.account + `"`))
				code, _, _ = forgejoPage(ctx, base+"/admin", cookies)
				Expect(code).To(Equal(map[bool]int{true: http.StatusOK, false: http.StatusForbidden}[c.admin]))
			}
		}, SpecTimeout(20*time.Minute))
	}
})

// forgejoPage gets a Forgejo web page with the session cookies and returns the status, the final path
// (after redirects, for example to /user/login) and the body.
func forgejoPage(ctx context.Context, target string, cookies []*http.Cookie) (int, string, string) {
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	Expect(err).NotTo(HaveOccurred())
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := insecureClient.Do(req)
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())
	return resp.StatusCode, resp.Request.URL.Path, string(body)
}

var _ = Describe("SSOOpenCloud", Label("sandbox", "testbed", "sso", "opencloud"), func() {
	base := "https://" + opencloudHost
	waitForIdentity()
	BeforeEach(func(ctx SpecContext) {
		skipUnlessDeployed(ctx, "opencloud")
		waitForApp(ctx, base+"/")
	}, NodeTimeout(25*time.Minute))

	for _, c := range []struct {
		account string
		role    string // OpenCloud role; empty: Kanidm refuses the account (not in homelab-files)
	}{{"dev-admin", "Admin"}, {"dev-editor", "User"}, {"dev-viewer", ""}, {"dev-outsider", ""}} {
		It(c.account, func(ctx SpecContext) {
			if c.role == "" {
				Expect(browserDenied(ctx, c.account, ensureCredentials(ctx, c.account), openCloudAuthorizeURL(base))).To(ContainSubstring("Access Denied"))
				return
			}
			// Twice: the second login must reuse the account (keyed by Kanidm's sub) and keep the role
			var ids []string
			var token string
			for range 2 {
				token = openCloudLogin(ctx, c.account, ensureCredentials(ctx, c.account), base)
				auth := []string{"Authorization", "Bearer " + token}
				var me struct {
					ID                 string `json:"id"`
					DisplayName        string `json:"displayName"`
					AppRoleAssignments []struct {
						AppRoleID string `json:"appRoleId"`
					} `json:"appRoleAssignments"`
				}
				// A first login provisions the account while the browser's own requests race it (OpenCloud may create it
				// twice and answer 500 until it settles), as a person reloading would see
				graph := func(path string, out any) {
					Eventually(ctx, func() bool {
						status, err := getJSONE(ctx, "GET", base+path, nil, "", "", nil, out, auth...)
						return err == nil && status == http.StatusOK
					}).WithTimeout(time.Minute).WithPolling(2*time.Second).Should(BeTrue(), "%s did not answer 200 for %s", path, c.account)
				}
				graph("/graph/v1.0/me?$expand=appRoleAssignments", &me)
				Expect(me.DisplayName).To(Equal(c.account))
				ids = append(ids, me.ID)

				var apps struct {
					Value []struct {
						AppRoles []struct {
							ID          string `json:"id"`
							DisplayName string `json:"displayName"`
						} `json:"appRoles"`
					} `json:"value"`
				}
				graph("/graph/v1.0/applications", &apps)
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
				// OpenCloud's first login can race and store the same role twice; a wrong, mixed, unknown or missing
				// role still fails
				if len(roles) > 1 {
					GinkgoWriter.Printf("%s has %d role assignments %v (OpenCloud's first-login race stores duplicates)\n", c.account, len(roles), roles)
				}
				slices.Sort(roles)
				roles = slices.Compact(roles)
				Expect(roles).To(Equal([]string{c.role}))
			}
			Expect(ids[1]).To(Equal(ids[0]), "the second login created another account")

			// Files: a WebDAV round trip in the personal drive (with the last login's token), which OpenCloud
			// creates on first login
			var drive struct {
				Root struct {
					WebDavURL string `json:"webDavUrl"`
				} `json:"root"`
			}
			// Not getJSON: transport errors must retry
			Eventually(ctx, func() bool {
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/graph/v1.0/me/drive", nil)
				if err != nil {
					return false
				}
				req.Header.Set("Authorization", "Bearer "+token)
				resp, err := insecureClient.Do(req)
				if err != nil {
					return false
				}
				defer resp.Body.Close()
				return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&drive) == nil && drive.Root.WebDavURL != ""
			}).WithTimeout(time.Minute).WithPolling(2*time.Second).Should(BeTrue(), "no personal drive for %s", c.account)
			file := fmt.Sprintf("%s/homelab-test-%d.txt", strings.TrimSuffix(drive.Root.WebDavURL, "/"), time.Now().UnixNano())
			content := fmt.Sprintf("homelab test %s %d", c.account, time.Now().UnixNano())
			code, _ := davRequest(ctx, http.MethodPut, file, token, content)
			Expect(code).To(Equal(http.StatusCreated))
			// A new file is processed after the upload (asynchronously): until then a read is 425 Too Early
			var got string
			Eventually(ctx, func() int {
				code, got = davRequest(ctx, http.MethodGet, file, token, "")
				return code
			}).WithTimeout(time.Minute).WithPolling(2 * time.Second).ShouldNot(Equal(http.StatusTooEarly))
			Expect(code).To(Equal(http.StatusOK))
			Expect(got).To(Equal(content))
			code, _ = davRequest(ctx, http.MethodDelete, file, token, "")
			Expect(code).To(Equal(http.StatusNoContent))
		}, SpecTimeout(25*time.Minute))
	}
})

// davRequest sends a WebDAV request with a bearer token and returns the status and the body.
func davRequest(ctx context.Context, method, target, token, body string) (int, string) {
	req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := insecureClient.Do(req)
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())
	return resp.StatusCode, string(raw)
}
