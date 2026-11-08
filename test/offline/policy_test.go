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
