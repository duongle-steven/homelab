package offline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
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
		return runCheck(t, "check-policy", repo.Root(), "WORKLOADS="+file)
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
	// An exception excuses its namespace's workload only: the same name elsewhere still fails
	exception := `apiVersion: policies.kyverno.io/v1
kind: PolicyException
metadata:
  name: pod-security
  namespace: fixture
  annotations:
    argocd.argoproj.io/sync-wave: "-1"
spec:
  policyRefs:
    - {name: require-run-as-nonroot, kind: ValidatingPolicy}
  matchConditions:
    - name: rooted
      expression: "object.metadata.namespace == 'fixture' && object.metadata.name.startsWith('rooted')"
`
	rooted := deployment("rooted", "ghcr.io/x/app:1.2.3", false)
	out, err = check(rooted + "---\n" + exception)
	assert.Error(t, err, "the bare deployment still breaks policies the exception does not name")
	assert.NotContains(t, out, "require-run-as-nonroot", "the exception did not excuse its workload")
	elsewhere := strings.Replace(rooted, "namespace: fixture", "namespace: other", 1)
	out, _ = check(elsewhere + "---\n" + exception)
	assert.Contains(t, out, "require-run-as-nonroot Deployment/other/rooted", "an exception excused another namespace's workload")

	// docker.io is implied for a bare name; a digest pins
	out, err = check(deployment("hub", "library/busybox@sha256:"+strings.Repeat("b", 64), true))
	assert.NoError(t, err, out)
}

// exception renders a PolicyException in namespace fixture, with its sync wave when wave is not empty
func exception(wave string) string {
	annotations := ""
	if wave != "" {
		annotations = "\n  annotations:\n    argocd.argoproj.io/sync-wave: \"" + wave + "\""
	}
	return `apiVersion: policies.kyverno.io/v1
kind: PolicyException
metadata:
  name: waved
  namespace: fixture` + annotations + `
spec:
  policyRefs:
    - {name: require-run-as-nonroot, kind: ValidatingPolicy}
  matchConditions:
    - name: none
      expression: "false"
`
}

// Every PolicyException applies before the workloads it excuses (a sync wave below theirs): Argo CD applies kinds it
// does not know last in a wave, so pods admitted before their exception would keep a false fail until the next
// background scan (docs/superpowers/specs/2026-11-27-policy-report-alert-design.md)
func TestCheckPolicyExceptionWaves(t *testing.T) {
	check := func(resources string) (string, error) {
		file := filepath.Join(t.TempDir(), "workloads.yaml")
		if err := os.WriteFile(file, []byte(resources), 0o644); err != nil {
			t.Fatal(err)
		}
		return runCheck(t, "check-policy", repo.Root(), "WORKLOADS="+file)
	}
	good := deployment("good", "ghcr.io/x/app:1.2.3", true)
	t.Run("exception wave below workloads", func(t *testing.T) {
		out, err := check(good + "---\n" + exception("-1"))
		assert.NoError(t, err, out)
	})
	t.Run("exception without wave", func(t *testing.T) {
		out, err := check(good + "---\n" + exception(""))
		assert.Error(t, err, out)
		assert.Contains(t, out, "wave: ")
		assert.Contains(t, out, "PolicyException fixture/waved")
	})
	t.Run("exception wave below a negative workload wave", func(t *testing.T) {
		early := strings.Replace(good, "  namespace: fixture\n", "  namespace: fixture\n  annotations:\n    argocd.argoproj.io/sync-wave: \"-2\"\n", 1)
		out, err := check(early + "---\n" + exception("-1"))
		assert.Error(t, err, out)
		assert.Contains(t, out, "PolicyException fixture/waved at -1, workloads from -2")
	})
	// An exception for another namespace's workloads (k3s's own, in kube-system) is not in this render's sync
	t.Run("exception for another namespace", func(t *testing.T) {
		other := strings.Replace(exception("1"), "namespace: fixture", "namespace: kube-system", 1)
		out, err := check(good + "---\n" + other)
		assert.NoError(t, err, out)
	})
	t.Run("exception alone", func(t *testing.T) {
		out, err := check(exception("-1"))
		assert.NoError(t, err, out)
		out, err = check(exception(""))
		assert.Error(t, err, out)
		assert.Contains(t, out, "PolicyException fixture/waved at 0, workloads from 0")
	})
}
