package e2e

import (
	"net/http"
	"net/url"
	"time"

	"github.com/duongle-steven/homelab/test/internal/identity"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var kargoHost = "kargo." + suiteDomain

// Kargo's REST API, which its UI calls with the bearer token
const kargoAPI = "/v1beta1/"

// kargoAuthorizeURL is the authorization request Kargo's UI sends to Kanidm (public client, PKCE): Kanidm refuses
// accounts outside homelab-admins and homelab-editors before redirecting back
func kargoAuthorizeURL(appBase string) string {
	_, challenge := identity.PKCEPair()
	return kanidmURL + "/ui/oauth2?" + url.Values{
		"client_id":             {"kargo"},
		"redirect_uri":          {appBase + "/login"},
		"response_type":         {"code"},
		"scope":                 {"openid profile email groups"},
		"state":                 {"denied-test"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}.Encode()
}

// Admins administer Kargo, editors only see it, nobody else signs in (docs/how-to-guides/app-delivery.md)
var _ = Describe("SSOKargo", Label("sandbox", "sso", "kargo"), func() {
	base := "https://" + kargoHost
	// Optional in the sandbox (just dev deploys it with the specs that need it): skip before waiting for it
	BeforeEach(func(ctx SpecContext) { skipUnlessDeployed(ctx, deliveryApp) })
	waitForIdentity()
	waitForAppBeforeEach(base + "/")

	for _, c := range []struct {
		account string
		// A delete only an admin may make: an admin's of a project that does not exist (404, nothing deleted), an
		// editor's of the sandbox's project delivery-test (403; Kargo answers 404 for a missing one before it checks the
		// caller); 0: refused at Kanidm
		project      string
		deleteStatus int
	}{{"dev-admin", "no-such-project", http.StatusNotFound}, {"dev-editor", deliveryApp, http.StatusForbidden}, {"dev-viewer", "", 0}, {"dev-outsider", "", 0}} {
		It(c.account, func(ctx SpecContext) {
			if c.deleteStatus == 0 {
				Expect(browserDenied(ctx, c.account, ensureCredentials(ctx, c.account), kargoAuthorizeURL(base))).To(ContainSubstring("Access Denied"))
				return
			}
			token := browserBearer(ctx, c.account, ensureCredentials(ctx, c.account), base, base+kargoAPI, "Kargo")
			auth := []string{"Authorization", "Bearer " + token}
			Expect(getJSON(ctx, "GET", base+kargoAPI+"projects", nil, "", "", nil, nil, auth...)).To(Equal(http.StatusOK))
			Expect(getJSON(ctx, "DELETE", base+kargoAPI+"projects/"+c.project, nil, "", "", nil, nil, auth...)).To(Equal(c.deleteStatus))
		}, SpecTimeout(10*time.Minute))
	}
})
