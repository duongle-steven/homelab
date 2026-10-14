package test

import (
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestForgejoSeed checks the seed Job copied the GitOps repository and that Argo CD's repository
// credential reads it, as production Argo CD will.
func TestForgejoSeed(t *testing.T) {
	if os.Getenv("ENV") != "dev" {
		t.Skip("Forgejo tests run against the sandbox only")
	}
	base := "https://" + forgejoHost
	waitForApp(t, base+"/api/healthz")
	admin := secretValue(t, "forgejo", "forgejo-admin", "username")
	password := secretValue(t, "forgejo", "forgejo-admin", "password")

	var repo struct {
		Private bool `json:"private"`
		Empty   bool `json:"empty"`
	}
	require.Eventually(t, func() bool {
		return getJSON(t, "GET", base+"/api/v1/repos/ops/homelab", nil, admin, password, nil, &repo) == http.StatusOK
	}, 20*time.Minute, 15*time.Second, "ops/homelab was not seeded")
	assert.True(t, repo.Private)
	assert.False(t, repo.Empty)

	user := secretValue(t, "argocd", "argocd-repo-forgejo", "username")
	pass := secretValue(t, "argocd", "argocd-repo-forgejo", "password")
	assert.Equal(t, "http://forgejo-http.forgejo:3000/ops/homelab", secretValue(t, "argocd", "argocd-repo-forgejo", "url"))
	assert.Equal(t, http.StatusOK, getJSON(t, "GET", base+"/api/v1/repos/ops/homelab/branches/master", nil, user, pass, nil, nil))
}

// TestForgejoSeedIsIdempotent runs the seed script again against the seeded repository: it must succeed
// without touching it.
func TestForgejoSeedIsIdempotent(t *testing.T) {
	if os.Getenv("ENV") != "dev" {
		t.Skip("Forgejo tests run against the sandbox only")
	}
	options := k8s.NewKubectlOptions("", "", "forgejo")
	job, err := k8s.RunKubectlAndGetOutputE(t, options, "get", "jobs", "-o", "jsonpath={.items[0].metadata.name}")
	require.NoError(t, err)
	require.NotEmpty(t, job)
	rerun := job + "-rerun"
	_, err = k8s.RunKubectlAndGetOutputE(t, options, "create", "job", "--from=job/"+job, rerun)
	require.NoError(t, err)
	defer k8s.RunKubectlE(t, options, "delete", "job", rerun)
	_, err = k8s.RunKubectlAndGetOutputE(t, options, "wait", "--for=condition=complete", "--timeout=5m", "job/"+rerun)
	require.NoError(t, err)
	logs, err := k8s.RunKubectlAndGetOutputE(t, options, "logs", "job/"+rerun)
	require.NoError(t, err)
	assert.Contains(t, logs, "ops/homelab exists")
}
