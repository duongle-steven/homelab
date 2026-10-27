package test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deployment renders a one-container Deployment; secure sets what Pod Security restricted asks
func deployment(name, image string, secure bool) string {
	security := ""
	if secure {
		security = `
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        seccompProfile:
          type: RuntimeDefault`
	}
	container := ""
	if secure {
		container = `
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: [ALL]`
	}
	return `apiVersion: apps/v1
kind: Deployment
metadata:
  name: ` + name + `
  namespace: fixture
spec:
  selector:
    matchLabels: {app: ` + name + `}
  template:
    metadata:
      labels: {app: ` + name + `}
    spec:` + security + `
      containers:
        - name: app
          image: ` + image + container + "\n"
}

// policy runs Kyverno's policies (system/kyverno) on workloads and fails on any violation, naming it
func TestCheckPolicy(t *testing.T) {
	check := func(workloads string) (string, error) {
		file := filepath.Join(t.TempDir(), "workloads.yaml")
		if err := os.WriteFile(file, []byte(workloads), 0o644); err != nil {
			t.Fatal(err)
		}
		return runCheck(t, "check-policy", "..", "WORKLOADS="+file)
	}
	out, err := check(deployment("good", "ghcr.io/x/app:1.2.3", true))
	assert.NoError(t, err, out)

	for _, c := range []struct{ workload, policy string }{
		{deployment("bare", "ghcr.io/x/app:1.2.3", false), "require-run-as-nonroot"},
		{deployment("unpinned", "ghcr.io/x/app:latest", true), "require-pinned-images"},
		{deployment("untagged", "ghcr.io/x/app", true), "require-pinned-images"},
		{deployment("elsewhere", "evil.example/x/app:1.0", true), "allowed-registries"},
	} {
		out, err := check(c.workload)
		assert.Error(t, err, "%s passed", c.policy)
		assert.True(t, strings.Contains(out, c.policy), "%s not named in:\n%s", c.policy, out)
	}
	// docker.io is implied for a bare name; a digest pins
	out, err = check(deployment("hub", "library/busybox@sha256:"+strings.Repeat("b", 64), true))
	assert.NoError(t, err, out)
}

// privilegedNamespaces are labelled pod-security.kubernetes.io/enforce=privileged (system/argocd/values.yaml's
// templatePatch); every other managed namespace is baseline
var privilegedNamespaces = []string{"istio-system", "rook-ceph", "kured", "loki", "monitoring-system", "csi-hostpath", "tailscale", "wireguard"}

// In the sandbox: Kyverno audits (an unreachable Kyverno blocks nothing), its reports show no repository workload
// failing, a pod breaking restricted is admitted and reported, and Pod Security Admission refuses privileged pods
// outside the namespaces that need them
func TestPolicy(t *testing.T) {
	secretsSandboxOnly(t)
	kyverno := k8s.NewKubectlOptions("", "", "kyverno")
	k8s.WaitUntilDeploymentAvailable(t, kyverno, "kyverno-admission-controller", 60, 10*time.Second)

	t.Run("audit only", func(t *testing.T) {
		out, err := k8s.RunKubectlAndGetOutputE(t, kyverno, "get", "validatingwebhookconfigurations", "-o",
			`jsonpath={range .items[?(@.metadata.labels.webhook\.kyverno\.io/managed-by=="kyverno")]}{range .webhooks[*]}{.failurePolicy}{"\n"}{end}{end}`)
		require.NoError(t, err)
		require.NotEmpty(t, strings.TrimSpace(out), "no Kyverno webhook")
		for _, policy := range strings.Fields(out) {
			assert.Equal(t, "Ignore", policy)
		}
	})

	t.Run("pod security admission", func(t *testing.T) {
		for _, ns := range []string{"excalidraw", "istio-system", "csi-hostpath"} {
			want := "baseline"
			if slices.Contains(privilegedNamespaces, ns) {
				want = "privileged"
			}
			label, err := k8s.RunKubectlAndGetOutputE(t, kyverno, "get", "namespace", ns, "-o",
				`jsonpath={.metadata.labels.pod-security\.kubernetes\.io/enforce}`)
			require.NoError(t, err)
			assert.Equal(t, want, label, ns)
		}
		privileged := `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"privileged","namespace":"excalidraw"},` +
			`"spec":{"containers":[{"name":"c","image":"docker.io/library/busybox:1.37.0","securityContext":{"privileged":true}}]}}`
		cmd := exec.Command("kubectl", "create", "--dry-run=server", "-f", "-")
		cmd.Stdin = strings.NewReader(privileged)
		out, err := cmd.CombinedOutput()
		assert.Error(t, err, "a privileged pod was admitted in excalidraw:\n%s", out)
		assert.Contains(t, string(out), "violates PodSecurity")
	})

	t.Run("reports", func(t *testing.T) {
		// A pod breaking restricted is admitted (Audit) and reported
		namespace := fmt.Sprint("policy-test-", time.Now().Unix())
		opts := k8s.NewKubectlOptions("", "", namespace)
		k8s.CreateNamespace(t, opts, namespace)
		defer k8s.DeleteNamespace(t, opts, namespace)
		k8s.RunKubectl(t, opts, "run", "bare", "--image=docker.io/library/busybox:1.37.0", "--restart=Never", "--", "sleep", "300")
		require.Eventually(t, func() bool {
			out, err := k8s.RunKubectlAndGetOutputE(t, opts, "get", "policyreports.openreports.io,policyreports.wgpolicyk8s.io",
				"-o", `jsonpath={range .items[*].results[?(@.result=="fail")]}{.policy}{"\n"}{end}`)
			return err == nil && strings.Contains(out, "require-run-as-nonroot")
		}, 3*time.Minute, 10*time.Second, "no report of the bare pod's failure")

		// No repository workload fails: Enforce (15b-2) can follow
		out, err := k8s.RunKubectlAndGetOutputE(t, kyverno, "get", "policyreports.openreports.io,policyreports.wgpolicyk8s.io", "-A",
			"-o", `jsonpath={range .items[*]}{.metadata.namespace}{" "}{range .results[?(@.result=="fail")]}{.policy}{","}{end}{"\n"}{end}`)
		require.NoError(t, err)
		var failing []string
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && !strings.HasPrefix(fields[0], "policy-test-") {
				failing = append(failing, line)
			}
		}
		assert.Empty(t, failing, "workloads failing Kyverno's policies (a fix or a PolicyException is missing)")
	})
}
