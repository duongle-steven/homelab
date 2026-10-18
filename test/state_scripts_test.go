package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeBin writes executable scripts into a temporary directory placed first on PATH.
func fakeBin(t *testing.T, scripts map[string]string) (dir string, env []string) {
	dir = t.TempDir()
	for name, body := range scripts {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755))
	}
	return dir, append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "FIXTURES="+dir)
}

func runScript(t *testing.T, env []string, script string, args ...string) (string, error) {
	cmd := exec.Command("sh", append([]string{"../scripts/" + script}, args...)...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func TestStateCloudflareCredentials(t *testing.T) {
	dir, env := fakeBin(t, map[string]string{
		"cf": `[ -z "$CLOUDFLARE_API_TOKEN" ] || { echo "token leaked into cf" >&2; exit 3; }
[ -f "$FIXTURES/cf-fails" ] && { echo "not logged in" >&2; exit 1; }
[ -f "$FIXTURES/cf-logged-out" ] && { echo '{"accounts":[]}'; exit 0; }
[ -f "$FIXTURES/cf-no-accounts" ] && { printf '{"authSource":"OAuth token from %s/token.json","accounts":[]}\n' "$FIXTURES"; exit 0; }
printf '{"authSource":"OAuth token from %s/token.json","accounts":[{"id":"acc-1"},{"id":"acc-2"}]}\n' "$FIXTURES"`,
	})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "token.json"), []byte(`{"oauth_token":"tok-123"}`), 0o600))
	env = append(env, "CLOUDFLARE_API_TOKEN=old-token")

	out, err := runScript(t, env, "cloudflare-credentials", "sandbox")
	require.NoError(t, err)
	assert.JSONEq(t, `{"account_id":"","token":""}`, out, "the sandbox never calls cf")

	out, err = runScript(t, env, "cloudflare-credentials", "production")
	require.NoError(t, err)
	assert.JSONEq(t, `{"account_id":"acc-1","token":"tok-123"}`, out)

	out, err = runScript(t, append(env, "CLOUDFLARE_ACCOUNT_ID=acc-2"), "cloudflare-credentials", "production")
	require.NoError(t, err)
	assert.JSONEq(t, `{"account_id":"acc-2","token":"tok-123"}`, out)

	out, err = runScript(t, append(env, "HOMELAB_CLOUDFLARE_OFFLINE=1"), "cloudflare-credentials", "production")
	require.NoError(t, err)
	assert.JSONEq(t, `{"account_id":"offline","token":""}`, out, "make -C infra check never calls cf")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "cf-no-accounts"), nil, 0o644))
	out, err = runScript(t, env, "cloudflare-credentials", "production")
	assert.Error(t, err, "an account id of null: %s", out)
	assert.Contains(t, out, "no account")
	require.NoError(t, os.Remove(filepath.Join(dir, "cf-no-accounts")))

	require.NoError(t, os.WriteFile(filepath.Join(dir, "cf-logged-out"), nil, 0o644))
	out, err = runScript(t, env, "cloudflare-credentials", "production")
	assert.Error(t, err)
	assert.Contains(t, out, "cf auth login")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "cf-fails"), nil, 0o644))
	out, err = runScript(t, env, "cloudflare-credentials", "production")
	assert.Error(t, err)
	assert.Contains(t, out, "not logged in")
}

func TestStateBucket(t *testing.T) {
	for _, c := range []struct {
		name, getCode, postCode string
		created, fails          bool
	}{
		{"missing bucket created", "404", "200", true, false},
		{"existing bucket kept", "200", "", false, false},
		{"API error", "403", "", false, true},
		{"create fails", "404", "409", true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, env := fakeBin(t, map[string]string{
				"cloudflare-credentials": `echo '{"account_id":"acc-1","token":"tok-123"}'`,
				// Records each call; -o writes the body, -w prints the status code
				"curl": `echo "$*" >> "$FIXTURES/calls"
out=; while [ $# -gt 0 ]; do case "$1" in -o) out=$2; shift;; esac; shift; done
case "$(tail -1 "$FIXTURES/calls")" in
  *"-X POST"*) code=$(cat "$FIXTURES/post");;
  *) code=$(cat "$FIXTURES/get");;
esac
echo '{"errors":[{"message":"fake '"$code"'"}]}' > "$out"; printf %s "$code"`,
			})
			require.NoError(t, os.WriteFile(filepath.Join(dir, "get"), []byte(c.getCode), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "post"), []byte(c.postCode), 0o644))
			out, err := runScript(t, append(env, "CLOUDFLARE_CREDENTIALS="+filepath.Join(dir, "cloudflare-credentials")), "ensure-state-bucket", "homelab-production-tfstate")
			calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
			assert.Contains(t, string(calls), "accounts/acc-1/r2/buckets/homelab-production-tfstate")
			assert.NotContains(t, string(calls), "tok-123", "the token must not be on curl's command line")
			assert.Equal(t, c.created, strings.Contains(string(calls), "-X POST"))
			if c.fails {
				assert.Error(t, err)
				assert.Contains(t, out, "fake")
			} else {
				assert.NoError(t, err, out)
			}
		})
	}
}
