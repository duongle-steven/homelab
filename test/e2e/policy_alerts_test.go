package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// rulerAlerts returns the labels of the alerts Loki's ruler holds active (its Prometheus-compatible API)
func rulerAlerts(ctx context.Context) ([]map[string]string, error) {
	out, err := kube.Kubectl(ctx, "loki", "get", "--raw", "/api/v1/namespaces/loki/services/loki:3100/proxy/prometheus/api/v1/alerts")
	if err != nil {
		return nil, err
	}
	var res struct {
		Data struct {
			Alerts []struct {
				Labels map[string]string `json:"labels"`
			} `json:"alerts"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return nil, err
	}
	labels := make([]map[string]string, 0, len(res.Data.Alerts))
	for _, a := range res.Data.Alerts {
		labels = append(labels, a.Labels)
	}
	return labels, nil
}

// A new failure in Kyverno's reports alerts once for its top owner, a rollout's new pod keeps the same alert, and a
// pod gone at once still alerts from its admission event (G1,
// docs/superpowers/specs/2026-11-27-policy-report-alert-design.md)
var _ = Describe("PolicyAlerts", Label("sandbox", "policy-alerts"), func() {
	It("alerts once per owner, and for a pod gone at once", func(ctx SpecContext) {
		waitForDeployment(ctx, "kyverno", "kyverno-admission-controller")
		namespace := fmt.Sprint("policy-alert-", time.Now().Unix())
		Expect(kube.CreateNamespace(ctx, namespace)).To(Succeed())
		// The spec deletes it itself at the end; this covers a failure before that
		DeferCleanup(func(ctx SpecContext) {
			out, err := kube.Kubectl(ctx, "", "delete", "namespace", namespace, "--ignore-not-found", "--wait=false")
			Expect(err).NotTo(HaveOccurred(), out)
		}, NodeTimeout(5*time.Minute))

		// fired returns the alerts of that name in the namespace that Prometheus or Loki's ruler holds firing (not
		// Alertmanager's view: it mutes warnings at night)
		fired := func(name string) func() []map[string]string {
			return func() []map[string]string {
				all, err := prometheusQuery(ctx, fmt.Sprintf(`ALERTS{alertname=%q,alertstate="firing"}`, name))
				Expect(err).NotTo(HaveOccurred())
				ruler, err := rulerAlerts(ctx)
				Expect(err).NotTo(HaveOccurred())
				var found []map[string]string
				for _, a := range append(all, ruler...) {
					if a["alertname"] == name && a["namespace"] == namespace {
						found = append(found, a)
					}
				}
				return found
			}
		}
		owner := func(a map[string]string) string { return a["owner_kind"] + "/" + a["owner_name"] + " " + a["policy"] }

		// A Deployment breaking restricted: one alert per policy, all for the Deployment, none per pod or ReplicaSet
		out, err := kube.Kubectl(ctx, namespace, "create", "deployment", "web", "--image=docker.io/library/busybox:1.37.0", "--", "sleep", "3600")
		Expect(err).NotTo(HaveOccurred(), out)
		Eventually(ctx, func() ([]map[string]string, error) {
			return prometheusQuery(ctx, fmt.Sprintf(`kyverno:policy_result:owner{namespace=%q,owner_kind="Deployment",owner_name="web",policy="require-run-as-nonroot"}`, namespace))
		}).WithTimeout(5*time.Minute).WithPolling(15*time.Second).Should(HaveLen(1), "no result for the Deployment")
		var before []string
		Eventually(ctx, func() []string {
			before = nil
			for _, a := range fired("PolicyReportFail")() {
				before = append(before, owner(a))
			}
			return before
		}).WithTimeout(5 * time.Minute).WithPolling(15 * time.Second).Should(ContainElement("Deployment/web require-run-as-nonroot"))
		Expect(before).To(HaveEach(HavePrefix("Deployment/web ")), "an alert named a pod or ReplicaSet")

		// A new pod (a new name) is the same failure
		old, err := kube.Kubectl(ctx, namespace, "get", "pod", "-l", "app=web", "-o", "jsonpath={.items[0].metadata.name}")
		Expect(err).NotTo(HaveOccurred(), old)
		out, err = kube.Kubectl(ctx, namespace, "delete", "pod", old, "--wait=false")
		Expect(err).NotTo(HaveOccurred(), out)
		Eventually(ctx, func() ([]map[string]string, error) {
			return prometheusQuery(ctx, fmt.Sprintf(`kyverno_policyreport_result{namespace=%q,kind="Pod",name!=%q,policy="require-run-as-nonroot"}`, namespace, old))
		}).WithTimeout(5*time.Minute).WithPolling(15*time.Second).ShouldNot(BeEmpty(), "the new pod was not reported")
		Consistently(ctx, func() []string {
			var now []string
			for _, a := range fired("PolicyReportFail")() {
				now = append(now, owner(a))
			}
			return now
		}).WithTimeout(90*time.Second).WithPolling(15*time.Second).Should(ConsistOf(before), "the new pod changed the alerts")

		// A pod that exits at once alerts from its admission event
		out, err = kube.Kubectl(ctx, namespace, "run", "brief", "--image=docker.io/library/busybox:1.37.0", "--restart=Never", "--", "true")
		Expect(err).NotTo(HaveOccurred(), out)
		Eventually(ctx, func() []string {
			var kinds []string
			for _, a := range fired("PolicyViolationEvent")() {
				kinds = append(kinds, a["kind"]+" "+a["policy"])
			}
			return kinds
		}).WithTimeout(5 * time.Minute).WithPolling(15 * time.Second).Should(ContainElement("Pod require-run-as-nonroot"))

		// Gone with its namespace
		Expect(kube.DeleteNamespace(ctx, namespace)).To(Succeed())
		Eventually(ctx, func() ([]map[string]string, error) {
			return prometheusQuery(ctx, fmt.Sprintf(`kyverno_policyreport_result{namespace=%q}`, namespace))
		}).WithTimeout(5*time.Minute).WithPolling(15*time.Second).Should(BeEmpty(), "the reports' series outlived the namespace")
	}, SpecTimeout(25*time.Minute))
})
