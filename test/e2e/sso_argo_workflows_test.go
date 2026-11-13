package e2e

import (
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Argo Workflows' UI (step 10f, platform/argo-workflows): people sign in with Kanidm; admins manage the golden path's
// templates, editors and users only read them; nobody else gets in
var _ = Describe("SSOArgoWorkflows", Label("sandbox", "sso", "ci"), func() {
	base := "https://" + ciHost
	start := base + "/oauth2/redirect"
	waitForIdentity()
	waitForAppBeforeEach(base + "/api/v1/version")

	for account, canDelete := range map[string]bool{"dev-admin": true, "dev-editor": false, "dev-viewer": false} {
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
			// Deleting a template that does not exist: the role decides between 404 and 403, nothing is changed
			status, _ := getJSONE(ctx, "DELETE", base+"/api/v1/workflow-templates/ci/no-such-template", cookies, "", "", nil, nil)
			if canDelete {
				Expect(status).To(Equal(http.StatusNotFound))
			} else {
				Expect(status).To(Equal(http.StatusForbidden))
			}
		}, SpecTimeout(15*time.Minute))
	}

	It("dev-outsider", func(ctx SpecContext) {
		Expect(browserDenied(ctx, "dev-outsider", ensureCredentials(ctx, "dev-outsider"), start)).To(ContainSubstring("Access Denied"))
	}, SpecTimeout(15*time.Minute))
})
