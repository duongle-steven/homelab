package test

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const stateBucket = "homelab-production-tfstate"

// fakeS3 is the part of R2's S3 API the s3 backend uses (path style, one bucket): objects, conditional puts for the
// lock file, and ListObjectsV2.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    []string
	url     string
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// /<bucket>/<key>
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	if parts[0] != stateBucket {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `<Error><Code>NoSuchBucket</Code><Message>no such bucket</Message></Error>`)
		return
	}
	key := ""
	if len(parts) == 2 {
		key = parts[1]
	}
	switch {
	case r.Method == http.MethodGet && key == "":
		prefix := r.URL.Query().Get("prefix")
		type content struct{ Key string }
		var result struct {
			XMLName  xml.Name  `xml:"ListBucketResult"`
			Contents []content `xml:"Contents"`
			KeyCount int       `xml:"KeyCount"`
		}
		var keys []string
		for k := range f.objects {
			if strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			result.Contents = append(result.Contents, content{Key: k})
		}
		result.KeyCount = len(keys)
		w.Header().Set("Content-Type", "application/xml")
		_ = xml.NewEncoder(w).Encode(result)
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		body, ok := f.objects[key]
		if !ok {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			if r.Method == http.MethodGet {
				_, _ = io.WriteString(w, `<Error><Code>NoSuchKey</Code><Message>missing</Message></Error>`)
			}
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method == http.MethodGet {
			_, _ = w.Write(body)
		}
	case r.Method == http.MethodPut:
		if _, exists := f.objects[key]; exists && r.Header.Get("If-None-Match") == "*" {
			w.WriteHeader(http.StatusPreconditionFailed)
			_, _ = io.WriteString(w, `<Error><Code>PreconditionFailed</Code><Message>exists</Message></Error>`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.objects[key] = body
		f.puts = append(f.puts, key)
	case r.Method == http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func (f *fakeS3) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for k := range f.objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (f *fakeS3) putKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.puts...)
}

// prodTree is stateTree plus production units ("marker", and "fresh", never applied) on a fake S3, with a stub lab
// (static credentials; ensure records its call) and cloudflare-credentials returning $FAKE_TOKEN.
func prodTree(t *testing.T) (string, *fakeS3, func(unit, token string, extra []string, args ...string) (string, error)) {
	root := stateTree(t)
	write := func(path, body string, mode os.FileMode) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, path), []byte(body), mode))
	}
	write("scripts/cloudflare-credentials", "#!/bin/sh\nprintf '{\"account_id\":\"acc-1\",\"token\":\"%s\"}\\n' \"$FAKE_TOKEN\"\n", 0o755)
	write("bin/lab", `#!/bin/sh
case "$1 $2 $3" in
  "infra state credentials") echo '{"Version":1,"AccessKeyId":"test-key","SecretAccessKey":"test-secret"}' ;;
  "infra state ensure") echo "$*" >> "$(dirname "$0")/ensure.log"; echo "State bucket ready." ;;
  *) echo "stub lab: $*" >&2; exit 1 ;;
esac
`, 0o755)
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
	s3 := &fakeS3{objects: map[string][]byte{}}
	server := httptest.NewServer(s3)
	t.Cleanup(server.Close)
	s3.url = server.URL
	run := func(unit, token string, extra []string, args ...string) (string, error) {
		cmd := exec.Command("terragrunt", append(args, "--non-interactive", "--log-level", "error")...)
		cmd.Dir = filepath.Join(root, "infra/production", unit)
		cmd.Env = append(append(stateEnv(passphraseA, ""), "FAKE_TOKEN="+token, "HOMELAB_STATE_ENDPOINT="+server.URL,
			"HOMELAB_LAB="+filepath.Join(root, "bin/lab")), extra...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	return root, s3, run
}

// generated returns the contents of every file the unit's Terragrunt cache generated under that name
func generated(t *testing.T, root, unit, name string) string {
	matches, err := filepath.Glob(filepath.Join(root, "infra/production", unit, ".terragrunt-cache", "*", "*", "*", name))
	require.NoError(t, err)
	var all strings.Builder
	for _, m := range matches {
		body, err := os.ReadFile(m)
		require.NoError(t, err)
		all.Write(body)
	}
	return all.String()
}

// cf refreshes its OAuth token: the generated backend and credentials config never carry it, so plan needs no init.
// Also proves production needs no .state/ directory.
func TestStateTokenRefresh(t *testing.T) {
	root, s3, run := prodTree(t)
	out, err := run("marker", "token-one", nil, "apply", "-auto-approve")
	require.NoError(t, err, out)
	assert.Equal(t, []string{"marker/tfstate.json"}, s3.keys(), "the state reached the fake bucket")

	backend := generated(t, root, "marker", "backend.tf")
	require.Contains(t, backend, `backend "s3"`)
	for _, file := range []string{"backend.tf", "r2-state.config"} {
		assert.NotContains(t, generated(t, root, "marker", file), "token-one", "%s carries the OAuth token", file)
	}

	out, err = run("marker", "token-two", nil, "plan", "-detailed-exitcode")
	require.NoError(t, err, "plan after a token refresh: %s", out)
	assert.Equal(t, backend, generated(t, root, "marker", "backend.tf"), "backend.tf changed with the token")
}

// The state lands encrypted, and a lock file is written and removed around each command
func TestStateS3Lock(t *testing.T) {
	_, s3, run := prodTree(t)
	out, err := run("marker", "token-one", nil, "apply", "-auto-approve")
	require.NoError(t, err, out)
	assert.Contains(t, s3.putKeys(), "marker/tfstate.json.tflock", "no lock file was written")
	assert.Equal(t, []string{"marker/tfstate.json"}, s3.keys(), "the lock file stayed")
	s3.mu.Lock()
	state := string(s3.objects["marker/tfstate.json"])
	s3.mu.Unlock()
	assert.Contains(t, state, "encrypted_data")
	assert.NotContains(t, state, "visible-marker")
}

// init runs lab infra state ensure with the account and bucket
func TestStateBucketEnsuredOnInit(t *testing.T) {
	root, _, run := prodTree(t)
	out, err := run("marker", "token-one", nil, "init")
	require.NoError(t, err, out)
	log, err := os.ReadFile(filepath.Join(root, "bin/ensure.log"))
	require.NoError(t, err, "ensure did not run")
	assert.Contains(t, string(log), "--account-id acc-1 --bucket "+stateBucket)
}

// A production unit never applied has no object: rotation must not push it, and it names the account it rotates in
func TestStateRotationSkipsMissingObjects(t *testing.T) {
	root, s3, run := prodTree(t)
	out, err := run("marker", "token-one", nil, "apply", "-auto-approve")
	require.NoError(t, err, out)

	cmd := exec.Command("sh", "scripts/rotate-state-passphrase", "production")
	cmd.Dir = root
	cmd.Env = append(stateEnv(passphraseB, passphraseA), "FAKE_TOKEN=token-one", "HOMELAB_STATE_ENDPOINT="+s3.url,
		"HOMELAB_LAB="+filepath.Join(root, "bin/lab"))
	rotated, err := cmd.CombinedOutput()
	require.NoError(t, err, string(rotated))
	assert.Contains(t, string(rotated), "acc-1")
	assert.Contains(t, string(rotated), "production/fresh: no state, skipped")
	assert.Equal(t, []string{"marker/tfstate.json"}, s3.keys(), "an empty state was pushed for the unit never applied")
}
