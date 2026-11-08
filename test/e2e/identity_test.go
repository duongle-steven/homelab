package e2e

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// waitForIdentity is the identity and SSO containers' BeforeEach: a fresh sandbox syncs Kanidm and the smoke client
// late, and these specs run before the smoke specs that would otherwise wait for them
func waitForIdentity() {
	BeforeEach(func(ctx SpecContext) { waitForIdentityStack(ctx) }, NodeTimeout(25*time.Minute))
}

var _ = Describe("IdentityLogin", Label("sandbox", "testbed", "identity"), func() {
	waitForIdentity()

	for _, account := range testAccounts {
		if account.group == "" {
			continue
		}
		It(account.name, func(ctx SpecContext) {
			secret := smokeClientSecret(ctx)
			c := ensureCredentials(ctx, account.name)

			code, page, verifier := browserAuthorize(ctx, account.name, c, c.Password)
			Expect(code).NotTo(BeEmpty(), "no authorization code for %s; page:\n%s", account.name, page)

			claims := exchangeCode(ctx, code, secret, verifier)
			GinkgoWriter.Printf("homelab_groups claim for %s: %v\n", account.name, claims["homelab_groups"])
			Expect(claims["email"]).To(Equal(account.name + "@example.com"))
			Expect(claims["homelab_groups"]).To(Equal([]any{account.group}))
			Expect(claims["preferred_username"]).To(Equal(account.name))
		}, SpecTimeout(15*time.Minute))
	}
})

var _ = Describe("IdentityDenied", Label("sandbox", "testbed", "identity"), func() {
	waitForIdentity()

	It("refuses an account in no homelab group after its login", func(ctx SpecContext) {
		c := ensureCredentials(ctx, "dev-outsider")

		code, page, _ := browserAuthorize(ctx, "dev-outsider", c, c.Password)

		// Refused after a successful login, not because the login itself failed
		Expect(code).To(BeEmpty(), "an account in no homelab group must not get an authorization code")
		Expect(page).To(ContainSubstring("Access Denied"))
		Expect(page).NotTo(ContainSubstring("Login Failed"))
	}, SpecTimeout(15*time.Minute))
})

// IdentityDeniedNeedsLogin guards IdentityDenied: a failed login must not look like a refusal.
var _ = Describe("IdentityDeniedNeedsLogin", Label("sandbox", "testbed", "identity"), func() {
	waitForIdentity()

	It("does not show a failed login as a refusal", func(ctx SpecContext) {
		c := ensureCredentials(ctx, "dev-outsider")

		code, page, _ := browserAuthorize(ctx, "dev-outsider", c, "wrong-"+c.Password)

		Expect(code).To(BeEmpty())
		Expect(page).NotTo(ContainSubstring("Access Denied"))
	}, SpecTimeout(15*time.Minute))
})
