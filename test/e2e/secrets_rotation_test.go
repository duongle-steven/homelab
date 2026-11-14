package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// rotateSecrets runs lab secrets rotate on the sandbox for names (no off-site backup is needed there)
func rotateSecrets(ctx context.Context, names ...string) string {
	GinkgoHelper()
	return lab(ctx, append(append([]string{"secrets", "rotate"}, names...), "--env", labEnv, "--yes", "--skip-backup-check=sandbox test")...)
}

func grafanaStatus(ctx context.Context, password string) int {
	status, _ := getJSONE(ctx, http.MethodGet, "https://"+grafanaHost+"/api/user", nil, "admin", password, nil, nil)
	return status
}

// Rotation runs lab secrets rotate on the sandbox, one entry per kind of method, alone after the suite (it restarts
// most apps): the new value works, the old one is refused, each method checked even when another failed,
// and the readers run again (the sandbox has one replica per app: their short stops are not checked) (step 11b)
var _ = Describe("Rotation", Ordered, ContinueOnFailure, Serial, Label("sandbox", "testbed", "rotation", "alone"), func() {
	BeforeAll(func(ctx SpecContext) { waitForUnsealed(ctx) }, NodeTimeout(35*time.Minute))

	It("app password", func(ctx SpecContext) {
		waitForApp(ctx, "https://"+grafanaHost+"/api/health")
		old := generatedValue(ctx, "grafana/generated/admin", "password")
		Expect(grafanaStatus(ctx, old)).To(Equal(http.StatusOK))
		rotateSecrets(ctx, "grafana.admin")
		next := generatedValue(ctx, "grafana/generated/admin", "password")
		Expect(old == next).To(BeFalse(), "the value did not change")
		Eventually(ctx, func() int { return grafanaStatus(ctx, next) }).WithTimeout(5*time.Minute).WithPolling(5*time.Second).
			Should(Equal(http.StatusOK), "the new password does not sign in")
		Expect(grafanaStatus(ctx, old)).To(Equal(http.StatusUnauthorized), "the old password still signs in")
	}, SpecTimeout(50*time.Minute))

	It("overlap", func(ctx SpecContext) {
		ntfyUp(ctx)
		old := ntfyToken(ctx, "alerts")
		rotateSecrets(ctx, "ntfy.alerts-token")
		next := ntfyToken(ctx, "alerts")
		Expect(old == next).To(BeFalse(), "the value did not change")
		Expect(next == "tk_"+generatedValue(ctx, "monitoring-system/generated/ntfy", "token")).To(BeTrue(), "Alertmanager's copy differs")
		Eventually(ctx, func() int {
			status, _ := ntfyRequest(ctx, http.MethodGet, "/v1/account", next, "")
			return status
		}).WithTimeout(5*time.Minute).WithPolling(5*time.Second).Should(Equal(http.StatusOK), "the new token is refused")
		status, _ := ntfyRequest(ctx, http.MethodGet, "/v1/account", old, "")
		Expect(status).To(Equal(http.StatusUnauthorized), "the old token still works after the rotation")
		Expect(baoValueOrEmpty(ctx, "ntfy/generated/alerts-token", "previous_token")).To(BeEmpty(), "previous_token outlived the rotation")
	}, SpecTimeout(50*time.Minute))

	It("forgejo token", func(ctx SpecContext) {
		waitForApp(ctx, "https://"+forgejoHost+"/api/healthz")
		lab(ctx, "infra", "forgejo", "sync", "--env", labEnv)
		old := generatedValue(ctx, "argocd/forgejo", "token")
		rotateSecrets(ctx, "forgejo.argocd-token")
		next := generatedValue(ctx, "argocd/forgejo", "token")
		Expect(old == next).To(BeFalse(), "the value did not change")
		read := func(token string) int {
			status, _ := getJSONE(ctx, http.MethodGet, "https://"+forgejoHost+"/api/v1/repos/ops/homelab", nil, "", "", nil, nil, "Authorization", "token "+token)
			return status
		}
		Expect(read(next)).To(Equal(http.StatusOK))
		Expect(read(old)).NotTo(Equal(http.StatusOK), "the old token still reads ops/homelab")
		// Argo CD's credential follows (it reads ops/homelab in production)
		Eventually(ctx, func() bool { return secretValue(ctx, "argocd", "argocd-repo-forgejo", "password") == next }).
			WithTimeout(5*time.Minute).WithPolling(5*time.Second).Should(BeTrue(), "argocd-repo-forgejo still holds the old token")
		// lab infra forgejo sync keeps it
		lab(ctx, "infra", "forgejo", "sync", "--env", labEnv)
		Expect(next == generatedValue(ctx, "argocd/forgejo", "token")).To(BeTrue(), "sync replaced the rotated token")
	}, SpecTimeout(50*time.Minute))

	It("kaniop client", func(ctx SpecContext) {
		old := secretValue(ctx, "grafana", "grafana-kanidm-oauth2-credentials", "CLIENT_SECRET")
		rotateSecrets(ctx, "kanidm-client.grafana")
		Expect(old == secretValue(ctx, "grafana", "grafana-kanidm-oauth2-credentials", "CLIENT_SECRET")).To(BeFalse(), "the client secret did not change")
		waitForApp(ctx, "https://"+grafanaHost+"/api/health")
	}, SpecTimeout(30*time.Minute))

	// registry.ci: one value in zot's and ci's branches, after the golden path's idle wait; zot's htpasswd and ci's
	// Secret follow it
	It("registry ci", func(ctx SpecContext) {
		// Finishes a rotation an earlier run left interrupted, which already holds its new value
		rotateSecrets(ctx, "registry.ci")
		old := generatedValue(ctx, "zot/generated/ci", "password")
		rotateSecrets(ctx, "registry.ci")
		value := generatedValue(ctx, "zot/generated/ci", "password")
		Expect(old == value).To(BeFalse(), "the value did not change")
		Expect(generatedValue(ctx, "ci/generated/registry", "password") == value).To(BeTrue(), "ci's branch differs from zot's")
		Eventually(ctx, func() bool {
			return secretValue(ctx, "ci", "registry", "password") == value
		}).WithTimeout(2*time.Minute).WithPolling(5*time.Second).Should(BeTrue(), "Secret ci/registry does not hold the new value")
	}, SpecTimeout(40*time.Minute))

	It("all", func(ctx SpecContext) {
		// Assume everything leaked, from a script (no terminal): manual steps and other environments' entries are
		// skipped, the phone token waits for people, and the run goes through the rest
		// Nobody re-enters the phone token in this sandbox: its wait is closed afterwards (both tokens stay valid), even
		// if the run fails, or the scheduled run below would report it as a rotation to finish by hand (as it does in
		// production)
		closed := time.Now().UTC().Format(time.RFC3339)
		DeferCleanup(func(ctx SpecContext) {
			baoPutMeta(ctx, "rotation/ntfy.phone-token", map[string]string{"rotation-phase": "done", "rotation-method": "ntfy-overlap",
				"rotated-by": "test", "rotated-at": closed})
		}, NodeTimeout(5*time.Minute))
		out := lab(ctx, "secrets", "rotate", "--all", "--env", labEnv, "--yes", "--skip-backup-check=sandbox test")
		results := out[strings.LastIndex(out, "Results:"):]
		Expect(results).NotTo(ContainSubstring("failed"))
		Expect(results).NotTo(ContainSubstring("not started"))
		Expect(results).To(ContainSubstring("ntfy.phone-token: waiting for people"))
		Expect(results).To(ContainSubstring("cloudflare.cert-manager: skipped: production only"))
		Expect(results).To(ContainSubstring("grafana.admin: rotated"))
		// paperless is optional in the sandbox: --all skips its entries when it is not deployed
		if !appDeployed(ctx, "paperless") {
			Expect(results).To(ContainSubstring("paperless.secret-key: skipped: not deployed here"))
		}
		waitForApp(ctx, "https://"+grafanaHost+"/api/health")
		waitForApp(ctx, "https://"+forgejoHost+"/api/healthz")
	}, SpecTimeout(60*time.Minute))

	It("scheduled", func(ctx SpecContext) {
		// The CronJob rotates a due entry with its own access: a fresh off-site backup first, then ntfy's alerts token
		// made 100 days old
		dailyBackup(ctx)
		baoPutMeta(ctx, "rotation/ntfy.alerts-token", map[string]string{"rotation-phase": "done", "rotation-method": "ntfy-overlap",
			"rotated-by": "test", "rotated-at": time.Now().Add(-100 * 24 * time.Hour).UTC().Format(time.RFC3339)})
		old := ntfyToken(ctx, "alerts")
		job := "rotation-test-" + time.Now().Format("150405")
		out, err := kube.Kubectl(ctx, "secrets-rotation", "create", "job", job, "--from=cronjob/secrets-rotation")
		Expect(err).NotTo(HaveOccurred(), out)
		if !jobSucceeds(ctx, "secrets-rotation", job) {
			logs, _ := kube.Kubectl(ctx, "secrets-rotation", "logs", "job/"+job)
			Fail("the scheduled run failed: " + logs)
		}
		next := ntfyToken(ctx, "alerts")
		Expect(old == next).To(BeFalse(), "the scheduled run did not rotate the due token")
		Eventually(ctx, func() int {
			status, _ := ntfyRequest(ctx, http.MethodGet, "/v1/account", next, "")
			return status
		}).WithTimeout(5*time.Minute).WithPolling(5*time.Second).Should(Equal(http.StatusOK), "the token the CronJob made is refused")
		// An interrupted rotation by command cannot be finished by the CronJob: the run fails, which alerts
		baoPutMeta(ctx, "rotation/kanidm-client.grafana", map[string]string{"rotation-phase": "switched", "rotation-method": "kaniop-client"})
		failing := job + "-f"
		out, err = kube.Kubectl(ctx, "secrets-rotation", "create", "job", failing, "--from=cronjob/secrets-rotation")
		Expect(err).NotTo(HaveOccurred(), out)
		Expect(jobSucceeds(ctx, "secrets-rotation", failing)).To(BeFalse(), "a run with an interrupted rotation by command succeeded")
		Eventually(ctx, func() bool {
			alerts, err := prometheusQuery(ctx, `ALERTS{alertname="SecretRotationFailed", alertstate="firing"}`)
			return err == nil && len(alerts) > 0
		}).WithTimeout(10*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "SecretRotationFailed does not fire")
		// A person finishes it, and the next run succeeds again
		rotateSecrets(ctx, "kanidm-client.grafana")
		out, err = kube.Kubectl(ctx, "secrets-rotation", "delete", "job", failing)
		Expect(err).NotTo(HaveOccurred(), out)
	}, SpecTimeout(60*time.Minute))
})

// baoValueOrEmpty is secret/<path>#<property>, or "" when it is not there
func baoValueOrEmpty(ctx context.Context, path, property string) string {
	value, err := baoValueE(ctx, path, property)
	if err != nil {
		return ""
	}
	return value
}

// baoPutMeta sets custom metadata on secret/<path> with the admin role (rotation records: phases and names, no value)
func baoPutMeta(ctx context.Context, path string, meta map[string]string) {
	GinkgoHelper()
	args := []string{"kv", "metadata", "put"}
	for k, v := range meta {
		args = append(args, "-custom-metadata="+k+"="+v)
	}
	admin(ctx, append(args, "secret/"+path)...)
}

// jobSucceeds waits up to 5 minutes for job to finish and reports whether it succeeded
func jobSucceeds(ctx context.Context, namespace, job string) bool {
	GinkgoHelper()
	var status string
	Eventually(ctx, func() bool {
		out, err := kube.Kubectl(ctx, namespace, "get", "job", job, "-o", "jsonpath={.status.succeeded}/{.status.failed}")
		status = out
		return err == nil && (strings.HasPrefix(out, "1/") || strings.HasSuffix(out, "/1"))
	}).WithTimeout(5*time.Minute).WithPolling(5*time.Second).Should(BeTrue(), "job %s did not finish", job)
	return strings.HasPrefix(status, "1/")
}

// dailyBackup takes an off-site backup the way the daily schedule does, and waits for it
func dailyBackup(ctx context.Context) {
	GinkgoHelper()
	// OpenBao's snapshot claim binds at its CronJob's first run (:55): before it Velero cannot back it up
	runCronJob(ctx, "openbao", "openbao-snapshot")
	var schedule struct {
		Spec struct {
			Template map[string]any `json:"template"`
		} `json:"spec"`
	}
	kubectlJSON(ctx, &schedule, "-n", "velero", "get", "schedules.velero.io", "daily")
	// A new cluster is read-only until lab backup enable; it is read-only again afterwards, as BackupRestore expects
	// (also when this fails: the cleanup disables it again)
	lab(ctx, "backup", "enable", "--yes", "--env", labEnv)
	DeferCleanup(func(ctx SpecContext) { _, _ = labE(ctx, "backup", "disable", "--yes", "--env", labEnv) }, NodeTimeout(10*time.Minute))
	name := fmt.Sprintf("daily-rotation-%d", time.Now().Unix())
	manifest, err := json.Marshal(map[string]any{
		"apiVersion": "velero.io/v1", "kind": "Backup",
		"metadata": map[string]any{"name": name, "namespace": "velero", "labels": map[string]string{"velero.io/schedule-name": "daily"}},
		"spec":     schedule.Spec.Template,
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(kube.Apply(ctx, "velero", string(manifest))).To(Succeed())
	waitBackup(ctx, name)
	lab(ctx, "backup", "disable", "--yes", "--env", labEnv)
}
