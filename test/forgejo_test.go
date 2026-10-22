package test

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lab runs the CLI from cli/ (as the Makefile does) and returns its combined output.
func lab(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("go", append([]string{"run", "./cmd/lab"}, args...)...)
	cmd.Dir = "../cli"
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "lab %v:\n%s", args, out)
	return string(out)
}

// forgejoGit runs git against the sandbox's ops/homelab as user with secret (a token or a password), on the node
// and with the sandbox CA, and returns its output and error.
func forgejoGit(t *testing.T, dir, user, secret string, args ...string) (string, error) {
	t.Helper()
	ca := filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(ca, []byte(configMapValue(t, "forgejo", "homelab-dev-ca", "ca.crt")), 0o600))
	askpass := filepath.Join(t.TempDir(), "askpass")
	require.NoError(t, os.WriteFile(askpass, []byte("#!/bin/sh\ncase \"$1\" in Username*) echo \"$GIT_USER\" ;; *) echo \"$GIT_SECRET\" ;; esac\n"), 0o700))
	full := append([]string{"-C", dir, "-c", "credential.helper=", "-c", "http.sslCAInfo=" + ca,
		"-c", "http.curloptResolve=" + forgejoHost + ":443:" + sandboxAddress(t)}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS="+askpass, "GIT_USER="+user, "GIT_SECRET="+secret,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func baoValue(t *testing.T, path, property string) string {
	t.Helper()
	value, err := baoValueE(path, property)
	require.NoError(t, err)
	require.NotEmpty(t, value, "secret/%s#%s", path, property)
	return value
}

// baoValueE reads OpenBao the documented way: an admin login inside OpenBao's pod, the token never printed. Its error
// carries OpenBao's message (never a value): the in-pod script exits with bao kv get's status, revoking the token
// either way
func baoValueE(path, property string) (string, error) {
	script := "kubectl -n openbao create token openbao-admin --audience=openbao --duration=10m | kubectl -n openbao exec -i openbao-0 -c openbao -- " +
		"sh -c 'export BAO_TOKEN=$(bao write -field=token auth/kubernetes/login role=admin jwt=-) && bao kv get -field=" + property + " secret/" + path + "; s=$?; bao token revoke -self >/dev/null 2>&1; exit $s'"
	var stdout, stderr strings.Builder
	cmd := exec.Command("sh", "-c", script)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("secret/%s#%s: %w: %s", path, property, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// permanentBaoError tells a read that cannot succeed later (the path exists without that property) from one that may
// (not pushed yet, OpenBao restarting)
func permanentBaoError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not present in map")
}

// TestForgejoDeploy runs lab infra forgejo sync twice and push, as make infra env=production does, against the
// sandbox's Forgejo, then checks what production relies on.
func TestForgejoDeploy(t *testing.T) {
	if os.Getenv("ENV") != "dev" {
		t.Skip("Forgejo tests run against the sandbox only")
	}
	waitForApp(t, "https://"+forgejoHost+"/api/healthz")
	lab(t, "infra", "forgejo", "sync", "--env", "sandbox")
	deploy, argocd := baoValue(t, "forgejo/deploy", "token"), baoValue(t, "argocd/forgejo", "token")
	require.NotEmpty(t, deploy)
	require.NotEmpty(t, argocd)
	// Woodpecker's OAuth client (docs/superpowers/specs/2026-10-21-woodpecker-design.md, decision 2)
	clientID, clientSecret := baoValue(t, "woodpecker/forgejo", "client_id"), baoValue(t, "woodpecker/forgejo", "client_secret")
	require.NotEmpty(t, clientID)
	require.NotEmpty(t, clientSecret)
	assert.Equal(t, "https://"+ciHost+"/authorize", baoValue(t, "woodpecker/forgejo", "redirect_uri"))
	lab(t, "infra", "forgejo", "sync", "--env", "sandbox")
	assert.Equal(t, deploy, baoValue(t, "forgejo/deploy", "token"), "a second sync replaced deploy's token")
	assert.Equal(t, argocd, baoValue(t, "argocd/forgejo", "token"), "a second sync replaced argocd's token")
	assert.Equal(t, clientID, baoValue(t, "woodpecker/forgejo", "client_id"), "a second sync replaced Woodpecker's client")
	assert.True(t, clientSecret == baoValue(t, "woodpecker/forgejo", "client_secret"), "a second sync replaced Woodpecker's client secret")

	lab(t, "infra", "forgejo", "push", "--env", "sandbox")
	assert.Contains(t, lab(t, "infra", "forgejo", "push", "--env", "sandbox"), "nothing to push")

	// Forgejo's master has the checkout's tree
	clone := t.TempDir()
	out, err := forgejoGit(t, clone, "argocd", argocd, "clone", "--quiet", "https://"+forgejoHost+"/ops/homelab.git", ".")
	require.NoError(t, err, out)
	tree, err := exec.Command("git", "-C", clone, "rev-parse", "HEAD^{tree}").Output()
	require.NoError(t, err)
	head, err := exec.Command("git", "rev-parse", "HEAD^{tree}").Output()
	require.NoError(t, err)
	assert.Equal(t, strings.TrimSpace(string(head)), strings.TrimSpace(string(tree)))

	// The protection: argocd reads but cannot push; deploy can neither force-push nor delete master
	_, err = forgejoGit(t, clone, "argocd", argocd, "commit", "--quiet", "--allow-empty", "-m", "argocd")
	require.NoError(t, err)
	out, err = forgejoGit(t, clone, "argocd", argocd, "push", "--quiet", "origin", "HEAD:refs/heads/master")
	assert.Error(t, err, "argocd pushed to master:\n%s", out)
	orphan, err := forgejoGit(t, clone, "deploy", deploy, "commit-tree", "HEAD^{tree}", "-m", "orphan")
	require.NoError(t, err)
	out, err = forgejoGit(t, clone, "deploy", deploy, "push", "--quiet", "--force", "origin", strings.TrimSpace(orphan)+":refs/heads/master")
	assert.Error(t, err, "deploy force-pushed master:\n%s", out)
	out, err = forgejoGit(t, clone, "deploy", deploy, "push", "--quiet", "origin", ":refs/heads/master")
	assert.Error(t, err, "deploy deleted master:\n%s", out)

	// The allowlist itself: a write collaborator outside it pushes a branch but not master
	admin, adminPassword := secretValue(t, "forgejo", "forgejo-admin", "username"), secretValue(t, "forgejo", "forgejo-admin", "password")
	api := "https://" + forgejoHost + "/api/v1"
	probe, probePassword := "allowlist-probe", fmt.Sprintf("Probe-%d-pw", time.Now().UnixNano())
	never := false
	require.Equal(t, http.StatusCreated, getJSON(t, "POST", api+"/admin/users", nil, admin, adminPassword, map[string]any{
		"username": probe, "email": probe + "@noreply.invalid", "password": probePassword, "must_change_password": &never,
	}, nil))
	t.Cleanup(func() {
		getJSON(t, "DELETE", api+"/admin/users/"+probe+"?purge=true", nil, admin, adminPassword, nil, nil)
	})
	require.Equal(t, http.StatusNoContent, getJSON(t, "PUT", api+"/repos/ops/homelab/collaborators/"+probe, nil, admin, adminPassword,
		map[string]any{"permission": "write"}, nil))
	out, err = forgejoGit(t, clone, probe, probePassword, "push", "--quiet", "origin", "HEAD:refs/heads/"+probe)
	require.NoError(t, err, "a write collaborator could not push a branch:\n%s", out)
	out, err = forgejoGit(t, clone, probe, probePassword, "push", "--quiet", "origin", "HEAD:refs/heads/master")
	assert.Error(t, err, "a write collaborator outside the allowlist pushed master:\n%s", out)
	_, _ = forgejoGit(t, clone, probe, probePassword, "push", "--quiet", "origin", ":refs/heads/"+probe)

	// Site admins are refused too (Forgejo's protection applies to them)
	out, err = forgejoGit(t, clone, admin, adminPassword, "push", "--quiet", "origin", "HEAD:refs/heads/master")
	assert.Error(t, err, "%s (a site admin) pushed master:\n%s", admin, out)

	// Argo CD's credential reads master with the read-only token
	repoSecret := waitForSecret(t, "argocd", "argocd-repo-forgejo")
	assert.Equal(t, "argocd", string(repoSecret["username"]))
	assert.Equal(t, http.StatusOK, getJSON(t, "GET", "https://"+forgejoHost+"/api/v1/repos/ops/homelab/branches/master", nil,
		string(repoSecret["username"]), string(repoSecret["password"]), nil, nil))
}
