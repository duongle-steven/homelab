package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
