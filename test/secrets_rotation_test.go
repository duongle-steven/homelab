package test

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rotateSecrets runs lab secrets rotate on the sandbox for names (no off-site backup is needed there)
func rotateSecrets(t *testing.T, names ...string) string {
	t.Helper()
	return lab(t, append(append([]string{"secrets", "rotate"}, names...), "--env", "sandbox", "--yes", "--skip-backup-check=sandbox test")...)
}

// baoPutStdin writes secret/<path> key=<value> with the admin role, the value on stdin (never in a command line)
func baoPutStdin(t *testing.T, path, key, value string) {
	t.Helper()
	options := k8s.NewKubectlOptions("", "", "openbao")
	options.Logger = logger.Discard
	token, err := k8s.RunKubectlAndGetOutputE(t, options, "create", "token", "openbao-admin", "--duration=10m", "--audience=openbao")
	require.NoError(t, err)
	script := `read -r jwt; read -r v; BAO_TOKEN="$(echo "$jwt" | bao write -field=token auth/kubernetes/login role=admin jwt=-)" && export BAO_TOKEN && printf %s "$v" | bao kv put "$0" "$1=-" >/dev/null`
	cmd := exec.Command("kubectl", "-n", "openbao", "exec", "-i", "openbao-0", "-c", "openbao", "--", "sh", "-c", script, "secret/"+path, key)
	cmd.Stdin = strings.NewReader(token + "\n" + value + "\n")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

func grafanaStatus(password string) int {
	status, _ := getJSONE(http.MethodGet, "https://"+grafanaHost+"/api/user", nil, "admin", password, nil, nil)
	return status
}

// lab secrets rotate on the sandbox, one entry per kind of method: the new value works, the old one is refused,
// and the readers run again (the sandbox has one replica per app: their short stops are not checked) (step 11b)
func TestSecretsRotation(t *testing.T) {
	secretsSandboxOnly(t)
	waitForUnsealed(t)

	t.Run("app password", func(t *testing.T) {
		waitForApp(t, "https://"+grafanaHost+"/api/health")
		old := generatedValue(t, "grafana/generated/admin", "password")
		require.Equal(t, http.StatusOK, grafanaStatus(old))
		rotateSecrets(t, "grafana.admin")
		next := generatedValue(t, "grafana/generated/admin", "password")
		require.NotEqual(t, old, next)
		assert.Eventually(t, func() bool { return grafanaStatus(next) == http.StatusOK }, 5*time.Minute, 5*time.Second, "the new password does not sign in")
		assert.Equal(t, http.StatusUnauthorized, grafanaStatus(old), "the old password still signs in")
	})

	t.Run("overlap", func(t *testing.T) {
		waitForNtfy(t)
		old := ntfyToken(t, "alerts")
		rotateSecrets(t, "ntfy.alerts-token")
		next := ntfyToken(t, "alerts")
		require.NotEqual(t, old, next)
		assert.Equal(t, next, "tk_"+generatedValue(t, "monitoring-system/generated/ntfy", "token"), "Alertmanager's copy")
		assert.Eventually(t, func() bool {
			status, _ := ntfyRequest(t, http.MethodGet, "/v1/account", next, "")
			return status == http.StatusOK
		}, 5*time.Minute, 5*time.Second, "the new token is refused")
		status, _ := ntfyRequest(t, http.MethodGet, "/v1/account", old, "")
		assert.Equal(t, http.StatusUnauthorized, status, "the old token still works after the rotation")
		assert.Empty(t, baoValueOrEmpty("ntfy/generated/alerts-token", "previous_token"), "previous_token outlived the rotation")
	})

	t.Run("forgejo token", func(t *testing.T) {
		waitForApp(t, "https://"+forgejoHost+"/api/healthz")
		lab(t, "infra", "forgejo", "sync", "--env", "sandbox")
		old := generatedValue(t, "argocd/forgejo", "token")
		rotateSecrets(t, "forgejo.argocd-token")
		next := generatedValue(t, "argocd/forgejo", "token")
		require.NotEqual(t, old, next)
		read := func(token string) int {
			status, _ := getJSONE(http.MethodGet, "https://"+forgejoHost+"/api/v1/repos/ops/homelab", nil, "", "", nil, nil, "Authorization", "token "+token)
			return status
		}
		assert.Equal(t, http.StatusOK, read(next))
		assert.NotEqual(t, http.StatusOK, read(old), "the old token still reads ops/homelab")
		// Argo CD's credential follows (it reads ops/homelab in production)
		assert.Eventually(t, func() bool { return secretValue(t, "argocd", "argocd-repo-forgejo", "password") == next },
			5*time.Minute, 5*time.Second, "argocd-repo-forgejo still holds the old token")
		// lab infra forgejo sync keeps it
		lab(t, "infra", "forgejo", "sync", "--env", "sandbox")
		assert.Equal(t, next, generatedValue(t, "argocd/forgejo", "token"))
	})

	t.Run("kaniop client", func(t *testing.T) {
		old := secretValue(t, "grafana", "grafana-kanidm-oauth2-credentials", "CLIENT_SECRET")
		rotateSecrets(t, "kanidm-client.grafana")
		assert.NotEqual(t, old, secretValue(t, "grafana", "grafana-kanidm-oauth2-credentials", "CLIENT_SECRET"))
		waitForApp(t, "https://"+grafanaHost+"/api/health")
	})

	t.Run("woodpecker agent", func(t *testing.T) {
		// The token lab uses, as a person makes it once (docs/how-to-guides/woodpecker.md); dev-admin is the sandbox's
		// Woodpecker admin
		token := woodpeckerToken(t, "dev-admin")
		baoPutStdin(t, "woodpecker/admin-token", "token", token)
		old := generatedValue(t, "woodpecker/generated/agent", "secret")
		rotateSecrets(t, "woodpecker.agent")
		require.NotEqual(t, old, generatedValue(t, "woodpecker/generated/agent", "secret"))
		assert.Eventually(t, func() bool {
			status, raw := woodpeckerRequest(t, http.MethodGet, "/api/agents", token, nil, nil)
			var agents []map[string]any
			return status == http.StatusOK && json.Unmarshal(raw, &agents) == nil && len(agents) > 0
		}, 5*time.Minute, 5*time.Second, "no agent registered with the new secret")
	})

	t.Run("scheduled", func(t *testing.T) {
		options := k8s.NewKubectlOptions("", "", "secrets-rotation")
		job := "rotation-test-" + time.Now().Format("150405")
		k8s.RunKubectl(t, options, "create", "job", job, "--from=cronjob/secrets-rotation")
		// Nothing is due on a new sandbox: the run logs in to OpenBao with its own role, takes no lock, and succeeds
		if err := k8s.RunKubectlE(t, options, "wait", "--for=condition=Complete", "--timeout=5m", "job/"+job); err != nil {
			logs, _ := k8s.RunKubectlAndGetOutputE(t, options, "logs", "job/"+job)
			require.FailNow(t, "the scheduled run failed", logs)
		}
		// An interrupted rotation by command cannot be finished by the CronJob: the run fails, which alerts
		baoPutMeta(t, "rotation/kanidm-client.paperless", map[string]string{"rotation-phase": "switched", "rotation-method": "kaniop-client"})
		failing := job + "-f"
		k8s.RunKubectl(t, options, "create", "job", failing, "--from=cronjob/secrets-rotation")
		require.NoError(t, k8s.RunKubectlE(t, options, "wait", "--for=condition=Failed", "--timeout=5m", "job/"+failing))
		assert.Eventually(t, func() bool {
			alerts, err := prometheusQuery(t, `ALERTS{alertname="SecretRotationFailed", alertstate="firing"}`)
			return err == nil && len(alerts) > 0
		}, 10*time.Minute, 15*time.Second, "SecretRotationFailed does not fire")
		// A person finishes it, and the next run succeeds again
		rotateSecrets(t, "kanidm-client.paperless")
		k8s.RunKubectl(t, options, "delete", "job", failing)
	})
}

// baoValueOrEmpty is secret/<path>#<property>, or "" when it is not there
func baoValueOrEmpty(path, property string) string {
	value, err := baoValueE(path, property)
	if err != nil {
		return ""
	}
	return value
}

// baoPutMeta sets custom metadata on secret/<path> with the admin role (rotation records: phases and names, no value)
func baoPutMeta(t *testing.T, path string, meta map[string]string) {
	t.Helper()
	args := []string{"kv", "metadata", "put"}
	for k, v := range meta {
		args = append(args, "-custom-metadata="+k+"="+v)
	}
	admin(t, append(args, "secret/"+path)...)
}
