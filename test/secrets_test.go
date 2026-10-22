package test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

// generatedValue is a generated secret as every app reads it, from OpenBao (secret/<path>, admin role), once pushed:
// it waits up to 20 minutes for the push, and fails at once on a property the path does not hold
func generatedValue(t *testing.T, path, property string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Minute)
	for {
		value, err := baoValueE(path, property)
		if err == nil && value != "" {
			return value
		}
		if permanentBaoError(err) || time.Now().After(deadline) {
			require.FailNowf(t, "generated value not readable", "secret/%s#%s: %v", path, property, err)
		}
		time.Sleep(15 * time.Second)
	}
}

// grafanaAdmin reads Grafana's admin password from its branch, as Grafana's namespace, once generated-secrets has
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

// The writer is generated-secrets' default service account only, and reaches only secret/<namespace>/generated/: it reads
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
		out, err := bao(t, "writer", "generated-secrets", "default", args...)
		assert.Error(t, err, "writer: bao %v", args)
		assert.Contains(t, out, "permission denied", "writer: bao %v", args)
	}
	out, err = bao(t, "writer", "generated-secrets", "default", "kv", "get", "-field=password", "secret/grafana/generated/admin")
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

// Grafana's admin Secret comes from OpenBao and equals its generated value there
func TestSecretsReachConsumers(t *testing.T) {
	secretsSandboxOnly(t)
	waitForUnsealed(t)
	generated := generatedValue(t, "grafana/generated/admin", "password")
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

// OpenTofu's WireGuard key (infra/sandbox/secrets) reaches OpenBao through platform/infra-secrets, and comes back after
// its OpenBao value is lost
func TestSecretsInfraPush(t *testing.T) {
	secretsSandboxOnly(t)
	waitForUnsealed(t)
	want := secretValue(t, "infra-secrets", "wireguard.private-key", "private-key")
	require.NotEmpty(t, want)
	// The sandbox has no namespace wireguard (production only): read with the admin role
	read := func() string {
		out, err := bao(t, "admin", "openbao", "openbao-admin", "kv", "get", "-field=private-key", "secret/wireguard/private-key")
		if err != nil {
			return ""
		}
		return out
	}
	require.Eventually(t, func() bool { return read() == want }, 20*time.Minute, 15*time.Second,
		"secret/wireguard/private-key does not hold the Secret's value")

	admin(t, "kv", "metadata", "delete", "secret/wireguard/private-key")
	k8s.RunKubectl(t, k8s.NewKubectlOptions("", "", "infra-secrets"), "annotate", "pushsecret", "wireguard.private-key",
		"force-sync="+fmt.Sprint(time.Now().UnixNano()), "--overwrite")
	assert.Eventually(t, func() bool { return read() == want }, 5*time.Minute, 10*time.Second,
		"the lost value was not pushed again")
}

// Generated secrets live only in OpenBao: no Secret in generated-secrets, a push again leaves every value as it is,
// one entry's destinations hold the same value, and a destination deleted (a rotation) is written again
func TestSecretsGeneratedOnlyInOpenBao(t *testing.T) {
	secretsSandboxOnly(t)
	waitForUnsealed(t)
	options := k8s.NewKubectlOptions("", "", "generated-secrets")
	options.Logger = logger.Discard
	secrets, err := k8s.RunKubectlAndGetOutputE(t, options, "get", "secrets", "-o", "name")
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(secrets), "a Secret in generated-secrets")

	hash := func(path, property string) string {
		sum := sha256.Sum256([]byte(generatedValue(t, path, property)))
		return hex.EncodeToString(sum[:])
	}
	// Every destination of every entry holds a value under the entry's key (values.yaml and the sandbox's extras)
	for _, file := range []string{"values.yaml", "values-dev.yaml"} {
		out, err := exec.Command("yq", "-o=json", "-I=0", "(.secrets // []) + (.extraSecrets // [])", "../platform/generated-secrets/"+file).Output()
		require.NoError(t, err, file)
		var entries []generatedEntry
		require.NoError(t, json.Unmarshal(out, &entries), file)
		for _, e := range entries {
			for _, destination := range e.Destinations {
				assert.NotEmpty(t, hash(destination, e.Key), "%s: %s#%s", e.Name, destination, e.Key)
			}
		}
	}
	// Both destinations of one entry: the same push wrote them
	assert.Equal(t, hash("ntfy/generated/alerts-token", "token"), hash("monitoring-system/generated/ntfy", "token"))

	before := hash("grafana/generated/admin", "password")
	// forceSync returns once the PushSecret has synced since the annotation (its syncedResourceVersion follows the
	// annotations), so a comparison after it saw a push
	forceSync := func(name string) {
		synced := func() string {
			out, err := k8s.RunKubectlAndGetOutputE(t, options, "get", "pushsecret", name, "-o", "jsonpath={.status.syncedResourceVersion}")
			require.NoError(t, err)
			return out
		}
		previous := synced()
		_, err := k8s.RunKubectlAndGetOutputE(t, options, "annotate", "pushsecret", name, fmt.Sprintf("force-sync=%d", time.Now().UnixNano()), "--overwrite")
		require.NoError(t, err)
		require.Eventually(t, func() bool { return synced() != previous }, 2*time.Minute, 2*time.Second, "PushSecret %s did not sync", name)
	}
	for range 2 {
		forceSync("grafana.admin")
		assert.Equal(t, before, hash("grafana/generated/admin", "password"), "a push replaced an existing value")
	}

	// A rotation as docs/concepts/secrets-management.md does it: the destination deleted, the next push writes a new
	// value, the readers' ExternalSecrets refreshed and the readers restarted. Woodpecker's agent secret needs nothing
	// but a restart; its server and agents must agree on it again (TestWoodpecker may run after this)
	probe := hash("woodpecker/generated/agent", "secret")
	admin(t, "kv", "metadata", "delete", "secret/woodpecker/generated/agent")
	forceSync("woodpecker.agent")
	rotated := hash("woodpecker/generated/agent", "secret")
	assert.NotEqual(t, probe, rotated, "the deleted destination was not written again")
	woodpecker := k8s.NewKubectlOptions("", "", "woodpecker")
	woodpecker.Logger = logger.Discard
	for _, name := range []string{"woodpecker-secret", "woodpecker-agent"} {
		_, err := k8s.RunKubectlAndGetOutputE(t, woodpecker, "annotate", "externalsecret", name, fmt.Sprintf("force-sync=%d", time.Now().UnixNano()), "--overwrite")
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			out, err := k8s.RunKubectlAndGetOutputE(t, woodpecker, "get", "secret", name, "-o", "jsonpath={.data.WOODPECKER_AGENT_SECRET}")
			if err != nil {
				return false
			}
			value, err := base64.StdEncoding.DecodeString(out)
			sum := sha256.Sum256(value)
			return err == nil && hex.EncodeToString(sum[:]) == rotated
		}, 2*time.Minute, 2*time.Second, "Secret woodpecker/%s does not hold the rotated value", name)
	}
	for _, workload := range []string{"statefulset/woodpecker-server", "statefulset/woodpecker-agent"} {
		k8s.RunKubectl(t, woodpecker, "rollout", "restart", workload)
	}
	for _, workload := range []string{"statefulset/woodpecker-server", "statefulset/woodpecker-agent"} {
		k8s.RunKubectl(t, woodpecker, "rollout", "status", workload, "--timeout=5m")
	}
}

// A path or property that cannot appear fails at once with OpenBao's message, not after generatedValue's 20 minutes
func TestSecretsGeneratedValueErrors(t *testing.T) {
	secretsSandboxOnly(t)
	waitForUnsealed(t)
	generatedValue(t, "grafana/generated/admin", "password") // pushed by now
	_, err := baoValueE("grafana/generated/admin", "no-such-property")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no-such-property")
	assert.True(t, permanentBaoError(err), "a missing property is not passing trouble: %v", err)
	_, err = baoValueE("grafana/generated/no-such-entry", "password")
	require.Error(t, err)
	assert.False(t, permanentBaoError(err), "a path not pushed yet may still come: %v", err)
}
