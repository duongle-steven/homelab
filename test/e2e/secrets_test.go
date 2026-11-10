package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/gensecrets"
	"github.com/duongle-steven/homelab/test/internal/kube"
	"github.com/duongle-steven/homelab/test/internal/repo"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// bao runs the OpenBao CLI in OpenBao's pod, logged in with a token of service account namespace/account (audience
// openbao, the only one OpenBao's roles accept) and the given role. The token reaches the pod on stdin and is never
// logged
func bao(ctx context.Context, role, namespace, account string, args ...string) (string, error) {
	return baoAudience(ctx, "openbao", role, namespace, account, args...)
}

func baoAudience(ctx context.Context, audience, role, namespace, account string, args ...string) (string, error) {
	tokenArgs := []string{"create", "token", account, "--duration=10m"}
	if audience != "" {
		tokenArgs = append(tokenArgs, "--audience="+audience)
	}
	token, err := kube.Kubectl(ctx, namespace, tokenArgs...)
	Expect(err).NotTo(HaveOccurred(), "kubectl create token %s/%s", namespace, account)
	script := `BAO_TOKEN="$(bao write -field=token auth/kubernetes/login role="$0" jwt=-)" && export BAO_TOKEN && exec bao "$@"`
	cmd := exec.CommandContext(ctx, "kubectl", append([]string{"-n", "openbao", "exec", "-i", "openbao-0", "-c", "openbao", "--",
		"sh", "-c", script, role}, args...)...)
	cmd.Stdin = strings.NewReader(token)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// baoValue reads secret/<path>#<property> with the admin role (baoValueE) and fails the spec unless it has a value
func baoValue(ctx context.Context, path, property string) string {
	GinkgoHelper()
	value, err := baoValueE(ctx, path, property)
	Expect(err).NotTo(HaveOccurred())
	Expect(value).NotTo(BeEmpty(), "secret/%s#%s", path, property)
	return value
}

// baoValueE reads OpenBao the documented way: an admin login inside OpenBao's pod, the token never printed. Its error
// carries OpenBao's message (never a value): the in-pod script exits with bao kv get's status, revoking the token
// either way
func baoValueE(ctx context.Context, path, property string) (string, error) {
	script := "kubectl -n openbao create token openbao-admin --audience=openbao --duration=10m | kubectl -n openbao exec -i openbao-0 -c openbao -- " +
		"sh -c 'export BAO_TOKEN=$(bao write -field=token auth/kubernetes/login role=admin jwt=-) && bao kv get -field=" + property + " secret/" + path + "; s=$?; bao token revoke -self >/dev/null 2>&1; exit $s'"
	var stdout, stderr strings.Builder
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("secret/%s#%s: %w: %s", path, property, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// permanentBaoError tells a read that cannot succeed later (the path exists without that property) from one that may
// (not pushed yet, OpenBao restarting)
func permanentBaoError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not present in map")
}

// waitForUnsealed waits until OpenBao's pod answers, initialised and unsealed
func waitForUnsealed(ctx context.Context) {
	Eventually(ctx, func() bool {
		cmd := exec.CommandContext(ctx, "kubectl", "-n", "openbao", "exec", "openbao-0", "-c", "openbao", "--",
			"bao", "status", "-format=json")
		out, err := cmd.Output()
		if err != nil {
			return false
		}
		var status struct {
			Initialized bool `json:"initialized"`
			Sealed      bool `json:"sealed"`
		}
		return json.Unmarshal(out, &status) == nil && status.Initialized && !status.Sealed
	}).WithTimeout(30*time.Minute).WithPolling(10*time.Second).Should(BeTrue(), "OpenBao is not initialised and unsealed")
}

// generatedValue is a generated secret as every app reads it, from OpenBao (secret/<path>, admin role), once pushed:
// it waits up to 20 minutes for the push, and fails at once on a property the path does not hold
func generatedValue(ctx context.Context, path, property string) string {
	GinkgoHelper()
	deadline := time.Now().Add(20 * time.Minute)
	for {
		value, err := baoValueE(ctx, path, property)
		if err == nil && value != "" {
			return value
		}
		if permanentBaoError(err) || time.Now().After(deadline) {
			Fail(fmt.Sprintf("generated value not readable: secret/%s#%s: %v", path, property, err))
		}
		select {
		case <-ctx.Done():
			Fail(fmt.Sprintf("generated value not readable: secret/%s#%s: %v", path, property, ctx.Err()))
		case <-time.After(15 * time.Second):
		}
	}
}

// grafanaAdmin reads Grafana's admin password from its branch, as Grafana's namespace, once generated-secrets has
// pushed it
func grafanaAdmin(ctx context.Context) string {
	var password string
	Eventually(ctx, func() bool {
		out, err := bao(ctx, "apps", "grafana", "default", "kv", "get", "-field=password", "secret/grafana/generated/admin")
		password = out
		return err == nil && out != ""
	}).WithTimeout(20*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "secret/grafana/generated/admin is not readable")
	return password
}

// admin runs the OpenBao CLI with the admin role and fails the spec on an error
func admin(ctx context.Context, args ...string) string {
	GinkgoHelper()
	out, err := bao(ctx, "admin", "openbao", "openbao-admin", args...)
	Expect(err).NotTo(HaveOccurred(), out)
	return out
}

// deferAdmin runs admin when the spec ends, passed or not
func deferAdmin(args ...string) {
	DeferCleanup(func(ctx SpecContext) { admin(ctx, args...) }, NodeTimeout(5*time.Minute))
}

var _ = Describe("SecretsUnsealed", Label("sandbox", "testbed", "secrets"), func() {
	It("is initialised and unsealed", func(ctx SpecContext) {
		waitForUnsealed(ctx)
	}, SpecTimeout(35*time.Minute))
})

// A namespace reads its own branch, not another's, and writes nowhere
var _ = Describe("SecretsNamespaceBranches", Label("sandbox", "testbed", "secrets"), func() {
	It("keeps a namespace to its own branch", func(ctx SpecContext) {
		waitForUnsealed(ctx)
		Expect(grafanaAdmin(ctx)).NotTo(BeEmpty())

		out, err := bao(ctx, "apps", "grafana", "default", "kv", "get", "-field=password", "secret/zot/generated/ci")
		Expect(err).To(HaveOccurred(), "grafana read zot's branch")
		Expect(out).To(ContainSubstring("permission denied"))

		out, err = bao(ctx, "apps", "grafana", "default", "kv", "put", "secret/grafana/probe", "value=x")
		Expect(err).To(HaveOccurred(), "grafana wrote to OpenBao")
		Expect(out).To(ContainSubstring("permission denied"))
	}, SpecTimeout(55*time.Minute))
})

// The writer is generated-secrets' default service account only, and reaches only secret/<namespace>/generated/: it reads
// and writes nothing else and deletes nothing
var _ = Describe("SecretsWriterRole", Label("sandbox", "testbed", "secrets"), func() {
	It("reaches only generated paths", func(ctx SpecContext) {
		waitForUnsealed(ctx)
		out, err := bao(ctx, "writer", "grafana", "default", "kv", "get", "secret/grafana/generated/admin")
		Expect(err).To(HaveOccurred(), "grafana logged in as writer")
		Expect(out).To(ContainSubstring("not authorized"))

		grafanaAdmin(ctx) // pushed by now
		admin(ctx, "kv", "put", "secret/grafana/writer-probe", "value=x")
		deferAdmin("kv", "metadata", "delete", "secret/grafana/writer-probe")
		for _, args := range [][]string{
			{"kv", "get", "secret/grafana/writer-probe"},
			{"kv", "put", "secret/grafana/writer-probe", "value=y"},
			{"kv", "put", "secret/grafana/other", "value=y"},
			{"kv", "delete", "secret/grafana/generated/admin"},
			{"kv", "metadata", "delete", "secret/grafana/generated/admin"},
		} {
			out, err := bao(ctx, "writer", "generated-secrets", "default", args...)
			Expect(err).To(HaveOccurred(), "writer: bao %v", args)
			Expect(out).To(ContainSubstring("permission denied"), "writer: bao %v", args)
		}
		out, err = bao(ctx, "writer", "generated-secrets", "default", "kv", "get", "-field=password", "secret/grafana/generated/admin")
		Expect(err).NotTo(HaveOccurred(), "the writer reads generated paths (External Secrets checks before it pushes)")
		Expect(out).NotTo(BeEmpty())
	}, SpecTimeout(55*time.Minute))
})

// OpenBao refuses tokens issued for the API server (the default audience): a token External Secrets sends cannot be
// replayed against the API server, and one stolen from a pod does not open OpenBao
var _ = Describe("SecretsAudience", Label("sandbox", "testbed", "secrets"), func() {
	It("refuses the API server's audience", func(ctx SpecContext) {
		waitForUnsealed(ctx)
		out, err := baoAudience(ctx, "", "apps", "grafana", "default", "kv", "get", "secret/grafana/generated/admin")
		Expect(err).To(HaveOccurred(), "OpenBao accepted a token without audience openbao")
		Expect(out).To(ContainSubstring("invalid audience"))
	}, SpecTimeout(35*time.Minute))
})

// Emergency access: the admin role, for service account openbao-admin
var _ = Describe("SecretsAdmin", Label("sandbox", "testbed", "secrets"), func() {
	It("lists the policies", func(ctx SpecContext) {
		waitForUnsealed(ctx)
		out := admin(ctx, "policy", "list")
		for _, policy := range []string{"apps", "writer", "admin"} {
			Expect(strings.Fields(out)).To(ContainElement(policy))
		}
	}, SpecTimeout(35*time.Minute))
})

// Grafana's admin Secret comes from OpenBao and equals its generated value there
var _ = Describe("SecretsReachConsumers", Label("sandbox", "testbed", "secrets"), func() {
	It("gives Grafana the generated password", func(ctx SpecContext) {
		waitForUnsealed(ctx)
		generated := generatedValue(ctx, "grafana/generated/admin", "password")
		Eventually(ctx, func() bool {
			var secret struct {
				Data map[string][]byte `json:"data"`
			}
			return kube.GetJSON(ctx, "grafana", &secret, "secret", "grafana-admin") == nil && string(secret.Data["admin-password"]) == generated
		}).WithTimeout(20*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "grafana-admin does not hold the generated password")
	}, SpecTimeout(75*time.Minute))
})

// After its pod is replaced OpenBao unseals itself and keeps its data: a value nothing pushes again (written with the
// admin role) is still there. Consumers keep their Secrets meanwhile; External Secrets retries the reads that fail
// while the pod restarts
var _ = Describe("SecretsSurviveRestart", Label("sandbox", "testbed", "secrets"), func() {
	It("keeps its data across a restart", func(ctx SpecContext) {
		waitForUnsealed(ctx)
		sentinel := fmt.Sprint(time.Now().UnixNano())
		admin(ctx, "kv", "put", "secret/smoke/restart", "value="+sentinel)
		deferAdmin("kv", "metadata", "delete", "secret/smoke/restart")
		_, err := kube.Kubectl(ctx, "openbao", "delete", "pod", "openbao-0", "--wait=true")
		Expect(err).NotTo(HaveOccurred())
		waitForUnsealed(ctx)
		Expect(admin(ctx, "kv", "get", "-field=value", "secret/smoke/restart")).To(Equal(sentinel))
	}, SpecTimeout(65*time.Minute))
})

// Role infra (OpenTofu's secrets, platform/infra-secrets) writes every branch except generated/, which it neither
// reads nor writes
var _ = Describe("SecretsInfraRole", Label("sandbox", "testbed", "secrets"), func() {
	It("writes every branch but generated/", func(ctx SpecContext) {
		waitForUnsealed(ctx)
		grafanaAdmin(ctx) // pushed by now
		deferAdmin("kv", "metadata", "delete", "secret/grafana/infra-probe")
		out, err := bao(ctx, "infra", "infra-secrets", "openbao-infra", "kv", "put", "secret/grafana/infra-probe", "value=x")
		Expect(err).NotTo(HaveOccurred(), out)
		for _, args := range [][]string{
			{"kv", "get", "secret/grafana/generated/admin"},
			{"kv", "put", "secret/grafana/generated/admin", "password=x"},
			{"kv", "put", "secret/grafana/generated/new", "value=x"},
			{"kv", "metadata", "get", "secret/grafana/generated/admin"},
			{"kv", "metadata", "delete", "secret/grafana/infra-probe"},
		} {
			out, err := bao(ctx, "infra", "infra-secrets", "openbao-infra", args...)
			Expect(err).To(HaveOccurred(), "infra: bao %v", args)
			Expect(out).To(ContainSubstring("permission denied"), "infra: bao %v", args)
		}
		out, err = bao(ctx, "infra", "grafana", "default", "kv", "get", "secret/grafana/infra-probe")
		Expect(err).To(HaveOccurred(), "grafana logged in as infra")
		Expect(out).To(ContainSubstring("not authorized"))
	}, SpecTimeout(55*time.Minute))
})

// OpenTofu's WireGuard key (infra/sandbox/secrets) reaches OpenBao through platform/infra-secrets, and comes back after
// its OpenBao value is lost
var _ = Describe("SecretsInfraPush", Label("sandbox", "testbed", "secrets"), func() {
	It("pushes OpenTofu's secret again once lost", func(ctx SpecContext) {
		waitForUnsealed(ctx)
		want := secretValue(ctx, "infra-secrets", "wireguard.private-key", "private-key")
		Expect(want).NotTo(BeEmpty())
		// The sandbox has no namespace wireguard (production only): read with the admin role
		read := func() string {
			out, err := bao(ctx, "admin", "openbao", "openbao-admin", "kv", "get", "-field=private-key", "secret/wireguard/private-key")
			if err != nil {
				return ""
			}
			return out
		}
		Eventually(ctx, func() bool { return read() == want }).WithTimeout(20*time.Minute).WithPolling(15*time.Second).
			Should(BeTrue(), "secret/wireguard/private-key does not hold the Secret's value")

		admin(ctx, "kv", "metadata", "delete", "secret/wireguard/private-key")
		_, err := kube.Kubectl(ctx, "infra-secrets", "annotate", "pushsecret", "wireguard.private-key",
			"force-sync="+fmt.Sprint(time.Now().UnixNano()), "--overwrite")
		Expect(err).NotTo(HaveOccurred())
		Eventually(ctx, func() bool { return read() == want }).WithTimeout(5*time.Minute).WithPolling(10*time.Second).
			Should(BeTrue(), "the lost value was not pushed again")
	}, SpecTimeout(60*time.Minute))
})

// Generated secrets live only in OpenBao: no Secret in generated-secrets, a push again leaves every value as it is,
// one entry's destinations hold the same value, and a destination deleted (a rotation) is written again
var _ = Describe("SecretsGeneratedOnlyInOpenBao", Label("sandbox", "testbed", "secrets"), func() {
	It("keeps generated values only in OpenBao", func(ctx SpecContext) {
		waitForUnsealed(ctx)
		secrets, err := kube.Kubectl(ctx, "generated-secrets", "get", "secrets", "-o", "name")
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(secrets)).To(BeEmpty(), "a Secret in generated-secrets")

		hash := func(path, property string) string {
			sum := sha256.Sum256([]byte(generatedValue(ctx, path, property)))
			return hex.EncodeToString(sum[:])
		}
		// Every destination of every entry holds a value under the entry's key (values.yaml and the extras testbed and
		// the sandbox share)
		for _, file := range []string{"values.yaml", "values-local.yaml"} {
			out, err := exec.CommandContext(ctx, "yq", "-o=json", "-I=0", "(.secrets // []) + (.extraSecrets // [])", filepath.Join(repo.Root(), "platform/generated-secrets", file)).Output()
			Expect(err).NotTo(HaveOccurred(), file)
			var entries []gensecrets.Entry
			Expect(json.Unmarshal(out, &entries)).To(Succeed(), file)
			for _, e := range entries {
				for _, destination := range e.Destinations {
					Expect(hash(destination, e.Key)).NotTo(BeEmpty(), "%s: %s#%s", e.Name, destination, e.Key)
				}
			}
		}
		// Both destinations of one entry: the same push wrote them
		Expect(hash("monitoring-system/generated/ntfy", "token")).To(Equal(hash("ntfy/generated/alerts-token", "token")))

		before := hash("grafana/generated/admin", "password")
		// forceSync returns once the PushSecret has synced since the annotation (its syncedResourceVersion follows the
		// annotations), so a comparison after it saw a push
		forceSync := func(name string) {
			synced := func() string {
				out, err := kube.Kubectl(ctx, "generated-secrets", "get", "pushsecret", name, "-o", "jsonpath={.status.syncedResourceVersion}")
				Expect(err).NotTo(HaveOccurred())
				return out
			}
			previous := synced()
			_, err := kube.Kubectl(ctx, "generated-secrets", "annotate", "pushsecret", name, fmt.Sprintf("force-sync=%d", time.Now().UnixNano()), "--overwrite")
			Expect(err).NotTo(HaveOccurred())
			Eventually(ctx, func() bool { return synced() != previous }).WithTimeout(2*time.Minute).WithPolling(2*time.Second).
				Should(BeTrue(), "PushSecret %s did not sync", name)
		}
		for range 2 {
			forceSync("grafana.admin")
			Expect(hash("grafana/generated/admin", "password")).To(Equal(before), "a push replaced an existing value")
		}

		// A rotation as docs/concepts/secrets-management.md does it: the destination deleted, the next push writes a new
		// value, the readers' ExternalSecrets refreshed and the readers restarted; its server and agents must agree on it
		// again (Woodpecker may run after this)
		probe := hash("woodpecker/generated/agent", "secret")
		admin(ctx, "kv", "metadata", "delete", "secret/woodpecker/generated/agent")
		forceSync("woodpecker.agent")
		rotated := hash("woodpecker/generated/agent", "secret")
		Expect(rotated).NotTo(Equal(probe), "the deleted destination was not written again")
		for _, name := range []string{"woodpecker-secret", "woodpecker-agent"} {
			_, err := kube.Kubectl(ctx, "woodpecker", "annotate", "externalsecret", name, fmt.Sprintf("force-sync=%d", time.Now().UnixNano()), "--overwrite")
			Expect(err).NotTo(HaveOccurred())
			Eventually(ctx, func() bool {
				out, err := kube.Kubectl(ctx, "woodpecker", "get", "secret", name, "-o", "jsonpath={.data.WOODPECKER_AGENT_SECRET}")
				if err != nil {
					return false
				}
				value, err := base64.StdEncoding.DecodeString(out)
				sum := sha256.Sum256(value)
				return err == nil && hex.EncodeToString(sum[:]) == rotated
			}).WithTimeout(2*time.Minute).WithPolling(2*time.Second).Should(BeTrue(), "Secret woodpecker/%s does not hold the rotated value", name)
		}
		out, err := kube.Kubectl(ctx, "woodpecker", "rollout", "restart", "statefulset/woodpecker-server")
		Expect(err).NotTo(HaveOccurred(), out)
		// The agent starts again on a new claim, as lab's rotation does (workload.ResetClaims): the id on its old one is
		// unknown to the server once the agent secret changed ("AgentID not found in database")
		const claim = "agent-config-woodpecker-agent-0"
		uid, err := kube.Kubectl(ctx, "woodpecker", "get", "pvc", claim, "-o", "jsonpath={.metadata.uid}")
		Expect(err).NotTo(HaveOccurred(), uid)
		out, err = kube.Kubectl(ctx, "woodpecker", "delete", "pvc", claim, "--wait=false")
		Expect(err).NotTo(HaveOccurred(), out)
		Eventually(ctx, func() bool {
			if _, err := kube.Kubectl(ctx, "woodpecker", "delete", "pod", "woodpecker-agent-0", "--ignore-not-found", "--wait=false"); err != nil {
				return false
			}
			current, err := kube.Kubectl(ctx, "woodpecker", "get", "pvc", claim, "--ignore-not-found", "-o", "jsonpath={.metadata.uid}")
			return err == nil && current != uid
		}).WithTimeout(5*time.Minute).WithPolling(5*time.Second).Should(BeTrue(), "claim %s was not replaced", claim)
		// A pod created while the old claim was terminating may still hold it
		out, err = kube.Kubectl(ctx, "woodpecker", "delete", "pod", "woodpecker-agent-0", "--ignore-not-found")
		Expect(err).NotTo(HaveOccurred(), out)
		for _, workload := range []string{"statefulset/woodpecker-server", "statefulset/woodpecker-agent"} {
			out, err := kube.Kubectl(ctx, "woodpecker", "rollout", "status", workload, "--timeout=5m")
			Expect(err).NotTo(HaveOccurred(), out)
		}
	}, SpecTimeout(60*time.Minute))
})

// A path or property that cannot appear fails at once with OpenBao's message, not after generatedValue's 20 minutes
var _ = Describe("SecretsGeneratedValueErrors", Label("sandbox", "testbed", "secrets"), func() {
	It("tells a missing property from a path not pushed yet", func(ctx SpecContext) {
		waitForUnsealed(ctx)
		generatedValue(ctx, "grafana/generated/admin", "password") // pushed by now
		_, err := baoValueE(ctx, "grafana/generated/admin", "no-such-property")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("no-such-property"))
		Expect(permanentBaoError(err)).To(BeTrue(), "a missing property is not passing trouble: %v", err)
		_, err = baoValueE(ctx, "grafana/generated/no-such-entry", "password")
		Expect(err).To(HaveOccurred())
		Expect(permanentBaoError(err)).To(BeFalse(), "a path not pushed yet may still come: %v", err)
	}, SpecTimeout(55*time.Minute))
})

// baoPut writes secret/<path> value=<value> the documented way (an admin login inside OpenBao's pod); value is a test
// marker, never a secret
func baoPut(ctx context.Context, path, value string, del bool) {
	GinkgoHelper()
	op := "bao kv put secret/" + path + " value=" + value + " >/dev/null"
	if del {
		op = "bao kv metadata delete secret/" + path + " >/dev/null"
	}
	script := "kubectl -n openbao create token openbao-admin --audience=openbao --duration=10m | kubectl -n openbao exec -i openbao-0 -c openbao -- " +
		"sh -c 'export BAO_TOKEN=$(bao write -field=token auth/kubernetes/login role=admin jwt=-) && " + op + "; s=$?; bao token revoke -self >/dev/null 2>&1; exit $s'"
	out, err := exec.CommandContext(ctx, "sh", "-c", script).CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "secret/%s: %s", path, out)
}

// baoPutStdin writes secret/<path> key=<value> with the admin role, the value on stdin (never in a command line)
func baoPutStdin(ctx context.Context, path, key, value string) {
	GinkgoHelper()
	token, err := kube.Kubectl(ctx, "openbao", "create", "token", "openbao-admin", "--duration=10m", "--audience=openbao")
	Expect(err).NotTo(HaveOccurred(), "kubectl create token openbao/openbao-admin")
	script := `read -r jwt; read -r v; BAO_TOKEN="$(echo "$jwt" | bao write -field=token auth/kubernetes/login role=admin jwt=-)" && export BAO_TOKEN && printf %s "$v" | bao kv put "$0" "$1=-" >/dev/null`
	cmd := exec.CommandContext(ctx, "kubectl", "-n", "openbao", "exec", "-i", "openbao-0", "-c", "openbao", "--", "sh", "-c", script, "secret/"+path, key)
	cmd.Stdin = strings.NewReader(token + "\n" + value + "\n")
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))
}
