// Package identity: Kanidm logins for the tests (TOTP codes, PKCE pairs)
package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"
)

// Credentials of a sandbox test account, stored in a Secret so reruns can log in again
type Credentials struct {
	Password   string `json:"password"`
	TOTPSecret string `json:"totpSecret"` // base32 without padding
	Algorithm  string `json:"algorithm"`  // sha1, sha256 or sha512
	Digits     int    `json:"digits"`
	Period     uint   `json:"period"`
}

// TOTPCode is the TOTP code for c at a time
func TOTPCode(t *testing.T, c Credentials, at time.Time) string {
	t.Helper()
	algorithms := map[string]otp.Algorithm{"sha1": otp.AlgorithmSHA1, "sha256": otp.AlgorithmSHA256, "sha512": otp.AlgorithmSHA512}
	algorithm, ok := algorithms[c.Algorithm]
	require.True(t, ok, "unknown TOTP algorithm %q", c.Algorithm)
	digits := otp.DigitsSix
	if c.Digits == 8 {
		digits = otp.DigitsEight
	}
	code, err := totp.GenerateCodeCustom(c.TOTPSecret, at, totp.ValidateOpts{Period: c.Period, Digits: digits, Algorithm: algorithm})
	require.NoError(t, err)
	return code
}

// TOTPClock hands out codes from a step not used before, because Kanidm rejects a reused step.
type TOTPClock struct {
	Now   func() time.Time
	Sleep func(time.Duration)
	Last  map[string]uint64
}

func (k *TOTPClock) Code(t *testing.T, account string, c Credentials) string {
	t.Helper()
	for {
		now := k.Now()
		step := uint64(now.Unix()) / uint64(c.Period)
		if step > k.Last[account] {
			k.Last[account] = step
			return TOTPCode(t, c, now)
		}
		k.Sleep(time.Duration(uint64(c.Period)-uint64(now.Unix())%uint64(c.Period)) * time.Second)
	}
}

// PKCEPair returns a random PKCE verifier and its S256 challenge; Kanidm requires PKCE for every client.
func PKCEPair() (string, string) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}
