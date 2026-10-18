package test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeR2 serves OpenTofu's http backend like R2's object API, accepting only the current Bearer token.
type fakeR2 struct {
	mu      sync.Mutex
	token   string
	objects map[string][]byte
}

func (f *fakeR2) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		http.Error(w, "stale token", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		body, ok := f.objects[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	case http.MethodPut, http.MethodPost:
		body, _ := io.ReadAll(r.Body)
		f.objects[r.URL.Path] = body
	case http.MethodDelete:
		delete(f.objects, r.URL.Path)
	default:
		http.Error(w, "unsupported", http.StatusMethodNotAllowed)
	}
}

// cf refreshes its OAuth token, so production's backend header changes between runs: plan must still work (Terragrunt
// re-initialises when backend.tf changes, with -reconfigure). Also proves production needs no .state/ directory.
func TestStateTokenRefresh(t *testing.T) {
	root := stateTree(t)
	write := func(path, body string, mode os.FileMode) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, path), []byte(body), mode))
	}
	write("scripts/cloudflare-credentials", "#!/bin/sh\nprintf '{\"account_id\":\"acc-1\",\"token\":\"%s\"}\\n' \"$FAKE_TOKEN\"\n", 0o755)
	write("scripts/ensure-state-bucket", "#!/bin/sh\nexit 0\n", 0o755)
	write("infra/production/marker/terragrunt.hcl", `include "root" {
  path = find_in_parent_folders("root.hcl")
}
terraform {
  source = "${find_in_parent_folders("_modules")}//marker"
}
`, 0o644)

	r2 := &fakeR2{token: "token-one", objects: map[string][]byte{}}
	server := httptest.NewServer(r2)
	defer server.Close()

	run := func(token string, args ...string) (string, error) {
		cmd := exec.Command("terragrunt", append(args, "--non-interactive", "--log-level", "error")...)
		cmd.Dir = filepath.Join(root, "infra/production/marker")
		cmd.Env = append(stateEnv(passphraseA, ""), "FAKE_TOKEN="+token, "HOMELAB_STATE_API="+server.URL)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err := run("token-one", "apply", "-auto-approve")
	require.NoError(t, err, out)
	require.Len(t, r2.objects, 1, "the state reached the fake R2")

	r2.mu.Lock()
	r2.token = "token-two"
	r2.mu.Unlock()
	out, err = run("token-two", "plan", "-detailed-exitcode")
	require.NoError(t, err, "plan after a token refresh: %s", out)
}
