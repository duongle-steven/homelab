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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeR2 serves OpenTofu's http backend like R2's object API, accepting only the current Bearer token.
type fakeR2 struct {
	mu      sync.Mutex
	token   string
	objects map[string][]byte
	url     string
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

// prodTree is stateTree plus production units ("marker", and "fresh", never applied) whose http backend is a fake R2
// accepting only the current token; run calls terragrunt in a production unit with that token.
func prodTree(t *testing.T) (string, *fakeR2, func(unit, token string, extra []string, args ...string) (string, error)) {
	root := stateTree(t)
	write := func(path, body string, mode os.FileMode) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, path), []byte(body), mode))
	}
	write("scripts/cloudflare-credentials", "#!/bin/sh\nprintf '{\"account_id\":\"acc-1\",\"token\":\"%s\"}\\n' \"$FAKE_TOKEN\"\n", 0o755)
	write("scripts/ensure-state-bucket", "#!/bin/sh\nexit 0\n", 0o755)
	for _, unit := range []string{"marker", "fresh"} {
		module := map[string]string{"marker": "marker", "fresh": "empty"}[unit]
		write("infra/production/"+unit+"/terragrunt.hcl", `include "root" {
  path = find_in_parent_folders("root.hcl")
}
terraform {
  source = "${find_in_parent_folders("_modules")}//`+module+`"
}
`, 0o644)
	}
	r2 := &fakeR2{token: "token-one", objects: map[string][]byte{}}
	server := httptest.NewServer(r2)
	t.Cleanup(server.Close)
	r2.url = server.URL
	run := func(unit, token string, extra []string, args ...string) (string, error) {
		cmd := exec.Command("terragrunt", append(args, "--non-interactive", "--log-level", "error")...)
		cmd.Dir = filepath.Join(root, "infra/production", unit)
		cmd.Env = append(append(stateEnv(passphraseA, ""), "FAKE_TOKEN="+token, "HOMELAB_STATE_API="+server.URL), extra...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	return root, r2, run
}

func (f *fakeR2) setToken(token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.token = token
}

func (f *fakeR2) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for k := range f.objects {
		keys = append(keys, k)
	}
	return keys
}

// cf refreshes its OAuth token, so production's backend header changes between runs: plan must still work (Terragrunt
// re-initialises when backend.tf changes, with -reconfigure). Also proves production needs no .state/ directory.
func TestStateTokenRefresh(t *testing.T) {
	_, r2, run := prodTree(t)
	out, err := run("marker", "token-one", nil, "apply", "-auto-approve")
	require.NoError(t, err, out)
	require.Len(t, r2.keys(), 1, "the state reached the fake R2")

	r2.setToken("token-two")
	out, err = run("marker", "token-two", nil, "plan", "-detailed-exitcode")
	require.NoError(t, err, "plan after a token refresh: %s", out)

	// A migration run on a .terraform initialised with another token still reconfigures
	r2.setToken("token-three")
	out, err = run("marker", "token-three", []string{"HOMELAB_STATE_MIGRATE=1"}, "plan", "-detailed-exitcode")
	require.NoError(t, err, "migration plan after a token refresh: %s", out)
}

// A production unit never applied has no object, which the http backend reads as an empty state: rotation must not
// push it, and it names the account it rotates in
func TestStateRotationSkipsMissingObjects(t *testing.T) {
	root, r2, run := prodTree(t)
	out, err := run("marker", "token-one", nil, "apply", "-auto-approve")
	require.NoError(t, err, out)

	cmd := exec.Command("sh", "scripts/rotate-state-passphrase", "production")
	cmd.Dir = root
	cmd.Env = append(stateEnv(passphraseB, passphraseA), "FAKE_TOKEN=token-one", "HOMELAB_STATE_API="+r2.url)
	rotated, err := cmd.CombinedOutput()
	require.NoError(t, err, string(rotated))
	assert.Contains(t, string(rotated), "acc-1")
	assert.Contains(t, string(rotated), "production/fresh: no state, skipped")
	assert.Len(t, r2.keys(), 1, "an empty state was pushed for the unit never applied")
}
