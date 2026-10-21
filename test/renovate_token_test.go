package test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fakeInstallationToken = "ghs_faketoken0123456789"

// fakeGitHub is GitHub's installation token API: it checks the JWT against the App's public key and records the
// permissions asked for and the tokens revoked
type fakeGitHub struct {
	t           *testing.T
	key         *rsa.PublicKey
	status      int
	failFirst   []int // statuses answered before status, one per attempt
	attempts    int
	permissions map[string]string
	revoked     []string
	jwt         string
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/app/installations/173635875/access_tokens":
		f.attempts++
		if f.attempts <= len(f.failFirst) {
			w.WriteHeader(f.failFirst[f.attempts-1])
			_, _ = io.WriteString(w, `{"message":"try again"}`)
			return
		}
		f.jwt = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		parts := strings.Split(f.jwt, ".")
		if !assert.Len(f.t, parts, 3) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		signature, err := base64.RawURLEncoding.DecodeString(parts[2])
		require.NoError(f.t, err)
		digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if !assert.NoError(f.t, rsa.VerifyPKCS1v15(f.key, crypto.SHA256, digest[:], signature), "the JWT is not signed with the App's key") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var header map[string]string
		raw, _ := base64.RawURLEncoding.DecodeString(parts[0])
		require.NoError(f.t, json.Unmarshal(raw, &header))
		assert.Equal(f.t, "RS256", header["alg"])
		var claims struct {
			Iss string `json:"iss"`
			Iat int64  `json:"iat"`
			Exp int64  `json:"exp"`
		}
		raw, _ = base64.RawURLEncoding.DecodeString(parts[1])
		require.NoError(f.t, json.Unmarshal(raw, &claims))
		assert.Equal(f.t, "5406416", claims.Iss)
		assert.LessOrEqual(f.t, claims.Exp-claims.Iat, int64(600), "GitHub refuses a JWT that lives more than 10 minutes")
		var body struct {
			Permissions map[string]string `json:"permissions"`
		}
		require.NoError(f.t, json.NewDecoder(r.Body).Decode(&body))
		f.permissions = body.Permissions
		w.WriteHeader(f.status)
		if f.status == http.StatusCreated {
			_, _ = io.WriteString(w, `{"token":"`+fakeInstallationToken+`","expires_at":"2026-10-21T10:00:00Z"}`)
		} else {
			_, _ = io.WriteString(w, `{"message":"The permissions requested are not granted to this installation."}`)
		}
	case r.Method == http.MethodDelete && r.URL.Path == "/installation/token":
		f.revoked = append(f.revoked, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		w.WriteHeader(http.StatusNoContent)
	default:
		f.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

// runTokenScript runs platform/renovate's token script as the CronJob does: the App's IDs and permissions from the
// rendered values, GitHub's API the fake
func runTokenScript(t *testing.T, api, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("node", append([]string{"../platform/renovate/files/github-token.js"}, args...)...)
	cmd.Env = append(os.Environ(),
		"GITHUB_API="+api,
		"KEY_FILE="+filepath.Join(dir, "private-key"),
		"TOKEN_FILE="+filepath.Join(dir, "token"),
		"RETRY_DELAYS=10,10,10", // milliseconds: the script's own are 10, 30 and 90 seconds
	)
	for _, name := range []string{"APP_ID", "INSTALLATION_ID", "PERMISSIONS"} {
		cmd.Env = append(cmd.Env, name+"="+renderRenovate(t,
			`select(.kind == "CronJob") | .spec.jobTemplate.spec.template.spec.initContainers[] | select(.name == "github-token") | .env[] | select(.name == "`+name+`") | .value`))
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// The initContainer mints a one-hour installation token with exactly Renovate's permissions from a JWT signed with
// the App's key, and the Renovate container revokes it when the run ends; neither prints the key, the JWT or the
// token, and a refusal fails with GitHub's status
func TestRenovateTokenScript(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "private-key"),
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600))
	keyPEM, err := os.ReadFile(filepath.Join(dir, "private-key"))
	require.NoError(t, err)

	t.Run("mint and revoke", func(t *testing.T) {
		github := &fakeGitHub{t: t, key: &key.PublicKey, status: http.StatusCreated}
		server := httptest.NewServer(github)
		defer server.Close()

		out, err := runTokenScript(t, server.URL, dir, "mint")
		require.NoError(t, err, out)
		assert.Equal(t, map[string]string{"checks": "read", "contents": "write", "issues": "write",
			"pull_requests": "write", "statuses": "read", "workflows": "write"}, github.permissions)
		token, err := os.ReadFile(filepath.Join(dir, "token"))
		require.NoError(t, err)
		assert.Equal(t, fakeInstallationToken, string(token))
		info, err := os.Stat(filepath.Join(dir, "token"))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

		revokeOut, err := runTokenScript(t, server.URL, dir, "revoke")
		require.NoError(t, err, revokeOut)
		assert.Equal(t, []string{fakeInstallationToken}, github.revoked)

		for _, secret := range []string{fakeInstallationToken, github.jwt, strings.TrimSpace(string(keyPEM))} {
			assert.NotContains(t, out+revokeOut, secret)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(keyPEM)), "\n")[1:3] {
			assert.NotContains(t, out+revokeOut, line)
		}
	})

	// GitHub's or the network's passing trouble is retried, three times at most
	t.Run("retried", func(t *testing.T) {
		require.NoError(t, os.Remove(filepath.Join(dir, "token")))
		github := &fakeGitHub{t: t, key: &key.PublicKey, status: http.StatusCreated,
			failFirst: []int{http.StatusServiceUnavailable, http.StatusTooManyRequests}}
		server := httptest.NewServer(github)
		defer server.Close()
		out, err := runTokenScript(t, server.URL, dir, "mint")
		require.NoError(t, err, out)
		assert.Equal(t, 3, github.attempts)
		assert.FileExists(t, filepath.Join(dir, "token"))
	})

	t.Run("still failing", func(t *testing.T) {
		require.NoError(t, os.Remove(filepath.Join(dir, "token")))
		github := &fakeGitHub{t: t, key: &key.PublicKey, status: http.StatusCreated,
			failFirst: []int{http.StatusBadGateway, http.StatusBadGateway, http.StatusBadGateway, http.StatusBadGateway}}
		server := httptest.NewServer(github)
		defer server.Close()
		out, err := runTokenScript(t, server.URL, dir, "mint")
		require.Error(t, err, out)
		assert.Equal(t, 4, github.attempts, "one attempt and three retries")
		assert.Contains(t, out, "502")
		assert.NoFileExists(t, filepath.Join(dir, "token"))
	})

	// A wrong key, App or permission does not get better: no retry
	t.Run("refused", func(t *testing.T) {
		github := &fakeGitHub{t: t, key: &key.PublicKey, status: http.StatusUnprocessableEntity}
		server := httptest.NewServer(github)
		defer server.Close()
		out, err := runTokenScript(t, server.URL, dir, "mint")
		require.Error(t, err, out)
		assert.Contains(t, out, "422")
		assert.Contains(t, out, "not granted")
		assert.Equal(t, 1, github.attempts)
		assert.NoFileExists(t, filepath.Join(dir, "token"))
	})

	t.Run("GitHub unreachable", func(t *testing.T) {
		server := httptest.NewServer(http.NotFoundHandler())
		server.Close()
		out, err := runTokenScript(t, server.URL, dir, "mint")
		require.Error(t, err, out)
		assert.NoFileExists(t, filepath.Join(dir, "token"))
	})
}
