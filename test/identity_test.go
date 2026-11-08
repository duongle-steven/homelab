package test

import (
	"testing"

	"github.com/duongle-steven/homelab/test/internal/testenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIdentityLogin(t *testing.T) {
	testenv.LocalOnly(t)
	waitForIdentityStack(t)
	secret := smokeClientSecret(t)

	for _, account := range testAccounts {
		if account.group == "" {
			continue
		}
		t.Run(account.name, func(t *testing.T) {
			c := ensureCredentials(t, account.name)

			code, page, verifier := browserAuthorize(t, account.name, c, c.Password)
			require.NotEmpty(t, code, "no authorization code for %s; page:\n%s", account.name, page)

			claims := exchangeCode(t, code, secret, verifier)
			t.Logf("homelab_groups claim for %s: %v", account.name, claims["homelab_groups"])
			assert.Equal(t, account.name+"@example.com", claims["email"])
			assert.Equal(t, []any{account.group}, claims["homelab_groups"])
			assert.Equal(t, account.name, claims["preferred_username"])
		})
	}
}

func TestIdentityDenied(t *testing.T) {
	testenv.LocalOnly(t)
	waitForIdentityStack(t)
	c := ensureCredentials(t, "dev-outsider")

	code, page, _ := browserAuthorize(t, "dev-outsider", c, c.Password)

	// Refused after a successful login, not because the login itself failed
	assert.Empty(t, code, "an account in no homelab group must not get an authorization code")
	assert.Contains(t, page, "Access Denied")
	assert.NotContains(t, page, "Login Failed")
}

// TestIdentityDeniedNeedsLogin guards TestIdentityDenied: a failed login must not look like a refusal.
func TestIdentityDeniedNeedsLogin(t *testing.T) {
	testenv.LocalOnly(t)
	waitForIdentityStack(t)
	c := ensureCredentials(t, "dev-outsider")

	code, page, _ := browserAuthorize(t, "dev-outsider", c, "wrong-"+c.Password)

	assert.Empty(t, code)
	assert.NotContains(t, page, "Access Denied")
}
