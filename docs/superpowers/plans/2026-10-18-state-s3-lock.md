# Step 8d: State through R2's S3 API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Production's OpenTofu state uses the `s3` backend on R2's S3 endpoint with a lock file, its credentials from
`lab infra state credentials` (ported from upstream `nixos`), so no token sits in `backend.tf`.

**Architecture:** `cli/internal/state` and `cli/internal/cli/{cloudflare,infra_state}.go` come from upstream with
their tests. `infra/root.hcl` generates an `s3` backend plus an `r2-state.config` whose `credential_process` runs
`lab`; `lab infra state ensure` replaces `scripts/ensure-state-bucket`. Offline tests run OpenTofu against a fake S3
server with a stub `lab`.

**Tech Stack:** Go 1.26, cobra v1.10.2, github.com/cloudflare/cloudflare-go v0.119.0, github.com/aws/aws-sdk-go-v2
v1.47.3, OpenTofu 1.13 (`s3` backend, `use_lockfile`), Terragrunt.

**Spec:** `docs/superpowers/specs/2026-10-18-state-s3-lock-design.md`

## Global Constraints

- Branch `feat/state-s3-lock` (stacked on `feat/test-scopes`); never commit to `dev` or `master`.
- Imports: `github.com/duongle-steven/homelab/cli/...` (upstream's `github.com/khuedoan/homelab/cli/...` rewritten).
- Latest stable pins: cloudflare-go v0.119.0, aws-sdk-go-v2 v1.47.3 (upstream pins v0.115.0 and v1.39.2).
- The sandbox's backend stays `local`; production's becomes `s3` with `use_lockfile = true`, `use_path_style = true`,
  key `<unit>/tfstate.json`, bucket `homelab-production-tfstate`.
- `lab` runs as `go -C <repo>/cli run ./cmd/lab`; `HOMELAB_LAB` overrides it, `HOMELAB_STATE_ENDPOINT` overrides
  `https://<account>.r2.cloudflarestorage.com` (both for tests).
- Never print the OAuth token, R2 credentials or the state passphrase. Touching production state (Task 3) needs the
  user's go-ahead.

## Review Focus

1. **`credential_process` and a multi-word `lab` command:** the AWS SDK runs it through a shell, so
   `go -C … run ./cmd/lab …` must work. Pinned by Task 2's tests, whose stub is a script path, and by Task 3's real run.
2. **A missing object under `s3`:** rotation must still skip the unit (no object = empty pull). Pinned in Task 2
   (`TestStateRotationSkipsMissingObjects`).
3. **A stale lock after an interrupted command:** the lock key is `<unit>/tfstate.json.tflock`; the docs name
   `force-unlock`. Pinned in Task 2 (`TestStateS3Lock` sees the lock written and removed).
4. **The token in generated files:** neither `backend.tf` nor `r2-state.config` may contain the OAuth token. Pinned in
   Task 2 (`TestStateTokenRefresh`).
5. **`make -C infra check` without a login:** parsing must not run `lab` or `cf` (`HOMELAB_CLOUDFLARE_OFFLINE=1` keeps
   placeholders). Pinned in Task 2, Step 7.

---

### Task 1: Port upstream's state commands

**Files:**
- Create: `cli/internal/state/r2.go`, `cli/internal/state/r2_test.go`, `cli/internal/state/credentials_test.go`,
  `cli/internal/state/r2_activation_test.go`
- Create: `cli/internal/cli/cloudflare.go`, `cli/internal/cli/cloudflare_test.go`, `cli/internal/cli/infra_state.go`
- Modify: `cli/internal/cli/root.go` (add the `infra` command), `cli/go.mod`, `cli/go.sum`

**Interfaces:**
- Produces: `lab infra state ensure --account-id <id> --bucket <name>` (prints `State bucket <name> is ready.`) and
  `lab infra state credentials --account-id <id> --bucket <name>` (prints credential-process JSON:
  `{"Version":1,"AccessKeyId":…,"SecretAccessKey":…,"Expiration":…}`).

- [ ] **Step 1: Copy upstream's tests first (they fail to build)**

```bash
mkdir -p cli/internal/state
for f in cli/internal/state/r2_test.go cli/internal/state/credentials_test.go cli/internal/state/r2_activation_test.go cli/internal/cli/cloudflare_test.go; do
  git show upstream/nixos:$f | sed 's|github.com/khuedoan/homelab/cli|github.com/duongle-steven/homelab/cli|g' > $f
done
```

- [ ] **Step 2: Run them to see them fail**

Run: `direnv exec . env -C cli go test ./internal/state/ ./internal/cli/ 2>&1 | grep -v direnv | head -5`
Expected: build failures: missing module `github.com/cloudflare/cloudflare-go`, undefined `CreateR2Credentials`,
`cloudflareLogin`.

- [ ] **Step 3: Copy upstream's code and wire the command**

```bash
for f in cli/internal/state/r2.go cli/internal/cli/cloudflare.go cli/internal/cli/infra_state.go; do
  git show upstream/nixos:$f | sed 's|github.com/khuedoan/homelab/cli|github.com/duongle-steven/homelab/cli|g' > $f
done
```

In `cli/internal/cli/root.go`, after `root.AddCommand(newTestCmd())`, add:

```go
	infra := &cobra.Command{Use: "infra", Short: "Manage infrastructure prerequisites"}
	infra.AddCommand(newInfraStateCmd())
	root.AddCommand(infra)
```

and set `Example` to `"  lab test scope --base origin/dev\n  lab infra state ensure --bucket homelab-production-tfstate"`.

In `cli/internal/cli/cloudflare.go`, extend upstream's two login errors with the consent-page hint: the
`"cf OAuth login required; run cf auth login"` and `"read cf OAuth login failed; run cf auth login"` messages end with
`" (enable Account API Tokens on the consent page)"`. In `cli/internal/state/r2.go` the "cannot grant R2 object access"
error becomes `"cf OAuth login cannot grant R2 object access; run cf auth login --force and enable Account API Tokens on the consent page"`.
Adjust the ported tests only where they assert those exact strings.

- [ ] **Step 4: Pin the dependencies and run the tests**

Run: `direnv exec . env -C cli go get github.com/cloudflare/cloudflare-go@v0.119.0 github.com/aws/aws-sdk-go-v2@v1.47.3 2>&1 | tail -2; direnv exec . env -C cli go mod tidy; direnv exec . make -C cli test 2>&1 | grep -vE 'direnv|make:' | tail -5`
Expected: `ok` for `internal/cli`, `internal/scope`, `internal/state`; `go vet` silent. If v0.119.0 changed an API the
port uses (a compile error), fix the call to the new signature and ledger a ruling; do not pin an older version.

- [ ] **Step 5: Commit**

```bash
git add cli
git commit -m "feat(cli): lab infra state ensure and credentials, ported from upstream nixos"
```

---

### Task 2: The `s3` backend with a lock

**Files:**
- Modify: `infra/root.hcl` (locals, `generate "backend"`, new `generate "r2_credentials"`, the `state_bucket` hook)
- Delete: `scripts/ensure-state-bucket`; `TestStateBucket` in `test/state_scripts_test.go`
- Modify: `test/state_token_refresh_test.go` (fake S3 instead of fake R2), `test/scopes.yaml` (drop
  `scripts/ensure-state-bucket` from `skip`)
- Test: `test/state_token_refresh_test.go` (`TestStateTokenRefresh`, `TestStateRotationSkipsMissingObjects`,
  `TestStateS3Lock`, `TestStateBucketEnsuredOnInit`)

**Interfaces:**
- Consumes: `lab infra state ensure|credentials` (Task 1).
- Produces: production's generated `backend.tf` (`s3`) and `r2-state.config`; env overrides `HOMELAB_LAB`,
  `HOMELAB_STATE_ENDPOINT`.

- [ ] **Step 1: Replace the fake R2 with a fake S3 and write the tests**

Replace the `fakeR2` type, `prodTree` and the four production tests in `test/state_token_refresh_test.go` with:

```go
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

// fakeS3 is the part of R2's S3 API the s3 backend uses (path style): objects, conditional puts for the lock file,
// and ListObjectsV2.
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
	assert.Equal(t, []string{"homelab-production-tfstate/marker/tfstate.json"}, prefixed(s3.keys(), "homelab-production-tfstate/"), "the state reached the fake bucket")

	backend := generated(t, root, "marker", "backend.tf")
	require.Contains(t, backend, `backend "s3"`)
	for _, file := range []string{"backend.tf", "r2-state.config"} {
		assert.NotContains(t, generated(t, root, "marker", file), "token-one", "%s carries the OAuth token", file)
	}

	out, err = run("marker", "token-two", nil, "plan", "-detailed-exitcode")
	require.NoError(t, err, "plan after a token refresh: %s", out)
	assert.Equal(t, backend, generated(t, root, "marker", "backend.tf"), "backend.tf changed with the token")
}

func prefixed(keys []string, prefix string) []string {
	var out []string
	for _, k := range keys {
		out = append(out, prefix+k)
	}
	return out
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
	assert.Contains(t, string(log), "--account-id acc-1 --bucket homelab-production-tfstate")
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
```

`stateTree` copies `infra/root.hcl`, `scripts/rotate-state-passphrase` and `scripts/cloudflare-credentials`; leave it
as is.

In `test/state_scripts_test.go`, delete `TestStateBucket`.

- [ ] **Step 2: Run the tests to see them fail**

Run: `direnv exec . env -C test go test -run 'TestStateTokenRefresh|TestStateS3Lock|TestStateBucketEnsuredOnInit|TestStateRotationSkipsMissingObjects' ./... 2>&1 | grep -E -- '--- (FAIL|PASS)|^(ok|FAIL)' | head`
Expected: all four FAIL (the backend is still `http`: `backend "s3"` missing, nothing reaches the fake S3).

- [ ] **Step 3: The `s3` backend in `infra/root.hcl`**

In `locals`, replace `state_api` with:

```hcl
  # R2's S3 endpoint, and the command that runs lab (a fake endpoint and a stub in test/state_token_refresh_test.go)
  state_endpoint = get_env("HOMELAB_STATE_ENDPOINT", "https://${local.cloudflare.account_id}.r2.cloudflarestorage.com")
  lab            = get_env("HOMELAB_LAB", "go -C ${get_repo_root()}/cli run ./cmd/lab")
```

Replace the production branch of `generate "backend"` and its comment:

```hcl
# Production's state is on R2 through its S3 API (one object per unit, a lock file while a command runs), with
# expiring credentials from lab (r2-state.config); the sandbox's stays local. Both are encrypted (encryption.tf).
# Neither generated file holds a secret.
generate "backend" {
  path      = "backend.tf"
  if_exists = "overwrite_terragrunt"
  contents  = <<-EOF
    terraform {
    %{~if local.production}
      backend "s3" {
        bucket                      = "${local.state_bucket}"
        key                         = "${local.unit}/tfstate.json"
        region                      = "auto"
        endpoints                   = { s3 = "${local.state_endpoint}" }
        use_path_style              = true
        use_lockfile                = true
        profile                     = "r2-state"
        shared_config_files         = ["./r2-state.config"]
        skip_credentials_validation = true
        skip_region_validation      = true
        skip_requesting_account_id  = true
        skip_metadata_api_check     = true
        skip_s3_checksum            = true
      }
    %{~else}
      backend "local" {
        path = "${get_repo_root()}/.state/${path_relative_to_include()}/terraform.tfstate"
      }
    %{~endif}
    }
  EOF
}

generate "r2_credentials" {
  path      = "r2-state.config"
  if_exists = "overwrite_terragrunt"
  disable   = !local.production
  contents  = <<-EOF
    [profile r2-state]
    credential_process = ${local.lab} infra state credentials --account-id ${local.cloudflare.account_id} --bucket ${local.state_bucket}
  EOF
}
```

Replace the `state_bucket` hook's `execute` with
`["sh", "-c", "${local.lab} infra state ensure --account-id ${local.cloudflare.account_id} --bucket ${local.state_bucket}"]`.
Then `git rm scripts/ensure-state-bucket` and drop `  - scripts/ensure-state-bucket` from `test/scopes.yaml`'s `skip`.

- [ ] **Step 4: Run the tests**

Run: `direnv exec . env -C test go test -run 'TestState' -v ./... 2>&1 | grep -E -- '--- (FAIL|PASS)|^(ok|FAIL)'`
Expected: every `TestState*` passes. If the fake S3 lacks a request the backend makes (an `X-Id` query, `HEAD` on the
bucket, a multipart upload), add that case to `fakeS3` (ledger it) rather than loosening the assertions.

- [ ] **Step 5: Commit**

```bash
git add infra/root.hcl test/state_token_refresh_test.go test/state_scripts_test.go test/scopes.yaml
git commit -m "feat(infra): production state through R2's S3 API with a lock; credentials from lab, none in backend.tf"
```

- [ ] **Step 6: The basic checks**

Run: `direnv exec . make -C cli test check >/dev/null && echo cli=0; direnv exec . env -u HOMELAB_STATE_PASSPHRASE make -C infra check >/dev/null 2>&1; echo infra=$?; shellcheck scripts/*-state* scripts/cloudflare-credentials && echo sc=0`
Expected: `cli=0`, `infra=0`, `sc=0`.

- [ ] **Step 7: No `lab` or `cf` at parse time**

Run: `direnv exec . env PATH=/nonexistent:/usr/bin:/bin HOMELAB_CLOUDFLARE_OFFLINE=1 terragrunt hcl validate --working-dir infra 2>&1 | tail -2`
Expected: no `cf`/`go` "not found" error (`run_cmd` runs only `cloudflare-credentials`, which prints placeholders
offline; `lab` runs only in hooks and `credential_process`). If `terragrunt` itself is not found with that PATH, use
`env -u` on `PATH` entries instead and ledger the command used.

---

### Task 3: Migrate production's backend, with the user

**Files:** none in git.

- [ ] **Step 1: Ask the user (stop and ask)**

Tell the user: the `cloudflare` unit's object stays where it is (`homelab-production-tfstate/cloudflare/tfstate.json`,
already encrypted); `init` switches each production unit from `http` to `s3`; `plan` must then show no changes. Ask
for their go-ahead, and confirm `cf auth whoami` lists `account_api_tokens:create`:
`direnv exec . sh -c 'env -u CLOUDFLARE_API_TOKEN cf --quiet auth whoami' | jq -r '.scopes[] | select(. == "account_api_tokens:create")'`.

- [ ] **Step 2: Switch and verify `cloudflare`**

Run: `cd infra/production/cloudflare && direnv exec ../../.. terragrunt init --non-interactive --log-level error >/dev/null; echo init=$?; direnv exec ../../.. terragrunt state list --non-interactive --log-level error | wc -l; direnv exec ../../.. terragrunt plan --non-interactive --log-level error -detailed-exitcode >/dev/null 2>&1; echo plan=$?; cd -`
Expected: `init=0`, `8`, `plan=0`.

- [ ] **Step 3: The empty units**

Run: `for u in argocd openbao secrets; do (cd infra/production/$u && direnv exec ../../.. terragrunt init --non-interactive --log-level error >/dev/null 2>&1; echo "$u init=$?"); done`
Expected: `init=0` three times (their dependencies may fail later commands; init only touches the backend). Ledger any
non-zero with its message.

- [ ] **Step 4: No stray lock, and the object is still encrypted**

List the bucket through the Cloudflare API with the OAuth token (a mode-600 header file, removed afterwards):
`GET /accounts/<id>/r2/buckets/homelab-production-tfstate/objects` must show only `cloudflare/tfstate.json` (no
`.tflock`), and `GET …/objects/cloudflare/tfstate.json` must be JSON with `encrypted_data` and no `resources`.
Expected: one object, `encrypted`.

- [ ] **Step 5: Ledger** the counts and exit codes.

---

### Task 4: Documentation

**Files:**
- Modify: `docs/installation/production/external-resources.md`, `CLAUDE.md`, `docs/superpowers/deferred-minors.md`,
  `docs/concepts/secrets-management.md`

- [ ] **Step 1: external-resources.md**

- Cloudflare login section: after the `cf auth login` command, add: "On the consent page, enable **Account API
  Tokens** (it is off by default): OpenTofu manages the DNS tokens and `lab` creates the expiring R2 credentials with
  it. `cf auth whoami` must list `account_api_tokens:create`; otherwise run `cf auth login --force` and enable it."
- Remove the 8d warning block and the `--filter '!cloudflare'` workaround; step 4 of the Cloudflare steps goes back to
  "`make infra env=production` applies every production unit".
- "Where state lives": production's state is on R2 through its S3 API (`s3` backend, bucket
  `homelab-production-tfstate`, one object per unit, a `<unit>/tfstate.json.tflock` lock file while a command runs,
  one-hour credentials from `lab infra state credentials`); an interrupted command can leave the lock: `terragrunt
  force-unlock <ID>` with the ID OpenTofu prints.

- [ ] **Step 2: CLAUDE.md**

- Roadmap: `8d …` (the current 8d text) becomes `8d production state through R2's S3 API with a lock (upstream's s3
  backend, expiring R2 credentials from lab infra state credentials) (done)`.
- Sandbox bullet: "production's is on R2 (`homelab-production-tfstate`, Cloudflare credentials from `cf auth login`)"
  becomes "production's is on R2 through its S3 API with a lock (`homelab-production-tfstate`, credentials from `lab`
  and `cf auth login` with Account API Tokens enabled)".
- Production checklist: the Cloudflare sentence "whose OAuth scopes do not cover account API tokens: the unit cannot
  plan or apply until step 8d" becomes "(enable Account API Tokens on the consent page)"; the OpenBao bullet loses
  "; until step 8d `terragrunt run --all apply --filter '!cloudflare'` in `infra/production`, since `cloudflare` cannot
  apply".
- Ideas, the Go CLI entry: "`cli/` exists with `lab test scope` and upstream's `lab infra state ensure|credentials`;".

- [ ] **Step 3: secrets-management.md** — drop "until step 8d the unit cannot apply, [external resources](…)" from the
  rotation bullet.

- [ ] **Step 4: deferred-minors.md** — the 8b line "Losing `.state/production/secrets` silently creates a new
  WireGuard key, which `Replace` pushes over OpenBao's" becomes "Losing `secrets/tfstate.json` on R2 (or the state
  passphrase) silently creates a new WireGuard key, which `Replace` pushes over OpenBao's", When `11 (backups)`.

- [ ] **Step 5: Build and commit**

Run: `direnv exec . mkdocs build --strict 2>&1 | grep -aE 'WARNING|built in' | tail -1`
Expected: built, no warning.

```bash
git add docs CLAUDE.md
git commit -m "docs: state through R2's S3 API with a lock; enable Account API Tokens when logging in"
```

---

### Task 5: Final checks and `make dev`

- [ ] **Step 1: Basic checks**

Run: `direnv exec . env -C test go vet ./... && direnv exec . env -C test go test -run 'TestSmokeAppsForEnv|TestIdentity(TOTPWaitsForNextStep|PKCE)|TestGatewayRouteReady|TestGatewayNodeAddressFromInspect|TestNodeRestart(Blockers|WatchdogJobs)|TestIstioCNIWatchdog|TestState' ./... 2>&1 | tail -1; direnv exec . make -C cli test check >/dev/null && echo cli=0; for c in check-mesh-policies check-env-config check-secret-stores; do direnv exec . scripts/$c >/dev/null 2>&1; echo $c=$?; done; direnv exec . env -u HOMELAB_STATE_PASSPHRASE make -C infra check >/dev/null 2>&1; echo infra=$?; direnv exec . mkdocs build --strict >/dev/null 2>&1; echo mkdocs=$?`
Expected: `ok`, every `=0`.

- [ ] **Step 2: `make dev`** (scoped: `infra/root.hcl` selects the whole suite)

Run: `direnv exec . make dev base=feat/test-scopes > /tmp/claude-1000/-home-nixos-homelab/44339b87-568a-46fd-9484-aed834c50ef8/scratchpad/dev-8d.log 2>&1; echo dev=$?; grep -aE '^make dev:|^DONE|PASS TestNodeRestart |=== FAIL' /tmp/claude-1000/-home-nixos-homelab/44339b87-568a-46fd-9484-aed834c50ef8/scratchpad/dev-8d.log | tail -5`
Expected: `dev=0`, the whole suite selected, `DONE 123 tests`, `TestNodeRestart` passes. (`base=feat/test-scopes`
because this branch is stacked; once #39 is merged the default `origin/dev` is right.)
