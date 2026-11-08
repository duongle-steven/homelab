package offline

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"

	"github.com/duongle-steven/homelab/test/internal/identity"
	"github.com/stretchr/testify/assert"
)

func TestIdentityTOTPWaitsForNextStep(t *testing.T) {
	c := identity.Credentials{TOTPSecret: "JBSWY3DPEHPK3PXP", Algorithm: "sha256", Digits: 6, Period: 30}
	t0 := time.Unix(1_800_000_000, 0) // start of a 30 s step
	now := t0
	var slept time.Duration
	clock := &identity.TOTPClock{
		Now:   func() time.Time { return now },
		Sleep: func(d time.Duration) { slept += d; now = now.Add(d) },
		Last:  map[string]uint64{},
	}

	first := clock.Code(t, "dev-admin", c)
	second := clock.Code(t, "dev-admin", c)

	assert.Equal(t, 30*time.Second, slept, "second code must wait for the next step")
	assert.NotEqual(t, first, second)
	assert.Equal(t, identity.TOTPCode(t, c, t0.Add(30*time.Second)), second)
}

func TestIdentityPKCE(t *testing.T) {
	verifier, challenge := identity.PKCEPair()
	assert.Regexp(t, `^[A-Za-z0-9_-]{43,128}$`, verifier)
	sum := sha256.Sum256([]byte(verifier))
	assert.Equal(t, base64.RawURLEncoding.EncodeToString(sum[:]), challenge)
	other, _ := identity.PKCEPair()
	assert.NotEqual(t, verifier, other)
}
