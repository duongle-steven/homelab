package test

import (
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	grafanaHost   = "grafana.127-0-0-1.nip.io"
	argocdHost    = "argocd.127-0-0-1.nip.io"
	registryHost  = "registry.127-0-0-1.nip.io"
	budgetHost    = "budget.127-0-0-1.nip.io"
	paperlessHost = "paperless.127-0-0-1.nip.io"
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
		return resp.StatusCode < 500 && resp.StatusCode != http.StatusNotFound
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

func TestSSOArgoCD(t *testing.T) {
	skipUnlessSandbox(t)
	base := "https://" + argocdHost
	waitForApp(t, base+"/api/version")

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
			cookies := appLogin(t, account, ensureCredentials(t, account), base+"/zot/auth/login?provider=oidc&callback_ui=/", registryHost)

			var key struct {
				APIKey string `json:"apiKey"`
			}
			label := fmt.Sprintf("sso-test-%d", time.Now().UnixNano())
			require.Equal(t, http.StatusCreated, getJSON(t, "POST", base+"/zot/auth/apikey", cookies, "", "", map[string]any{"label": label, "scopes": []string{}}, &key,
				// zot only accepts its session cookie from its UI client
				"X-ZOT-API-CLIENT", "zot-ui"))
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
			require.NotEmpty(t, start.Data.ReturnURL)
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
	start := base + "/accounts/oidc/kanidm/login/?process=login"

	for _, c := range []struct {
		account   string
		allowed   bool
		superuser bool
	}{{"dev-admin", true, true}, {"dev-editor", true, false}, {"dev-viewer", false, false}} {
		t.Run(c.account, func(t *testing.T) {
			if !c.allowed {
				assert.Contains(t, browserDenied(t, c.account, ensureCredentials(t, c.account), start), "Access Denied")
				return
			}
			cookies := appLogin(t, c.account, ensureCredentials(t, c.account), start, paperlessHost)
			var settings struct {
				User struct {
					Username    string `json:"username"`
					IsSuperuser bool   `json:"is_superuser"`
				} `json:"user"`
			}
			require.Equal(t, http.StatusOK, getJSON(t, "GET", base+"/api/ui_settings/", cookies, "", "", nil, &settings))
			assert.Equal(t, c.account, settings.User.Username)
			assert.Equal(t, c.superuser, settings.User.IsSuperuser)
			// Regular users get document permissions from the paperless group homelab-documents
			assert.Equal(t, http.StatusOK, getJSON(t, "GET", base+"/api/documents/", cookies, "", "", nil, nil))
		})
	}
}
