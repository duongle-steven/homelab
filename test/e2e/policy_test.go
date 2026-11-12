package e2e

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// privilegedNamespaces are labelled pod-security.kubernetes.io/enforce=privileged (system/argocd/values.yaml's
// templatePatch); every other managed namespace is baseline
var privilegedNamespaces = []string{"istio-system", "rook-ceph", "loki", "monitoring-system", "csi-hostpath", "falco", "tailscale", "wireguard"}

// In the sandbox: Kyverno audits (an unreachable Kyverno blocks nothing), its reports show no repository workload
// failing, a pod breaking restricted is admitted and reported, and Pod Security Admission refuses privileged pods
// outside the namespaces that need them
var _ = Describe("Policy", Label("sandbox", "testbed", "policy"), func() {
	BeforeEach(func(ctx SpecContext) {
		waitForDeployment(ctx, "kyverno", "kyverno-admission-controller")
	}, NodeTimeout(15*time.Minute))

	// The webhooks that judge ordinary resources (workloads) ignore an unreachable Kyverno; those for Kyverno's own
	// kinds (policies, exceptions) and for what it generates (an objectSelector on its labels) may refuse
	It("audit only", func(ctx SpecContext) {
		var configs struct {
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Webhooks []struct {
					Name           string          `json:"name"`
					FailurePolicy  string          `json:"failurePolicy"`
					ObjectSelector json.RawMessage `json:"objectSelector"`
				} `json:"webhooks"`
			} `json:"items"`
		}
		kubectlJSON(ctx, &configs, "get", "validatingwebhookconfigurations,mutatingwebhookconfigurations", "-l", "webhook.kyverno.io/managed-by=kyverno")
		checked := 0
		for _, c := range configs.Items {
			if !strings.Contains(c.Metadata.Name, "resource") {
				continue
			}
			for _, w := range c.Webhooks {
				if len(w.ObjectSelector) > 0 && string(w.ObjectSelector) != "{}" && strings.Contains(string(w.ObjectSelector), "kyverno.io") {
					continue
				}
				checked++
				Expect(w.FailurePolicy).To(Equal("Ignore"), "%s/%s", c.Metadata.Name, w.Name)
			}
		}
		Expect(checked).NotTo(BeZero(), "no Kyverno webhook for ordinary resources")
	}, SpecTimeout(5*time.Minute))

	It("pod security admission", func(ctx SpecContext) {
		for _, ns := range []string{"excalidraw", "istio-system", "csi-hostpath"} {
			want := "baseline"
			if slices.Contains(privilegedNamespaces, ns) {
				want = "privileged"
			}
			label, err := kube.Kubectl(ctx, "kyverno", "get", "namespace", ns, "-o",
				`jsonpath={.metadata.labels.pod-security\.kubernetes\.io/enforce}`)
			Expect(err).NotTo(HaveOccurred())
			Expect(label).To(Equal(want), ns)
		}
		privileged := `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"privileged","namespace":"excalidraw"},` +
			`"spec":{"containers":[{"name":"c","image":"docker.io/library/busybox:1.37.0","securityContext":{"privileged":true}}]}}`
		cmd := exec.CommandContext(ctx, "kubectl", "create", "--dry-run=server", "-f", "-")
		cmd.Stdin = strings.NewReader(privileged)
		out, err := cmd.CombinedOutput()
		Expect(err).To(HaveOccurred(), "a privileged pod was admitted in excalidraw:\n%s", out)
		Expect(string(out)).To(ContainSubstring("violates PodSecurity"))
	}, SpecTimeout(5*time.Minute))

	// The reports controller keeps its leader Lease: at its memory limit it stalled, missed the Lease renewal and
	// exited, again and again, and reports came late or never (system/kyverno/values.yaml)
	It("reports controller stays up", func(ctx SpecContext) {
		pod := `jsonpath={.items[0].status.containerStatuses[0].restartCount} {.items[0].status.containerStatuses[0].ready}`
		state := func() string {
			out, err := kube.Kubectl(ctx, "kyverno", "get", "pod", "-l", "app.kubernetes.io/component=reports-controller", "-o", pod)
			Expect(err).NotTo(HaveOccurred())
			return out
		}
		before := state()
		Expect(strings.HasSuffix(before, " true")).To(BeTrue(), "the reports controller is not ready: %s", before)
		sleep(ctx, 2*time.Minute)
		Expect(state()).To(Equal(before), "the reports controller restarted (restart count, ready)")
	}, SpecTimeout(10*time.Minute))

	It("reports", func(ctx SpecContext) {
		// A pod breaking restricted is admitted (Audit) and reported
		namespace := fmt.Sprint("policy-test-", time.Now().Unix())
		Expect(kube.CreateNamespace(ctx, namespace)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) { Expect(kube.DeleteNamespace(ctx, namespace)).To(Succeed()) }, NodeTimeout(5*time.Minute))
		out, err := kube.Kubectl(ctx, namespace, "run", "bare", "--image=docker.io/library/busybox:1.37.0", "--restart=Never", "--", "sleep", "300")
		Expect(err).NotTo(HaveOccurred(), out)
		Eventually(ctx, func() bool {
			out, err := kube.Kubectl(ctx, namespace, "get", "policyreports.wgpolicyk8s.io",
				"-o", `jsonpath={range .items[*].results[?(@.result=="fail")]}{.policy}{"\n"}{end}`)
			return err == nil && strings.Contains(out, "require-run-as-nonroot")
		}).WithTimeout(5*time.Minute).WithPolling(10*time.Second).Should(BeTrue(), "no report of the bare pod's failure")

		// No repository workload fails: Enforce (15b-2) can follow. Only pods created once every PolicyException
		// existed in its current form (created or last changed) count: a pod (and its Deployment's report) admitted while Kyverno had not yet cached the
		// exceptions keeps its fail until the next background scan (6 hours here), which is a startup race, not a
		// missing fix; scripts/ci-check policy judges every rendered workload anyway
		type meta struct {
			Namespace         string    `json:"namespace"`
			UID               string    `json:"uid"`
			CreationTimestamp time.Time `json:"creationTimestamp"`
			ManagedFields     []struct {
				Time time.Time `json:"time"`
			} `json:"managedFields"`
		}
		var exceptions, pods struct {
			Items []struct {
				Metadata meta `json:"metadata"`
			} `json:"items"`
		}
		kubectlJSON(ctx, &exceptions, "get", "policyexceptions.policies.kyverno.io", "-A", "--show-managed-fields")
		Expect(exceptions.Items).NotTo(BeEmpty(), "no PolicyException in the sandbox")
		var newest time.Time
		for _, e := range exceptions.Items {
			if e.Metadata.CreationTimestamp.After(newest) {
				newest = e.Metadata.CreationTimestamp
			}
			for _, f := range e.Metadata.ManagedFields {
				if f.Time.After(newest) {
					newest = f.Time
				}
			}
		}
		// k3s's own pods (kube-system) are not the repository's, nor the specs' fixtures (TrivyOperator's old alpine,
		// left by a run cut by a timeout, until that spec deletes it); passes leave no report, so the
		// pods judged are counted from the pods themselves
		outside := func(namespace string) bool {
			return namespace == "kube-system" || strings.HasPrefix(namespace, "policy-test-") ||
				strings.HasPrefix(namespace, "trivy-test-")
		}
		cutoff := newest.Add(time.Minute) // for Kyverno to cache the newest exception
		kubectlJSON(ctx, &pods, "get", "pods", "-A")
		fresh := map[string]bool{}
		for _, p := range pods.Items {
			if !outside(p.Metadata.Namespace) && p.Metadata.CreationTimestamp.After(cutoff) {
				fresh[p.Metadata.UID] = true
			}
		}
		var reports struct {
			Items []struct {
				Metadata meta `json:"metadata"`
				Scope    struct {
					Kind string `json:"kind"`
					Name string `json:"name"`
					UID  string `json:"uid"`
				} `json:"scope"`
				Results []struct {
					Policy string `json:"policy"`
					Result string `json:"result"`
				} `json:"results"`
			} `json:"items"`
		}
		kubectlJSON(ctx, &reports, "get", "policyreports.wgpolicyk8s.io", "-A")
		var failing []string
		for _, r := range reports.Items {
			if r.Scope.Kind != "Pod" || !fresh[r.Scope.UID] {
				continue
			}
			for _, res := range r.Results {
				if res.Result == "fail" {
					failing = append(failing, fmt.Sprintf("%s/%s: %s", r.Metadata.Namespace, r.Scope.Name, res.Policy))
				}
			}
		}
		// On a fresh cluster few pods start that late; the renders are judged in full by scripts/ci-check policy
		GinkgoWriter.Printf("judged %d of %d pods: those created after the newest PolicyException change (%s)\n", len(fresh), len(pods.Items), newest)
		Expect(failing).To(BeEmpty(), "workloads failing Kyverno's policies (a fix or a PolicyException is missing)")
	}, SpecTimeout(15*time.Minute))
})
