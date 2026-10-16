package test

import (
	"net/http"
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
	// The repository exists (and is empty) while the migration still clones it, so wait until it has content
	require.Eventually(t, func() bool {
		repo.Empty = true
		status, err := getJSONE("GET", base+"/api/v1/repos/ops/homelab", nil, admin, password, nil, &repo)
		return err == nil && status == http.StatusOK && !repo.Empty
	}, 20*time.Minute, 15*time.Second, "ops/homelab was not seeded")
	assert.True(t, repo.Private)

	// External Secrets creates it once its CRDs and the store are ready
	repoSecret := waitForSecret(t, "argocd", "argocd-repo-forgejo")
	user, pass := string(repoSecret["username"]), string(repoSecret["password"])
	assert.Equal(t, "http://forgejo-http.forgejo:3000/ops/homelab", string(repoSecret["url"]))
	assert.Equal(t, http.StatusOK, getJSON(t, "GET", base+"/api/v1/repos/ops/homelab/branches/master", nil, user, pass, nil, nil))
}

// TestForgejoSeedIsIdempotent runs the seed script again, inside the Forgejo pod, against the seeded
// repository: it must succeed without touching it.
func TestForgejoSeedIsIdempotent(t *testing.T) {
	if os.Getenv("ENV") != "dev" {
		t.Skip("Forgejo tests run against the sandbox only")
	}
	waitForApp(t, "https://"+forgejoHost+"/api/healthz")
	options := k8s.NewKubectlOptions("", "", "forgejo")
	options.Logger = logger.Discard
	script, err := k8s.RunKubectlAndGetOutputE(t, options, "get", "configmap", "forgejo-seed", "-o", `jsonpath={.data.seed\.sh}`)
	require.NoError(t, err)
	require.Contains(t, script, "ops/homelab exists")

	// The admin credentials go through stdin, not the command line
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	stdin := "export FORGEJO_ADMIN_USERNAME=" + quote(secretValue(t, "forgejo", "forgejo-admin", "username")) + "\n" +
		"export FORGEJO_ADMIN_PASSWORD=" + quote(secretValue(t, "forgejo", "forgejo-admin", "password")) + "\n" +
		// Only used if the repository were missing, which fails the assertion below anyway
		"export SEED_REPOSITORY=https://example.invalid/homelab\n" + script

	var out []byte
	ok := assert.Eventually(t, func() bool {
		cmd := exec.Command("kubectl", "--namespace", "forgejo", "exec", "-i", "deploy/forgejo", "--container", "forgejo", "--", "sh", "-s")
		cmd.Stdin = strings.NewReader(stdin)
		out, err = cmd.CombinedOutput()
		return err == nil
	}, 20*time.Minute, 15*time.Second, "seed script failed on its second run")
	if !ok {
		t.Fatalf("last output of the seed script: %v\n%s", err, out)
	}
	assert.Contains(t, string(out), "ops/homelab exists")
	assert.NotContains(t, string(out), "copied")
}
