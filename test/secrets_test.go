package test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func secretsSandboxOnly(t *testing.T) {
	if os.Getenv("ENV") != "dev" {
		t.Skip("OpenBao tests run against the sandbox only")
	}
}

// bao runs the OpenBao CLI in OpenBao's pod, logged in with a token of service account namespace/account (audience
// openbao, the only one OpenBao's roles accept) and the given role. The token reaches the pod on stdin and is never
// logged
func bao(t *testing.T, role, namespace, account string, args ...string) (string, error) {
	return baoAudience(t, "openbao", role, namespace, account, args...)
}

func baoAudience(t *testing.T, audience, role, namespace, account string, args ...string) (string, error) {
	options := k8s.NewKubectlOptions("", "", namespace)
	options.Logger = logger.Discard
	tokenArgs := []string{"create", "token", account, "--duration=10m"}
	if audience != "" {
		tokenArgs = append(tokenArgs, "--audience="+audience)
	}
	token, err := k8s.RunKubectlAndGetOutputE(t, options, tokenArgs...)
	require.NoError(t, err)
	script := `BAO_TOKEN="$(bao write -field=token auth/kubernetes/login role="$0" jwt=-)" && export BAO_TOKEN && exec bao "$@"`
	cmd := exec.Command("kubectl", append([]string{"-n", "openbao", "exec", "-i", "openbao-0", "-c", "openbao", "--",
		"sh", "-c", script, role}, args...)...)
	cmd.Stdin = strings.NewReader(token)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// waitForUnsealed waits until OpenBao's pod answers, initialised and unsealed
func waitForUnsealed(t *testing.T) {
	require.Eventually(t, func() bool {
		out, err := exec.Command("kubectl", "-n", "openbao", "exec", "openbao-0", "-c", "openbao", "--",
			"bao", "status", "-format=json").Output()
		if err != nil {
			return false
		}
		var status struct {
			Initialized bool `json:"initialized"`
			Sealed      bool `json:"sealed"`
		}
		return json.Unmarshal(out, &status) == nil && status.Initialized && !status.Sealed
	}, 30*time.Minute, 10*time.Second, "OpenBao is not initialised and unsealed")
}

// grafanaAdmin reads Grafana's admin password from its branch, as Grafana's namespace, once global-secrets has
// pushed it
func grafanaAdmin(t *testing.T) string {
	var password string
	require.Eventually(t, func() bool {
		out, err := bao(t, "apps", "grafana", "default", "kv", "get", "-field=password", "secret/grafana/generated/admin")
		password = out
		return err == nil && out != ""
	}, 20*time.Minute, 15*time.Second, "secret/grafana/generated/admin is not readable")
	return password
}

func TestSecretsUnsealed(t *testing.T) {
	secretsSandboxOnly(t)
	waitForUnsealed(t)
}

// A namespace reads its own branch, not another's, and writes nowhere
func TestSecretsNamespaceBranches(t *testing.T) {
	secretsSandboxOnly(t)
	waitForUnsealed(t)
	assert.NotEmpty(t, grafanaAdmin(t))

	out, err := bao(t, "apps", "grafana", "default", "kv", "get", "-field=password", "secret/zot/generated/ci")
	assert.Error(t, err, "grafana read zot's branch")
	assert.Contains(t, out, "permission denied")

	out, err = bao(t, "apps", "grafana", "default", "kv", "put", "secret/grafana/probe", "value=x")
	assert.Error(t, err, "grafana wrote to OpenBao")
	assert.Contains(t, out, "permission denied")
}

// admin runs the OpenBao CLI with the admin role and fails the test on an error
func admin(t *testing.T, args ...string) string {
	out, err := bao(t, "admin", "openbao", "openbao-admin", args...)
	require.NoError(t, err, out)
	return out
}

// The writer is global-secrets' default service account only, and reaches only secret/<namespace>/generated/: it reads
// and writes nothing else and deletes nothing
func TestSecretsWriterRole(t *testing.T) {
	secretsSandboxOnly(t)
	waitForUnsealed(t)
	out, err := bao(t, "writer", "grafana", "default", "kv", "get", "secret/grafana/generated/admin")
	assert.Error(t, err, "grafana logged in as writer")
	assert.Contains(t, out, "not authorized")

	grafanaAdmin(t) // pushed by now
	admin(t, "kv", "put", "secret/grafana/writer-probe", "value=x")
	defer admin(t, "kv", "metadata", "delete", "secret/grafana/writer-probe")
	for _, args := range [][]string{
		{"kv", "get", "secret/grafana/writer-probe"},
		{"kv", "put", "secret/grafana/writer-probe", "value=y"},
		{"kv", "put", "secret/grafana/other", "value=y"},
		{"kv", "delete", "secret/grafana/generated/admin"},
		{"kv", "metadata", "delete", "secret/grafana/generated/admin"},
	} {
		out, err := bao(t, "writer", "global-secrets", "default", args...)
		assert.Error(t, err, "writer: bao %v", args)
		assert.Contains(t, out, "permission denied", "writer: bao %v", args)
	}
	out, err = bao(t, "writer", "global-secrets", "default", "kv", "get", "-field=password", "secret/grafana/generated/admin")
	require.NoError(t, err, "the writer reads generated paths (External Secrets checks before it pushes)")
	assert.NotEmpty(t, out)
}

// OpenBao refuses tokens issued for the API server (the default audience): a token External Secrets sends cannot be
// replayed against the API server, and one stolen from a pod does not open OpenBao
func TestSecretsAudience(t *testing.T) {
	secretsSandboxOnly(t)
	waitForUnsealed(t)
	out, err := baoAudience(t, "", "apps", "grafana", "default", "kv", "get", "secret/grafana/generated/admin")
	assert.Error(t, err, "OpenBao accepted a token without audience openbao")
	assert.Contains(t, out, "invalid audience")
}

// Emergency access: the admin role, for service account openbao-admin
func TestSecretsAdmin(t *testing.T) {
	secretsSandboxOnly(t)
	waitForUnsealed(t)
	out := admin(t, "policy", "list")
	for _, policy := range []string{"apps", "writer", "admin"} {
		assert.Contains(t, strings.Fields(out), policy)
	}
}

// Grafana's admin Secret comes from OpenBao and equals the generated value
func TestSecretsReachConsumers(t *testing.T) {
	secretsSandboxOnly(t)
	waitForUnsealed(t)
	generated := secretValue(t, "global-secrets", "grafana.admin", "password")
	assert.Equal(t, generated, grafanaAdmin(t), "OpenBao's value differs from the generated one")
	require.Eventually(t, func() bool {
		return secretValue(t, "grafana", "grafana-admin", "admin-password") == generated
	}, 20*time.Minute, 15*time.Second, "grafana-admin does not hold the generated password")
}

// After its pod is replaced OpenBao unseals itself and keeps its data: a value nothing pushes again (written with the
// admin role) is still there. Consumers keep their Secrets meanwhile; External Secrets retries the reads that fail
// while the pod restarts
func TestSecretsSurviveRestart(t *testing.T) {
	secretsSandboxOnly(t)
	waitForUnsealed(t)
	sentinel := fmt.Sprint(time.Now().UnixNano())
	admin(t, "kv", "put", "secret/smoke/restart", "value="+sentinel)
	defer admin(t, "kv", "metadata", "delete", "secret/smoke/restart")
	k8s.RunKubectl(t, k8s.NewKubectlOptions("", "", "openbao"), "delete", "pod", "openbao-0", "--wait=true")
	waitForUnsealed(t)
	assert.Equal(t, sentinel, admin(t, "kv", "get", "-field=value", "secret/smoke/restart"))
}

// Role infra (OpenTofu's secrets, platform/infra-secrets) writes every branch except generated/, which it neither
// reads nor writes
func TestSecretsInfraRole(t *testing.T) {
	secretsSandboxOnly(t)
	waitForUnsealed(t)
	grafanaAdmin(t) // pushed by now
	defer admin(t, "kv", "metadata", "delete", "secret/grafana/infra-probe")
	out, err := bao(t, "infra", "infra-secrets", "openbao-infra", "kv", "put", "secret/grafana/infra-probe", "value=x")
	require.NoError(t, err, out)
	for _, args := range [][]string{
		{"kv", "get", "secret/grafana/generated/admin"},
		{"kv", "put", "secret/grafana/generated/admin", "password=x"},
		{"kv", "put", "secret/grafana/generated/new", "value=x"},
		{"kv", "metadata", "get", "secret/grafana/generated/admin"},
		{"kv", "metadata", "delete", "secret/grafana/infra-probe"},
	} {
		out, err := bao(t, "infra", "infra-secrets", "openbao-infra", args...)
		assert.Error(t, err, "infra: bao %v", args)
		assert.Contains(t, out, "permission denied", "infra: bao %v", args)
	}
	out, err = bao(t, "infra", "grafana", "default", "kv", "get", "secret/grafana/infra-probe")
	assert.Error(t, err, "grafana logged in as infra")
	assert.Contains(t, out, "not authorized")
}
