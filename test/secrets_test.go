package test

import (
	"encoding/json"
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

// bao runs the OpenBao CLI in OpenBao's pod, logged in with a token of service account namespace/account and the
// given role. The token reaches the pod on stdin and is never logged
func bao(t *testing.T, role, namespace, account string, args ...string) (string, error) {
	options := k8s.NewKubectlOptions("", "", namespace)
	options.Logger = logger.Discard
	token, err := k8s.RunKubectlAndGetOutputE(t, options, "create", "token", account, "--duration=10m")
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
		out, err := bao(t, "apps", "grafana", "default", "kv", "get", "-field=password", "secret/grafana/admin")
		password = out
		return err == nil && out != ""
	}, 20*time.Minute, 15*time.Second, "secret/grafana/admin is not readable")
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

	out, err := bao(t, "apps", "grafana", "default", "kv", "get", "-field=password", "secret/zot/ci")
	assert.Error(t, err, "grafana read zot's branch")
	assert.Contains(t, out, "permission denied")

	out, err = bao(t, "apps", "grafana", "default", "kv", "put", "secret/grafana/probe", "value=x")
	assert.Error(t, err, "grafana wrote to OpenBao")
	assert.Contains(t, out, "permission denied")
}

// The writer is global-secrets' default service account only
func TestSecretsWriterRole(t *testing.T) {
	secretsSandboxOnly(t)
	waitForUnsealed(t)
	out, err := bao(t, "writer", "grafana", "default", "kv", "get", "secret/grafana/admin")
	assert.Error(t, err, "grafana logged in as writer")
	assert.Contains(t, out, "not authorized")
}

// Emergency access: the admin role, for service account openbao-admin
func TestSecretsAdmin(t *testing.T) {
	secretsSandboxOnly(t)
	waitForUnsealed(t)
	out, err := bao(t, "admin", "openbao", "openbao-admin", "policy", "list")
	require.NoError(t, err, out)
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

// After its pod is replaced OpenBao unseals itself and keeps its data. Consumers keep their Secrets meanwhile; External
// Secrets retries the reads that fail while the pod restarts
func TestSecretsSurviveRestart(t *testing.T) {
	secretsSandboxOnly(t)
	waitForUnsealed(t)
	before := grafanaAdmin(t)
	k8s.RunKubectl(t, k8s.NewKubectlOptions("", "", "openbao"), "delete", "pod", "openbao-0", "--wait=true")
	waitForUnsealed(t)
	assert.Equal(t, before, grafanaAdmin(t))
}
