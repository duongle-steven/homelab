package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Backstage-1 (docs/superpowers/specs/2026-11-16-backstage-design.md): an optional component in the sandbox
// (just dev --apps backstage); these specs skip when it is absent and fail when HOMELAB_SANDBOX_APPS names it

var backstageHost = "backstage." + suiteDomain

// backstageIdentity signs account in through Kanidm and returns Backstage's token and the identity it issued
// (auth/identity.ts): the OIDC flow sets the refresh cookie, which /refresh trades for a Backstage token
func backstageIdentity(ctx context.Context, account string) (string, string, []string) {
	GinkgoHelper()
	base := "https://" + backstageHost
	start := base + "/api/auth/oidc/start?env=production&scope=" + url.QueryEscape("openid profile email groups")
	cookies := appLogin(ctx, account, ensureCredentials(ctx, account), start, backstageHost)
	var out struct {
		BackstageIdentity struct {
			Token    string `json:"token"`
			Identity struct {
				UserEntityRef       string   `json:"userEntityRef"`
				OwnershipEntityRefs []string `json:"ownershipEntityRefs"`
			} `json:"identity"`
		} `json:"backstageIdentity"`
	}
	Expect(getJSON(ctx, http.MethodGet, base+"/api/auth/oidc/refresh?env=production", cookies, "", "", nil, &out,
		"X-Requested-With", "XMLHttpRequest")).To(Equal(http.StatusOK))
	id := out.BackstageIdentity
	return id.Token, id.Identity.UserEntityRef, id.Identity.OwnershipEntityRefs
}

// backstageAPI calls Backstage's API with a user's token
func backstageAPI(ctx context.Context, token, method, path string, body, out any) int {
	GinkgoHelper()
	status, err := getJSONE(ctx, method, "https://"+backstageHost+path, nil, "", "", body, out, "Authorization", "Bearer "+token)
	Expect(err).NotTo(HaveOccurred())
	return status
}

func backstageReady() {
	BeforeEach(func(ctx SpecContext) {
		skipUnlessDeployed(ctx, "backstage")
		waitForApp(ctx, "https://"+backstageHost+"/.backstage/health/v1/readiness")
	}, NodeTimeout(25*time.Minute))
}

// Only homelab-admins and homelab-editors sign in: Kanidm's scopeMap refuses the rest; the identity comes from the
// token's name and groups (no catalog User)
var _ = Describe("BackstageSignIn", Label("sandbox", "backstage", "sso"), func() {
	waitForIdentity()
	backstageReady()
	for _, c := range []struct{ account, group string }{
		{"dev-admin", "homelab-admins"}, {"dev-editor", "homelab-editors"}, {"dev-viewer", ""}, {"dev-outsider", ""},
	} {
		It(c.account, func(ctx SpecContext) {
			if c.group == "" {
				start := "https://" + backstageHost + "/api/auth/oidc/start?env=production"
				Expect(browserDenied(ctx, c.account, ensureCredentials(ctx, c.account), start)).To(ContainSubstring("Access Denied"))
				return
			}
			token, user, owners := backstageIdentity(ctx, c.account)
			Expect(token).NotTo(BeEmpty())
			Expect(user).To(Equal("user:default/" + c.account))
			Expect(owners).To(ConsistOf("user:default/"+c.account, "group:default/"+c.group))
		}, SpecTimeout(15*time.Minute))
	}
})

// The catalog is what lab catalog generate wrote, read from the repository Argo CD syncs; editors may not add to it
var _ = Describe("BackstageCatalog", Label("sandbox", "backstage"), func() {
	waitForIdentity()
	backstageReady()
	It("lists the homelab, owned by its groups", func(ctx SpecContext) {
		token, _, _ := backstageIdentity(ctx, "dev-admin")
		var forgejo struct {
			Metadata struct {
				Links []struct{ URL string } `json:"links"`
			} `json:"metadata"`
			Spec struct{ Owner, System string } `json:"spec"`
		}
		Eventually(ctx, func() int {
			return backstageAPI(ctx, token, http.MethodGet, "/api/catalog/entities/by-name/component/default/forgejo", nil, &forgejo)
		}).WithTimeout(10 * time.Minute).WithPolling(20 * time.Second).Should(Equal(http.StatusOK))
		Expect(forgejo.Spec.Owner).To(Equal("group:default/homelab-admins"))
		Expect(forgejo.Spec.System).To(Equal("homelab-platform"))
		// homelab/host becomes the page's link on the environment's domain (catalog/hostLinks.ts)
		Expect(forgejo.Metadata.Links).To(ContainElement(HaveField("URL", "https://git."+suiteDomain)))
		Expect(backstageAPI(ctx, token, http.MethodGet, "/api/catalog/entities/by-name/group/default/homelab-editors", nil, nil)).
			To(Equal(http.StatusOK))

		editor, _, _ := backstageIdentity(ctx, "dev-editor")
		Expect(backstageAPI(ctx, editor, http.MethodPost, "/api/catalog/locations",
			map[string]string{"type": "url", "target": "https://example.invalid/catalog-info.yaml"}, nil)).To(Equal(http.StatusForbidden))
	}, SpecTimeout(25*time.Minute))
})

// TechDocs builds this repository's MkDocs site from the Domain homelab (catalog-info.yaml's dir:.)
var _ = Describe("BackstageTechDocs", Label("sandbox", "backstage"), func() {
	waitForIdentity()
	backstageReady()
	It("renders the homelab's docs", func(ctx SpecContext) {
		token, _, _ := backstageIdentity(ctx, "dev-editor")
		Eventually(ctx, func() bool {
			// The sync builds on first view; the page is served once it is built
			backstageAPI(ctx, token, http.MethodGet, "/api/techdocs/sync/default/domain/homelab", nil, nil)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet,
				"https://"+backstageHost+"/api/techdocs/static/docs/default/domain/homelab/index.html", nil)
			if err != nil {
				return false
			}
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := insecureClient.Do(req)
			if err != nil {
				return false
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			return err == nil && resp.StatusCode == http.StatusOK && strings.Contains(string(body), "<html")
		}).WithTimeout(15*time.Minute).WithPolling(30*time.Second).Should(BeTrue(), "TechDocs never served the homelab's docs")
	}, SpecTimeout(25*time.Minute))
})

// The template creates a repository in org ci, which the golden path builds, and registers it; an editor may not run
// it. Backstage reads its Forgejo token at start: lab infra forgejo sync writes it, then Backstage restarts on it
var _ = Describe("BackstageTemplate", Label("sandbox", "backstage", "ci"), func() {
	waitForIdentity()
	backstageReady()
	It("creates a repository the golden path builds", func(ctx SpecContext) {
		lab(ctx, "infra", "forgejo", "sync", "--env", labEnv)
		_, err := kube.Kubectl(ctx, "backstage", "annotate", "externalsecret", "backstage-forgejo", "force-sync="+fmt.Sprint(time.Now().Unix()), "--overwrite")
		Expect(err).NotTo(HaveOccurred())
		Eventually(ctx, func() error {
			_, err := kube.Kubectl(ctx, "backstage", "get", "secret", "backstage-forgejo")
			return err
		}).WithTimeout(2 * time.Minute).Should(Succeed())
		_, err = kube.Kubectl(ctx, "backstage", "rollout", "restart", "deployment/backstage")
		Expect(err).NotTo(HaveOccurred())
		_, err = kube.Kubectl(ctx, "backstage", "rollout", "status", "deployment/backstage", "--timeout=10m")
		Expect(err).NotTo(HaveOccurred())
		waitForApp(ctx, "https://"+backstageHost+"/.backstage/health/v1/readiness")

		name := fmt.Sprint("bs-test-", time.Now().Unix())
		DeferCleanup(func(ctx SpecContext) { forgejoAs(ctx, "", "DELETE", "/repos/ci/"+name, nil, nil) }, NodeTimeout(time.Minute))
		task := map[string]any{"templateRef": "template:default/ci-repository", "values": map[string]string{"name": name, "description": "Backstage's spec"}}

		editor, _, _ := backstageIdentity(ctx, "dev-editor")
		Expect(backstageAPI(ctx, editor, http.MethodPost, "/api/scaffolder/v2/tasks", task, nil)).To(Equal(http.StatusForbidden))

		admin, _, _ := backstageIdentity(ctx, "dev-admin")
		var created struct{ ID string }
		Eventually(ctx, func() int {
			return backstageAPI(ctx, admin, http.MethodPost, "/api/scaffolder/v2/tasks", task, &created)
		}).WithTimeout(5*time.Minute).WithPolling(20*time.Second).Should(Equal(http.StatusCreated), "the template is not in the catalog yet")
		var status struct{ Status string }
		Eventually(ctx, func() string {
			backstageAPI(ctx, admin, http.MethodGet, "/api/scaffolder/v2/tasks/"+created.ID, nil, &status)
			return status.Status
		}).WithTimeout(5 * time.Minute).WithPolling(10 * time.Second).Should(Equal("completed"))

		var branch struct {
			Commit struct{ ID string } `json:"commit"`
		}
		Expect(forgejoAs(ctx, "", "GET", "/repos/ci/"+name+"/branches/main", nil, &branch)).To(Equal(http.StatusOK))
		phase, _ := ciWorkflowFor(ctx, name, branch.Commit.ID, "push")
		Expect(phase).To(Equal("Succeeded"))
		Eventually(ctx, func() int {
			return backstageAPI(ctx, admin, http.MethodGet, "/api/catalog/entities/by-name/component/default/"+name, nil, nil)
		}).WithTimeout(5 * time.Minute).WithPolling(15 * time.Second).Should(Equal(http.StatusOK))
	}, SpecTimeout(45*time.Minute))
})

// A component's Kubernetes and Argo CD views read the cluster and Argo CD, read-only
var _ = Describe("BackstagePlugins", Label("sandbox", "backstage"), func() {
	waitForIdentity()
	backstageReady()
	It("shows Backstage's own workloads and Application", func(ctx SpecContext) {
		token, _, _ := backstageIdentity(ctx, "dev-editor")
		var workloads struct {
			Items []struct {
				Resources []struct {
					Type      string           `json:"type"`
					Resources []map[string]any `json:"resources"`
				} `json:"resources"`
			} `json:"items"`
		}
		Eventually(ctx, func() string {
			backstageAPI(ctx, token, http.MethodPost, "/api/kubernetes/resources/workloads/query",
				map[string]any{"auth": map[string]any{}, "entityRef": "component:default/backstage"}, &workloads)
			return fmt.Sprint(workloads)
		}).WithTimeout(5 * time.Minute).WithPolling(20 * time.Second).Should(ContainSubstring("backstage"))

		var app map[string]any
		Eventually(ctx, func() int {
			return backstageAPI(ctx, token, http.MethodGet, "/api/argocd/find/name/backstage", nil, &app)
		}).WithTimeout(5 * time.Minute).WithPolling(20 * time.Second).Should(Equal(http.StatusOK))
		Expect(fmt.Sprint(app)).To(ContainSubstring("Synced"))
	}, SpecTimeout(20*time.Minute))
})

// Backstage's spans reach Tempo: a request through the Gateway with a trace id leaves the backend's span in that trace
var _ = Describe("BackstageTraces", Label("sandbox", "backstage", "traces"), func() {
	backstageReady()
	It("traces a request into the backend", func(ctx SpecContext) {
		password := secretValue(ctx, "grafana", "grafana-admin", "admin-password")
		traceID := randomHex(16)
		traceparent := fmt.Sprintf("00-%s-%s-01", traceID, randomHex(8))
		Eventually(ctx, func() bool {
			if _, err := getJSONE(ctx, http.MethodGet, "https://"+backstageHost+"/.backstage/health/v1/readiness", nil, "", "", nil, nil,
				"traceparent", traceparent); err != nil {
				return false
			}
			var trace map[string]any
			status, err := getJSONE(ctx, http.MethodGet, "https://"+grafanaHost+"/api/datasources/proxy/uid/tempo/api/v2/traces/"+traceID,
				nil, "admin", password, nil, &trace)
			return err == nil && status == http.StatusOK && strings.Contains(fmt.Sprint(trace), "backstage")
		}).WithTimeout(10*time.Minute).WithPolling(20*time.Second).Should(BeTrue(), "no span of Backstage's in trace %s", traceID)
	}, SpecTimeout(20*time.Minute))
})

// After a node restart PostgreSQL may answer later than Backstage starts: Backstage exits on a failed start and comes
// back once the database does (it used to stay up, never retrying)
var _ = Describe("BackstageRecovers", Label("sandbox", "backstage"), func() {
	backstageReady()
	It("comes back after its database and itself restart together", func(ctx SpecContext) {
		_, err := kube.Kubectl(ctx, "backstage", "delete", "pod", "backstage-db-1", "--wait=false")
		Expect(err).NotTo(HaveOccurred())
		_, err = kube.Kubectl(ctx, "backstage", "delete", "pod", "-l", "app.kubernetes.io/name=backstage", "--wait=false")
		Expect(err).NotTo(HaveOccurred())
		waitForApp(ctx, "https://"+backstageHost+"/.backstage/health/v1/readiness")
	}, SpecTimeout(20*time.Minute))
})
