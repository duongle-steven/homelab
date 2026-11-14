package e2e

import (
	"fmt"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Argo Workflows' UI (step 10f, platform/argo-workflows): people sign in with Kanidm; admins manage the golden path's
// templates, editors and users only read them; nobody else gets in (editors run Workflows: CI covers that path)
var _ = Describe("SSOArgoWorkflows", Label("sandbox", "sso", "ci"), func() {
	base := "https://" + ciHost
	start := base + "/oauth2/redirect"
	waitForIdentity()
	waitForAppBeforeEach(base + "/api/v1/version")

	for account, canWrite := range map[string]bool{"dev-admin": true, "dev-editor": false, "dev-viewer": false} {
		It(account, func(ctx SpecContext) {
			cookies := appLogin(ctx, account, ensureCredentials(ctx, account), start, ciHost)
			Expect(getJSON(ctx, "GET", base+"/api/v1/workflows/ci", cookies, "", "", nil, nil)).To(Equal(http.StatusOK))
			var template struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			}
			Expect(getJSON(ctx, "GET", base+"/api/v1/workflow-templates/ci/pipeline", cookies, "", "", nil, &template)).To(Equal(http.StatusOK))
			Expect(template.Metadata.Name).To(Equal("pipeline"))
			// Creating a template: only admins may (Argo Server acts as the person's ServiceAccount, whose Role decides)
			probe := map[string]any{"template": map[string]any{
				"metadata": map[string]any{"name": fmt.Sprint("sso-probe-", time.Now().UnixNano())},
				"spec": map[string]any{"entrypoint": "main", "templates": []any{map[string]any{"name": "main",
					"container": map[string]any{"image": "docker.io/library/busybox:1.37.0", "command": []string{"true"}}}}}}}
			var created struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			}
			status, _ := getJSONE(ctx, "POST", base+"/api/v1/workflow-templates/ci", cookies, "", "", probe, &created)
			if canWrite {
				Expect(status).To(Equal(http.StatusOK))
				Expect(created.Metadata.Name).NotTo(BeEmpty())
				// Argo Server reads templates from its informer's cache: right after the create it may answer 404
				Eventually(ctx, func() int {
					status, _ := getJSONE(ctx, "DELETE", base+"/api/v1/workflow-templates/ci/"+created.Metadata.Name, cookies, "", "", nil, nil)
					return status
				}).WithTimeout(time.Minute).WithPolling(2 * time.Second).Should(Equal(http.StatusOK))
			} else {
				Expect(status).To(Equal(http.StatusForbidden), "%s created a template", account)
			}
		}, SpecTimeout(15*time.Minute))
	}

	It("dev-outsider", func(ctx SpecContext) {
		Expect(browserDenied(ctx, "dev-outsider", ensureCredentials(ctx, "dev-outsider"), start)).To(ContainSubstring("Access Denied"))
	}, SpecTimeout(15*time.Minute))
})
