package offline

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clusterSecret runs cluster-secret.sh with env and returns its output
func clusterSecret(t *testing.T, env ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", repo.Root()+"/infra/_modules/argocd/cluster-secret.sh")
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	return string(out), err
}

func yqOn(t *testing.T, doc, expr string) string {
	t.Helper()
	cmd := exec.Command("yq", "-o=json", "-I=0", expr)
	cmd.Stdin = strings.NewReader(doc)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return strings.TrimSpace(string(out))
}

// The bootstrap's cluster Secret carries what the ApplicationSets read: every annotation always present, one label per
// optional app asked
func TestArgoCDClusterSecret(t *testing.T) {
	out, err := clusterSecret(t, "REPOSITORY=https://github.com/example/homelab", "BRANCH=sandbox", "OPTIONAL_APPS=paperless paperless")
	require.NoError(t, err)
	assert.Equal(t, `"Secret"`, yqOn(t, out, ".kind"))
	assert.Equal(t, `{"name":"in-cluster","namespace":"argocd"}`, yqOn(t, out, `.metadata | {"name": .name, "namespace": .namespace}`))
	assert.JSONEq(t, `{"argocd.argoproj.io/secret-type":"cluster","homelab/bootstrap":"true","homelab/app-paperless":"true"}`,
		yqOn(t, out, ".metadata.labels"))
	assert.JSONEq(t, `{"repository":"https://github.com/example/homelab","revision":"sandbox","backupS3Url":""}`,
		yqOn(t, out, ".metadata.annotations"))
	assert.JSONEq(t, `{"name":"in-cluster","server":"https://kubernetes.default.svc"}`, yqOn(t, out, ".stringData"))

	out, err = clusterSecret(t, "REPOSITORY=r", "BRANCH=sandbox", "REVISION=abc123", "BACKUP_S3_URL=https://s3.example")
	require.NoError(t, err)
	assert.Equal(t, `"abc123"`, yqOn(t, out, ".metadata.annotations.revision"))
	assert.Equal(t, `"https://s3.example"`, yqOn(t, out, ".metadata.annotations.backupS3Url"))
	assert.Equal(t, `[]`, yqOn(t, out, `[.metadata.labels | keys | .[] | select(test("^homelab/app-"))]`))

	_, err = clusterSecret(t, "BRANCH=sandbox")
	assert.Error(t, err, "no REPOSITORY")
	_, err = clusterSecret(t, "REPOSITORY=r")
	assert.Error(t, err, "no BRANCH")
}

// On a new cluster the ApplicationSets' generators match nothing until the Secret exists, and re-applying them before it
// with a changed selector would delete every Application: the bootstrap applies the Secret before anything else
func TestArgoCDBootstrapAppliesSecretFirst(t *testing.T) {
	dir, env := fakeBin(t, map[string]string{"kubectl": `case "$*" in
  *"create namespace"*) printf 'apiVersion: v1\nkind: Namespace\nmetadata: {name: argocd}\n' ;;
  *wait*) ;;
  *apply*) yq -N '.kind + "/" + .metadata.name' >> "$FIXTURES/applied" ;;
  *) exit 1 ;;
esac`})
	root, err := filepath.Abs(repo.Root())
	require.NoError(t, err)
	cmd := exec.Command("sh", root+"/infra/_modules/argocd/bootstrap.sh")
	cmd.Env = append(env, "CHART="+root+"/system/argocd", "VALUES=values-dev.yaml", "ENVIRONMENT=dev",
		"REPOSITORY=https://github.com/example/homelab", "BRANCH=sandbox", "KUBECONFIG=/dev/null")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	raw, err := os.ReadFile(filepath.Join(dir, "applied"))
	require.NoError(t, err)
	applied := strings.Fields(string(raw))
	secret := -1
	for i, object := range applied {
		switch {
		case object == "Secret/in-cluster" && secret < 0:
			secret = i
		case strings.HasPrefix(object, "CustomResourceDefinition/"), strings.HasPrefix(object, "ApplicationSet/"):
			require.GreaterOrEqual(t, secret, 0, "%s applied before Secret/in-cluster", object)
		}
	}
	require.GreaterOrEqual(t, secret, 0, "Secret/in-cluster never applied: %v", applied)
}
