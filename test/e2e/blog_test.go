package e2e

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The blog's app and repository name (apps/blog.yaml, Backstage's template hugo-site)
const blogApp = "blog"

var blogHost = "www." + suiteDomain

// blogPage is what www answers through the Gateway: its status and body
func blogPage(ctx context.Context) (int, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+blogHost+"/", nil)
	if err != nil {
		return 0, ""
	}
	resp, err := insecureClient.Do(req)
	if err != nil {
		return 0, ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// blogRelease is ops/releases' blog.yaml: what Kargo promoted
func blogRelease(ctx context.Context) string {
	var file struct {
		Content string `json:"content"`
	}
	if forgejoAs(ctx, "", "GET", "/repos/ops/releases/contents/"+blogApp+".yaml?ref=main", nil, &file) != http.StatusOK {
		return ""
	}
	raw, _ := base64.StdEncoding.DecodeString(file.Content)
	return string(raw)
}

// The blog end to end (docs/superpowers/specs/2026-11-21-blog-design.md): "coming soon" until its repository exists,
// then Backstage's template hugo-site creates ci/blog, the CI builds and signs it, Kargo verifies and promotes it by
// itself, and Argo Rollouts switches www to it blue-green once its preview passed static-site-responds
var _ = Describe("Blog", Label("sandbox", "blog"), Ordered, ContinueOnFailure, func() {
	waitForIdentity()
	backstageReady()
	var site string
	// The repository is removed once every spec ran; the release file stays, as in production
	AfterAll(func(ctx SpecContext) { forgejoAs(ctx, "", "DELETE", "/repos/ci/"+blogApp, nil, nil) }, NodeTimeout(time.Minute))

	It("shows the coming soon page before its repository exists", func(ctx SpecContext) {
		if blogRelease(ctx) != "" {
			Skip("an earlier run already promoted the blog: ops/releases/blog.yaml exists")
		}
		Eventually(ctx, func() string {
			_, body := blogPage(ctx)
			return body
		}).WithTimeout(10 * time.Minute).WithPolling(15 * time.Second).Should(ContainSubstring("Coming soon"))
	}, SpecTimeout(15*time.Minute))

	It("creates ci/blog from the template and releases it", func(ctx SpecContext) {
		backstageReadsForgejo(ctx)
		// Left by an earlier run: the template refuses a repository that exists, and a location already registered
		forgejoAs(ctx, "", "DELETE", "/repos/ci/"+blogApp, nil, nil)
		admin, _, _ := backstageIdentity(ctx, "dev-admin")
		backstageUnregister(ctx, admin, blogApp)
		backstageCreateRepository(ctx, "hugo-site", blogApp, "The blog's spec")
		// The repository's entity beside the app's component:blog, without a conflict that would leave it out
		Eventually(ctx, func() int {
			return backstageAPI(ctx, admin, http.MethodGet, "/api/catalog/entities/by-name/component/default/"+blogApp+"-site", nil, nil)
		}).WithTimeout(5*time.Minute).WithPolling(10*time.Second).Should(Equal(http.StatusOK), "Backstage did not ingest ci/blog's entity")
		var branch struct {
			Commit struct {
				ID string `json:"id"`
			} `json:"commit"`
		}
		Eventually(ctx, func() int {
			return forgejoAs(ctx, "", "GET", "/repos/ci/"+blogApp+"/branches/main", nil, &branch)
		}).WithTimeout(5 * time.Minute).WithPolling(10 * time.Second).Should(Equal(http.StatusOK))
		site = branch.Commit.ID
		_, digest := ciWorkflowFor(ctx, blogApp, site, "push")
		Expect(digest).NotTo(BeEmpty(), "the CI pushed no image")
		Eventually(ctx, blogRelease).WithTimeout(20*time.Minute).WithPolling(20*time.Second).Should(ContainSubstring(site),
			"Kargo did not write the CI's tag to ops/releases")
	}, SpecTimeout(45*time.Minute))

	It("serves the site on www after the blue-green switch", func(ctx SpecContext) {
		Eventually(ctx, func() string {
			out, _ := kube.Kubectl(ctx, blogApp, "get", "rollouts.argoproj.io", blogApp, "-o", "jsonpath={.status.phase}")
			return strings.TrimSpace(out)
		}).WithTimeout(15*time.Minute).WithPolling(15*time.Second).Should(Equal("Healthy"), "the Rollout did not settle on the site")
		Eventually(ctx, func() string {
			_, body := blogPage(ctx)
			return body
		}).WithTimeout(5*time.Minute).WithPolling(10*time.Second).Should(And(ContainSubstring("Your name"), Not(ContainSubstring("Coming soon"))),
			"www does not serve the template's profile page")
		// The preview check ran and passed before the switch
		out, err := kube.Kubectl(ctx, blogApp, "get", "analysisruns.argoproj.io", "-o", `jsonpath={range .items[*]}{.spec.metrics[0].name} {.status.phase}{"\n"}{end}`)
		Expect(err).NotTo(HaveOccurred(), out)
		Expect(out).To(ContainSubstring("assets Successful"))
	}, SpecTimeout(25*time.Minute))
})
