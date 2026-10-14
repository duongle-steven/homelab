package test

import (
	"crypto/sha256"
	"encoding/base64"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIdentityTOTPWaitsForNextStep(t *testing.T) {
	c := credentials{TOTPSecret: "JBSWY3DPEHPK3PXP", Algorithm: "sha256", Digits: 6, Period: 30}
	t0 := time.Unix(1_800_000_000, 0) // start of a 30 s step
	now := t0
	var slept time.Duration
	clock := &totpClock{
		now:   func() time.Time { return now },
		sleep: func(d time.Duration) { slept += d; now = now.Add(d) },
		last:  map[string]uint64{},
	}

	first := clock.code(t, "dev-admin", c)
	second := clock.code(t, "dev-admin", c)

	assert.Equal(t, 30*time.Second, slept, "second code must wait for the next step")
	assert.NotEqual(t, first, second)
	assert.Equal(t, totpCode(t, c, t0.Add(30*time.Second)), second)
}

func TestIdentityHasGroup(t *testing.T) {
	assert.True(t, hasGroup(map[string]any{"groups": []any{"homelab-admins"}}, "homelab-admins"))
	assert.True(t, hasGroup(map[string]any{"groups": []any{"homelab-admins@auth.127-0-0-1.nip.io"}}, "homelab-admins"))
	assert.False(t, hasGroup(map[string]any{"groups": []any{"homelab-admins-old"}}, "homelab-admins"))
	assert.False(t, hasGroup(map[string]any{}, "homelab-admins"))
}

func TestIdentityLogin(t *testing.T) {
	if os.Getenv("ENV") != "dev" {
		t.Skip("identity tests run against the sandbox only")
	}
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
	if os.Getenv("ENV") != "dev" {
		t.Skip("identity tests run against the sandbox only")
	}
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
	if os.Getenv("ENV") != "dev" {
		t.Skip("identity tests run against the sandbox only")
	}
	waitForIdentityStack(t)
	c := ensureCredentials(t, "dev-outsider")

	code, page, _ := browserAuthorize(t, "dev-outsider", c, "wrong-"+c.Password)

	assert.Empty(t, code)
	assert.NotContains(t, page, "Access Denied")
}

func TestIdentityPKCE(t *testing.T) {
	verifier, challenge := pkcePair()
	assert.Regexp(t, `^[A-Za-z0-9_-]{43,128}$`, verifier)
	sum := sha256.Sum256([]byte(verifier))
	assert.Equal(t, base64.RawURLEncoding.EncodeToString(sum[:]), challenge)
	other, _ := pkcePair()
	assert.NotEqual(t, verifier, other)
}
