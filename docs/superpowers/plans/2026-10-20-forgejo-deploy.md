# Step 10c: production deploys by an explicit push — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `make infra env=production` deploys only a merged, green `origin/master` commit to `ops/homelab` on Forgejo, which only the account `deploy` may push and Argo CD reads with a read-only token.

**Architecture:** `lab infra forgejo verify|sync|push` in `cli/` (Go, ported from upstream `nixos`): `internal/env` reads `infra/<env>/cluster/config.json`, `internal/guard` checks git and GitHub's API, `internal/secrets` reads and writes OpenBao through the API server's service proxy as `openbao/openbao-admin`, `internal/forgejo` provisions org `ops` with `forgejo-sdk` and pushes with git. The cluster side drops the seed Job, gives Argo CD the read-only token and makes `choose-values.sh` switch only once that token reads `master`.

**Tech Stack:** Go 1.26, cobra, `codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3` v3.0.0, `github.com/openbao/openbao/api/v2` v2.7.1, `k8s.io/client-go` v0.37.1 (`k8s.io/api`, `k8s.io/apimachinery` v0.37.1), git, POSIX sh, GNU make.

**Spec:** `docs/superpowers/specs/2026-10-20-forgejo-deploy-design.md`

## Global Constraints

- Latest stable versions, pinned exactly: forgejo-sdk v3.0.0, openbao api v2.7.1, client-go/api/apimachinery v0.37.1.
- `--env production|sandbox`, `production` by default; no `--source`, `--domain` or `--kubeconfig` flags.
- Guards apply to `production` only: clean tree, HEAD == `origin/master`, latest `checks` on HEAD succeeded, the pull request merged into `master` as HEAD has a latest `sandbox` on its head that succeeded. A running check, an API error or no pull request fails; nothing ever passes by default.
- `sync` owns org `ops` only: repository `ops/homelab` private, default branch `master`; users `deploy` (write) and `argocd` (read), `restricted`; push allowlist with only `deploy`; tokens `lab` (`write:repository`) and `argocd` (`read:repository`) in OpenBao at `secret/forgejo/deploy` and `secret/argocd/forgejo`, property `token`.
- No token or password reaches a command-line argument, the output or a log.
- `push` never forces. Production pushes HEAD; it pushes nothing when Forgejo's `master` already is HEAD.
- OpenBao: log in as `openbao/openbao-admin` (TokenRequest, audience `openbao`) with role `admin` through `services/http:openbao:8200/proxy`; revoke the token when done.
- `cli/` tests use the standard library `testing` package only (the existing style; no testify there). `test/` uses testify.
- Every task ends with `scripts/ci-check all` in the dev shell (`direnv exec . scripts/ci-check all`) passing before its commit; commits end with the session's attribution lines.

## Review Focus

1. A GitHub check run listed twice under one name (a rerun, or `sandbox-retarget`'s API-created run next to `sandbox-release`'s): the newest run decides, never the first listed — Task 1 tests it.
2. `--env` with a typo (`prod`) must fail before touching anything, not fall back to production — Task 1 tests it.
3. A token in OpenBao that Forgejo no longer knows (Forgejo's data lost): `sync` must replace it, not keep a dead token because OpenBao has one — Task 3 tests it.
4. The sandbox's `ops/homelab` holding a commit that is not an ancestor of HEAD (the branch was rebased between two `make dev` runs): the sandbox push must still be a fast-forward — Task 4 tests it.
5. `choose-values.sh` when `argocd-repo-forgejo` exists but Forgejo's pod is down (exec fails): stay on the seed, do not fail the bootstrap — Task 5 tests it.

## Rulings (where this plan refines the spec)

- A token's validity is checked with `GET /api/v1/repos/ops/homelab`, not `GET /api/v1/user` (spec decision 4): `GET /user` needs the scope `read:user`, which neither token has; both scopes read the repository.
- The sandbox push is a commit with HEAD's tree on top of what the sandbox's Forgejo holds (`forgejo.SandboxCommit`), so it stays a fast-forward when a branch is rebased between two `make dev` runs; the sandbox test checks the tree, not the commit (spec: "Forgejo's master is HEAD"). Production pushes HEAD itself.
- `force=1` applies the infrastructure only and deploys nothing (no sync, push or second apply): a state migration from another commit must not deploy that commit (spec decision 9 named only the skipped guard).
- Whether a Forgejo site admin can push to the protected `master` is found once by hand on the sandbox (Task 7 Step 4), not by a permanent test: an accepted probe would move the sandbox's `master` and every later run's state with it.

---

## File Structure

- `cli/internal/env/env.go` (create): `Env`, `Load(root, name)`, `Env.GitHubRepository()`.
- `cli/internal/guard/git.go`, `github.go`, `verify.go` (create): git checks, GitHub's checks and pull requests, `Verify`.
- `cli/internal/secrets/openbao.go` (create): `Store` (`Open`, `Read`, `Write`, `Close`).
- `cli/internal/forgejo/endpoint.go`, `sync.go`, `push.go` (create): how to reach Forgejo, `Sync`, `Deployed`/`Push`/`SandboxCommit`.
- `cli/internal/cli/infra_forgejo.go` (create): cobra commands, sandbox endpoint, Argo CD credential refresh.
- `cli/internal/cli/root.go` (modify): register `forgejo` under `infra`.
- `infra/_modules/argocd/choose-values.sh` (modify): switch when Argo CD's token reads `master`.
- `platform/forgejo/templates/seed-job.yaml`, `platform/forgejo/files/seed.sh` (delete); `platform/forgejo/values.yaml` (modify: the seed comment).
- `system/argocd-config/templates/forgejo-repository.yaml`, `platform/global-secrets/values.yaml` (modify).
- `Makefile` (modify): `infra` runs verify, apply, sync, push, apply argocd; `production-checkout` goes.
- `test/forgejo_test.go` (rewrite), `test/ci_scripts_test.go` (modify), `test/scopes.yaml`, `scripts/ci-check` (modify).
- Docs: `docs/installation/production/deployment.md`, `docs/concepts/secrets-management.md`, `docs/reference/architecture/overview.md` (if it names the seed Job), CLAUDE.md.

---

### Task 1: environments, guards and `lab infra forgejo verify`

**Files:**
- Create: `cli/internal/env/env.go`, `cli/internal/env/env_test.go`
- Create: `cli/internal/guard/git.go`, `cli/internal/guard/github.go`, `cli/internal/guard/verify.go`, `cli/internal/guard/verify_test.go`
- Create: `cli/internal/cli/infra_forgejo.go` (verify only in this task)
- Modify: `cli/internal/cli/root.go`

**Interfaces:**
- Produces: `env.Load(root, name string) (env.Env, error)`; `env.Env{Name, Root, Domain, SeedRepository, Kubeconfig string}`; `(env.Env).GitHubRepository() (string, error)` (`owner/name`); `guard.GitHub{BaseURL string; HTTP *http.Client}`; `guard.Verify(ctx, root, repository string, gh guard.GitHub) (head string, err error)`; `newInfraForgejoCmd() *cobra.Command` with persistent flag `--env`; helper `forgejoEnv(cmd) (env.Env, error)`.

- [ ] **Step 1: Write the failing env test**

```go
// cli/internal/env/env_test.go
package env

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, "infra", name, "cluster")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoad(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "production", `{"domain": "example.dev", "seed_repository": "https://github.com/owner/homelab"}`)
	e, err := Load(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	if e.Domain != "example.dev" || e.Kubeconfig != filepath.Join(root, "infra/production/kubeconfig.yaml") {
		t.Fatalf("got %+v", e)
	}
	repo, err := e.GitHubRepository()
	if err != nil || repo != "owner/homelab" {
		t.Fatalf("GitHubRepository() = %q, %v", repo, err)
	}
}

func TestLoadRefusesUnknownEnvironments(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "production", `{"domain": "example.dev"}`)
	for _, name := range []string{"prod", "dev", "", "../production"} {
		if _, err := Load(root, name); err == nil {
			t.Errorf("Load(%q) succeeded", name)
		}
	}
}

func TestLoadNeedsADomain(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "sandbox", `{"domain": ""}`)
	if _, err := Load(root, "sandbox"); err == nil {
		t.Fatal("an empty domain was accepted")
	}
}

func TestGitHubRepositoryRefusesOtherHosts(t *testing.T) {
	for _, seed := range []string{"", "https://gitlab.com/owner/homelab", "https://github.com/owner", "https://github.com/owner/homelab/extra"} {
		if _, err := (Env{SeedRepository: seed}).GitHubRepository(); err == nil {
			t.Errorf("GitHubRepository() accepted %q", seed)
		}
	}
	if repo, err := (Env{SeedRepository: "https://github.com/owner/homelab.git"}).GitHubRepository(); err != nil || repo != "owner/homelab" {
		t.Errorf("GitHubRepository() = %q, %v", repo, err)
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `direnv exec . sh -c 'cd cli && go test ./internal/env/'`
Expected: FAIL (`undefined: Load`).

- [ ] **Step 3: Implement `env`**

```go
// cli/internal/env/env.go
// Package env reads an environment's settings from infra/<env>/cluster/config.json, the one place they live.
package env

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Env is one environment of the homelab: production or the sandbox.
type Env struct {
	Name           string
	Root           string
	Domain         string
	SeedRepository string
	Kubeconfig     string
}

// Load reads infra/<name>/cluster/config.json in the repository at root.
func Load(root, name string) (Env, error) {
	if name != "production" && name != "sandbox" {
		return Env{}, fmt.Errorf("--env must be production or sandbox, not %q", name)
	}
	path := filepath.Join(root, "infra", name, "cluster", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return Env{}, err
	}
	var config struct {
		Domain         string `json:"domain"`
		SeedRepository string `json:"seed_repository"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return Env{}, fmt.Errorf("%s: %w", path, err)
	}
	if config.Domain == "" {
		return Env{}, fmt.Errorf("%s has no domain", path)
	}
	return Env{
		Name:           name,
		Root:           root,
		Domain:         config.Domain,
		SeedRepository: config.SeedRepository,
		Kubeconfig:     filepath.Join(root, "infra", name, "kubeconfig.yaml"),
	}, nil
}

// GitHubRepository is the seed repository as owner/name: the GitHub repository whose checks guard a deploy.
func (e Env) GitHubRepository() (string, error) {
	u, err := url.Parse(e.SeedRepository)
	if err != nil || u.Host != "github.com" {
		return "", fmt.Errorf("seed_repository %q is not a GitHub repository", e.SeedRepository)
	}
	path := strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/")
	if strings.Count(path, "/") != 1 {
		return "", fmt.Errorf("seed_repository %q is not https://github.com/<owner>/<name>", e.SeedRepository)
	}
	return path, nil
}
```

- [ ] **Step 4: Run the env tests**

Run: `direnv exec . sh -c 'cd cli && go test ./internal/env/'`
Expected: `ok`.

- [ ] **Step 5: Write the failing guard test**

```go
// cli/internal/guard/verify_test.go
package guard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repos makes a bare origin and a clone on master with one commit pushed; it returns the clone and its HEAD.
func repos(t *testing.T) (string, string) {
	t.Helper()
	origin, clone := t.TempDir(), t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(origin, "init", "-q", "--bare", "-b", "master")
	git(clone, "init", "-q", "-b", "master")
	git(clone, "remote", "add", "origin", origin)
	if err := os.WriteFile(filepath.Join(clone, "file"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(clone, "add", "file")
	git(clone, "commit", "-q", "-m", "one")
	git(clone, "push", "-q", "origin", "master")
	return clone, git(clone, "rev-parse", "HEAD")
}

type run struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"html_url"`
}

type pull struct {
	MergedAt       string `json:"merged_at"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	Base           struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

// github serves check runs per "<sha> <name>" and pull requests per merge commit.
func github(t *testing.T, runs map[string][]run, pulls map[string][]pull) GitHub {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/repos/owner/homelab/commits/"), "/")
		switch {
		case len(parts) == 2 && parts[1] == "check-runs":
			_ = json.NewEncoder(w).Encode(map[string]any{"check_runs": runs[parts[0]+" "+r.URL.Query().Get("check_name")]})
		case len(parts) == 2 && parts[1] == "pulls":
			_ = json.NewEncoder(w).Encode(pulls[parts[0]])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return GitHub{BaseURL: server.URL, HTTP: server.Client()}
}

func merged(head, sha string) []pull {
	p := pull{MergedAt: "2026-10-20T00:00:00Z", MergeCommitSHA: sha}
	p.Base.Ref = "master"
	p.Head.SHA = head
	return []pull{p}
}

func success(id int64) []run { return []run{{ID: id, Status: "completed", Conclusion: "success"}} }

func TestVerify(t *testing.T) {
	clone, head := repos(t)
	ok := map[string][]run{head + " checks": success(1), "prhead sandbox": success(2)}
	cases := []struct {
		name  string
		runs  map[string][]run
		pulls map[string][]pull
		want  string // a substring of the error; empty for success
	}{
		{"both checks passed", ok, map[string][]pull{head: merged("prhead", head)}, ""},
		{"checks running", map[string][]run{head + " checks": {{ID: 1, Status: "in_progress"}}, "prhead sandbox": success(2)}, map[string][]pull{head: merged("prhead", head)}, "wait for it"},
		{"checks failed", map[string][]run{head + " checks": {{ID: 1, Status: "completed", Conclusion: "failure"}}}, map[string][]pull{head: merged("prhead", head)}, "concluded failure"},
		{"checks never ran", map[string][]run{}, map[string][]pull{head: merged("prhead", head)}, "has not run"},
		{"no pull request", ok, map[string][]pull{}, "no pull request"},
		{"pull request into dev", ok, map[string][]pull{head: func() []pull { p := merged("prhead", head); p[0].Base.Ref = "dev"; return p }()}, "no pull request"},
		{"sandbox failed", map[string][]run{head + " checks": success(1), "prhead sandbox": {{ID: 2, Status: "completed", Conclusion: "failure"}}}, map[string][]pull{head: merged("prhead", head)}, "concluded failure"},
		// Review Focus 1: a rerun (or sandbox-retarget's run beside sandbox-release's) lists two runs; the newest decides
		{"newest run decides", map[string][]run{head + " checks": success(1), "prhead sandbox": {{ID: 9, Status: "completed", Conclusion: "failure"}, {ID: 3, Status: "completed", Conclusion: "success"}}}, map[string][]pull{head: merged("prhead", head)}, "concluded failure"},
		{"newest run passed", map[string][]run{head + " checks": success(1), "prhead sandbox": {{ID: 3, Status: "completed", Conclusion: "failure"}, {ID: 9, Status: "completed", Conclusion: "success"}}}, map[string][]pull{head: merged("prhead", head)}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Verify(context.Background(), clone, "owner/homelab", github(t, c.runs, c.pulls))
			if c.want == "" {
				if err != nil || got != head {
					t.Fatalf("Verify() = %q, %v", got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Verify() error = %v, want %q", err, c.want)
			}
		})
	}
}

func TestVerifyGitGuards(t *testing.T) {
	gh := GitHub{BaseURL: "http://127.0.0.1:1", HTTP: http.DefaultClient} // never reached
	clone, _ := repos(t)
	if err := os.WriteFile(filepath.Join(clone, "file"), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(context.Background(), clone, "owner/homelab", gh); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("an uncommitted change: %v", err)
	}
	cmd := exec.Command("git", "-C", clone, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "commit", "-qam", "two")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := Verify(context.Background(), clone, "owner/homelab", gh); err == nil || !strings.Contains(err.Error(), "origin/master") {
		t.Fatalf("a commit not on origin/master: %v", err)
	}
}

func TestVerifyFailsWhenGitHubFails(t *testing.T) {
	clone, _ := repos(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	defer server.Close()
	if _, err := Verify(context.Background(), clone, "owner/homelab", GitHub{BaseURL: server.URL, HTTP: server.Client()}); err == nil {
		t.Fatal("an API error passed the guard")
	}
}
```

- [ ] **Step 6: Run it to see it fail**

Run: `direnv exec . sh -c 'cd cli && go test ./internal/guard/'`
Expected: FAIL (`undefined: Verify`, `undefined: GitHub`).

- [ ] **Step 7: Implement the guards**

```go
// cli/internal/guard/git.go
package guard

import (
	"fmt"
	"os/exec"
	"strings"
)

func git(root string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// releaseCommit is HEAD when the checkout is origin/master without uncommitted changes to tracked files.
func releaseCommit(root string) (string, error) {
	status, err := git(root, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return "", err
	}
	if status != "" {
		return "", fmt.Errorf("uncommitted changes: production deploys only origin/master as it is\n%s", status)
	}
	if _, err := git(root, "fetch", "--quiet", "origin", "master"); err != nil {
		return "", err
	}
	head, err := git(root, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	master, err := git(root, "rev-parse", "origin/master")
	if err != nil {
		return "", err
	}
	if head != master {
		return "", fmt.Errorf("HEAD %.12s is not origin/master %.12s: production deploys only origin/master (git switch master && git pull)", head, master)
	}
	return head, nil
}
```

```go
// cli/internal/guard/github.go
package guard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// GitHub reads check runs and pull requests from GitHub's REST API without a token (the repository is public).
type GitHub struct {
	BaseURL string // https://api.github.com
	HTTP    *http.Client
}

// Run is one check run.
type Run struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"html_url"`
}

func (g GitHub) get(ctx context.Context, path string, query url.Values, into any) error {
	u := g.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub API: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GitHub API %s: %s: %s", path, resp.Status, body)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// LatestRun is the newest check run named name on commit sha (the highest ID: a rerun or a second workflow
// reporting the same name lists several), or nil when none ran.
func (g GitHub) LatestRun(ctx context.Context, repository, sha, name string) (*Run, error) {
	var page struct {
		Runs []Run `json:"check_runs"`
	}
	if err := g.get(ctx, "/repos/"+repository+"/commits/"+sha+"/check-runs", url.Values{"check_name": {name}, "per_page": {"100"}}, &page); err != nil {
		return nil, err
	}
	var latest *Run
	for i := range page.Runs {
		if latest == nil || page.Runs[i].ID > latest.ID {
			latest = &page.Runs[i]
		}
	}
	return latest, nil
}

// MergedPullHead is the head commit of the pull request merged into master as sha, or "" when none was.
func (g GitHub) MergedPullHead(ctx context.Context, repository, sha string) (string, error) {
	var pulls []struct {
		MergedAt       *string `json:"merged_at"`
		MergeCommitSHA string  `json:"merge_commit_sha"`
		Base           struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := g.get(ctx, "/repos/"+repository+"/commits/"+sha+"/pulls", nil, &pulls); err != nil {
		return "", err
	}
	for _, p := range pulls {
		if p.MergedAt != nil && *p.MergedAt != "" && p.Base.Ref == "master" && p.MergeCommitSHA == sha {
			return p.Head.SHA, nil
		}
	}
	return "", nil
}
```

```go
// cli/internal/guard/verify.go
// Package guard decides whether a commit may reach production: origin/master, green checks, a merged pull request.
package guard

import (
	"context"
	"fmt"
)

// Verify returns HEAD when it may be deployed to production, or why not. It never passes when it cannot tell.
func Verify(ctx context.Context, root, repository string, gh GitHub) (string, error) {
	head, err := releaseCommit(root)
	if err != nil {
		return "", err
	}
	if err := succeeded(ctx, gh, repository, head, "checks"); err != nil {
		return "", err
	}
	pullHead, err := gh.MergedPullHead(ctx, repository, head)
	if err != nil {
		return "", err
	}
	if pullHead == "" {
		return "", fmt.Errorf("no pull request into master was merged as %.12s: production deploys only merged pull requests", head)
	}
	if err := succeeded(ctx, gh, repository, pullHead, "sandbox"); err != nil {
		return "", err
	}
	return head, nil
}

func succeeded(ctx context.Context, gh GitHub, repository, sha, name string) error {
	run, err := gh.LatestRun(ctx, repository, sha, name)
	if err != nil {
		return err
	}
	switch {
	case run == nil:
		return fmt.Errorf("check %s has not run on %.12s", name, sha)
	case run.Status != "completed":
		return fmt.Errorf("check %s on %.12s is %s: wait for it and run again (%s)", name, sha, run.Status, run.URL)
	case run.Conclusion != "success":
		return fmt.Errorf("check %s on %.12s concluded %s (%s)", name, sha, run.Conclusion, run.URL)
	}
	return nil
}
```

- [ ] **Step 8: Run the guard tests**

Run: `direnv exec . sh -c 'cd cli && go test -race ./internal/guard/ ./internal/env/'`
Expected: `ok` for both.

- [ ] **Step 9: Add `lab infra forgejo verify`**

```go
// cli/internal/cli/infra_forgejo.go
package cli

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/duongle-steven/homelab/cli/internal/env"
	"github.com/duongle-steven/homelab/cli/internal/guard"
	"github.com/duongle-steven/homelab/cli/internal/scope"
	"github.com/spf13/cobra"
)

func newInfraForgejoCmd() *cobra.Command {
	group := &cobra.Command{
		Use:   "forgejo",
		Short: "Deploy to the GitOps repository ops/homelab on Forgejo",
		Long:  "Production's Argo CD syncs ops/homelab on Forgejo, which changes only through push. --env sandbox targets the sandbox's Forgejo (the tests) and skips the guards.",
	}
	group.PersistentFlags().String("env", "production", "production, or sandbox (the tests)")
	verify := &cobra.Command{
		Use:   "verify",
		Short: "Check that HEAD may be deployed to production",
		Long:  "Pass only when the checkout is origin/master without uncommitted changes, its check checks succeeded, and the pull request merged into master as HEAD has a succeeded check sandbox on its head. A running check or an API error fails.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := forgejoEnv(cmd)
			if err != nil {
				return err
			}
			if e.Name != "production" {
				return fmt.Errorf("verify guards production only")
			}
			head, err := verifyProduction(cmd.Context(), e)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%.12s may be deployed.\n", head)
			return err
		},
	}
	group.AddCommand(verify)
	return group
}

// forgejoEnv loads the environment --env names, from the repository lab runs in.
func forgejoEnv(cmd *cobra.Command) (env.Env, error) {
	name, err := cmd.Flags().GetString("env")
	if err != nil {
		return env.Env{}, err
	}
	root, err := scope.Root()
	if err != nil {
		return env.Env{}, err
	}
	return env.Load(root, name)
}

func verifyProduction(ctx context.Context, e env.Env) (string, error) {
	repository, err := e.GitHubRepository()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return guard.Verify(ctx, e.Root, repository, guard.GitHub{BaseURL: "https://api.github.com", HTTP: &http.Client{Timeout: 30 * time.Second}})
}
```

In `cli/internal/cli/root.go`, after `infra.AddCommand(newInfraStateCmd())` add `infra.AddCommand(newInfraForgejoCmd())`, and extend `Example` with `\n  lab infra forgejo push`.

- [ ] **Step 10: Check the command against the real repository**

Run: `direnv exec . sh -c 'cd cli && go run ./cmd/lab infra forgejo verify --env prod; echo "exit=$?"; go run ./cmd/lab infra forgejo verify; echo "exit=$?"'`
Expected: the first fails with `--env must be production or sandbox, not "prod"`; the second fails on a feature branch with `HEAD … is not origin/master` (exit 1 both).

- [ ] **Step 11: Basic checks and commit**

Run: `direnv exec . scripts/ci-check all` — Expected: exit 0.

```bash
git add cli/internal/env cli/internal/guard cli/internal/cli/infra_forgejo.go cli/internal/cli/root.go
git commit -m "feat(cli): lab infra forgejo verify (origin/master, checks, the merged pull request's sandbox)"
```

---

### Task 2: OpenBao through the API server as `openbao-admin`

**Files:**
- Modify: `cli/go.mod`, `cli/go.sum` (add openbao api v2.7.1, client-go/api/apimachinery v0.37.1)
- Create: `cli/internal/secrets/openbao.go`, `cli/internal/secrets/openbao_test.go`

**Interfaces:**
- Produces: `secrets.Open(ctx, config *rest.Config) (*secrets.Store, error)`; `(*Store).Read(ctx, path string) (map[string]string, error)` (nil when absent); `(*Store).Write(ctx, path string, data map[string]string) error` (writes only a change); `(*Store).Close(ctx) error` (revokes). Internal `open(ctx, kube kubernetes.Interface, address string, httpClient *http.Client) (*Store, error)` for the tests.

- [ ] **Step 1: Add the dependencies**

Run: `direnv exec . sh -c 'cd cli && go get github.com/openbao/openbao/api/v2@v2.7.1 k8s.io/client-go@v0.37.1 k8s.io/api@v0.37.1 k8s.io/apimachinery@v0.37.1'`
Expected: `go.mod` lists the four at those versions.

- [ ] **Step 2: Write the failing test**

```go
// cli/internal/secrets/openbao_test.go
package secrets

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	authv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// fakeBao accepts the service account token "jwt-admin" for role admin and keeps KV v2 data in memory.
type fakeBao struct {
	mu      sync.Mutex
	data    map[string]map[string]any
	writes  int
	revoked bool
}

func (f *fakeBao) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/v1/auth/kubernetes/login":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["role"] != "admin" || body["jwt"] != "jwt-admin" {
			http.Error(w, `{"errors":["permission denied"]}`, http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"auth": map[string]any{"client_token": "bao-token"}})
	case r.Header.Get("X-Vault-Token") != "bao-token":
		http.Error(w, `{"errors":["permission denied"]}`, http.StatusForbidden)
	case r.URL.Path == "/v1/auth/token/revoke-self":
		f.revoked = true
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(r.URL.Path, "/v1/secret/data/"):
		path := strings.TrimPrefix(r.URL.Path, "/v1/secret/data/")
		switch r.Method {
		case http.MethodGet:
			data, ok := f.data[path]
			if !ok {
				http.Error(w, `{"errors":[]}`, http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": data, "metadata": map[string]any{"version": 1}}})
		default:
			var body struct {
				Data map[string]any `json:"data"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.data[path] = body.Data
			f.writes++
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"version": 1}})
		}
	default:
		http.NotFound(w, r)
	}
}

func kubeWithToken(t *testing.T) *fake.Clientset {
	t.Helper()
	kube := fake.NewClientset()
	kube.PrependReactor("create", "serviceaccounts", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create := action.(k8stesting.CreateAction)
		if create.GetSubresource() != "token" || create.GetNamespace() != "openbao" || create.(k8stesting.CreateActionImpl).Name != "openbao-admin" {
			t.Errorf("unexpected token request: %v", action)
		}
		request := create.GetObject().(*authv1.TokenRequest)
		if len(request.Spec.Audiences) != 1 || request.Spec.Audiences[0] != "openbao" {
			t.Errorf("audiences = %v", request.Spec.Audiences)
		}
		request.Status.Token = "jwt-admin"
		return true, request, nil
	})
	return kube
}

func TestStore(t *testing.T) {
	bao := &fakeBao{data: map[string]map[string]any{"forgejo/deploy": {"token": "old"}}}
	server := httptest.NewServer(bao)
	defer server.Close()
	ctx := context.Background()
	store, err := open(ctx, kubeWithToken(t), server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Read(ctx, "forgejo/deploy")
	if err != nil || got["token"] != "old" {
		t.Fatalf("Read() = %v, %v", got, err)
	}
	if got, err := store.Read(ctx, "argocd/forgejo"); err != nil || got != nil {
		t.Fatalf("Read() of a missing path = %v, %v", got, err)
	}
	if err := store.Write(ctx, "forgejo/deploy", map[string]string{"token": "old"}); err != nil || bao.writes != 0 {
		t.Fatalf("an unchanged Write() wrote: %v, %d", err, bao.writes)
	}
	if err := store.Write(ctx, "forgejo/deploy", map[string]string{"token": "new"}); err != nil || bao.writes != 1 {
		t.Fatalf("Write() = %v, %d writes", err, bao.writes)
	}
	if err := store.Close(ctx); err != nil || !bao.revoked {
		t.Fatalf("Close() = %v, revoked %v", err, bao.revoked)
	}
}

func TestStoreErrorsHideValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"errors":["secret-value-123"]}`, http.StatusInternalServerError)
	}))
	defer server.Close()
	_, err := open(context.Background(), kubeWithToken(t), server.URL, server.Client())
	if err == nil || strings.Contains(err.Error(), "jwt-admin") {
		t.Fatalf("open() = %v", err)
	}
}
```

- [ ] **Step 3: Run it to see it fail**

Run: `direnv exec . sh -c 'cd cli && go test ./internal/secrets/'`
Expected: FAIL (`undefined: open`).

- [ ] **Step 4: Implement the store**

```go
// cli/internal/secrets/openbao.go
// Package secrets reads and writes OpenBao's KV store as an admin, through the Kubernetes API server's service proxy.
package secrets

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"

	bao "github.com/openbao/openbao/api/v2"
	authv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Store is a logged-in OpenBao client for KV v2 mount secret/.
type Store struct {
	client *bao.Client
	kv     *bao.KVv2
}

// Open logs in to OpenBao as service account openbao/openbao-admin with the role admin (OpenBao has no root
// token), reaching it through the API server's proxy for service openbao.
func Open(ctx context.Context, config *rest.Config) (*Store, error) {
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	httpClient, err := rest.HTTPClientFor(config)
	if err != nil {
		return nil, err
	}
	address := kube.CoreV1().RESTClient().Get().Namespace("openbao").Resource("services").Name("http:openbao:8200").SubResource("proxy").URL().String()
	return open(ctx, kube, address, httpClient)
}

func open(ctx context.Context, kube kubernetes.Interface, address string, httpClient *http.Client) (*Store, error) {
	expiry := int64(600)
	request, err := kube.CoreV1().ServiceAccounts("openbao").CreateToken(ctx, "openbao-admin", &authv1.TokenRequest{
		Spec: authv1.TokenRequestSpec{Audiences: []string{"openbao"}, ExpirationSeconds: &expiry},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("token for openbao/openbao-admin: %w", err)
	}
	config := bao.DefaultConfig()
	config.Address = address
	config.HttpClient = httpClient
	client, err := bao.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("OpenBao client: %w", err)
	}
	// Never a token from the environment (BAO_TOKEN): only the admin login below
	client.ClearToken()
	login, err := client.Logical().WriteWithContext(ctx, "auth/kubernetes/login", map[string]any{"role": "admin", "jwt": request.Status.Token})
	if err != nil {
		return nil, fmt.Errorf("log in to OpenBao with the role admin failed")
	}
	if login == nil || login.Auth == nil || login.Auth.ClientToken == "" {
		return nil, fmt.Errorf("OpenBao returned no token for the role admin")
	}
	client.SetToken(login.Auth.ClientToken)
	return &Store{client: client, kv: client.KVv2("secret")}, nil
}

// Read returns the record at secret/<path>, or nil when there is none.
func (s *Store) Read(ctx context.Context, path string) (map[string]string, error) {
	record, err := s.kv.Get(ctx, path)
	if errors.Is(err, bao.ErrSecretNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read OpenBao secret/%s failed", path)
	}
	data := make(map[string]string, len(record.Data))
	for key, value := range record.Data {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("OpenBao secret/%s has a non-string field %s", path, key)
		}
		data[key] = text
	}
	return data, nil
}

// Write stores data at secret/<path> unless it is already there.
func (s *Store) Write(ctx context.Context, path string, data map[string]string) error {
	existing, err := s.Read(ctx, path)
	if err != nil {
		return err
	}
	if reflect.DeepEqual(existing, data) {
		return nil
	}
	fields := make(map[string]any, len(data))
	for key, value := range data {
		fields[key] = value
	}
	if _, err := s.kv.Put(ctx, path, fields); err != nil {
		return fmt.Errorf("write OpenBao secret/%s failed", path)
	}
	return nil
}

// Close revokes the admin token.
func (s *Store) Close(ctx context.Context) error {
	if err := s.client.Auth().Token().RevokeSelfWithContext(ctx, ""); err != nil {
		return fmt.Errorf("revoke the OpenBao admin token failed")
	}
	return nil
}
```

Note: the error messages never wrap the OpenBao client's errors, which can carry response bodies.

- [ ] **Step 5: Run the tests**

Run: `direnv exec . sh -c 'cd cli && go test -race ./internal/secrets/'`
Expected: `ok`. If the fake's paths differ from what the client sends (for example `GET /v1/secret/data/forgejo/deploy` vs a query string), adjust the fake, not the store.

- [ ] **Step 6: Try it against the sandbox (read only)**

Write a throwaway `cli/cmd/probe/main.go` that opens the store with `clientcmd.BuildConfigFromFlags("", "../infra/sandbox/kubeconfig.yaml")`, reads `forgejo/generated/admin` and prints only `len(data["password"]) > 0`, then `Close`. Run: `direnv exec . sh -c 'cd cli && go run ./cmd/probe'` — Expected: `true`. Delete `cli/cmd/probe` before committing.

- [ ] **Step 7: Basic checks and commit**

Run: `direnv exec . scripts/ci-check all` — Expected: exit 0.

```bash
git add cli/go.mod cli/go.sum cli/internal/secrets
git commit -m "feat(cli): OpenBao through the API server's proxy as openbao-admin (role admin)"
```

---

### Task 3: `forgejo.Sync`

**Files:**
- Modify: `cli/go.mod`, `cli/go.sum` (forgejo-sdk v3.0.0)
- Create: `cli/internal/forgejo/endpoint.go`, `cli/internal/forgejo/sync.go`, `cli/internal/forgejo/fake_test.go`, `cli/internal/forgejo/sync_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks (the store is an interface).
- Produces: `forgejo.Endpoint{URL string; HTTP *http.Client; Git []string}`; `forgejo.ProductionEndpoint(domain string) Endpoint`; `forgejo.Tokens` interface `{Read(ctx, path) (map[string]string, error); Write(ctx, path, data) error}` (satisfied by `*secrets.Store`); `forgejo.Sync(ctx, ep Endpoint, adminPassword string, tokens Tokens) error`; constants `forgejo.DeployPath = "forgejo/deploy"`, `forgejo.ArgoCDPath = "argocd/forgejo"`, `forgejo.Org = "ops"`, `forgejo.Repo = "homelab"`, `forgejo.Branch = "master"`.

- [ ] **Step 1: Add the SDK**

Run: `direnv exec . sh -c 'cd cli && go get codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3@v3.0.0'`

- [ ] **Step 2: Write the fake Forgejo**

```go
// cli/internal/forgejo/fake_test.go
package forgejo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fakeUser struct {
	password   string
	restricted bool
	tokens     map[int64]fakeToken
}

type fakeToken struct {
	name, sha1 string
	scopes     []string
}

// fakeForgejo implements the API calls Sync makes, in memory; writes counts every mutating request.
type fakeForgejo struct {
	mu            sync.Mutex
	admin         string
	org, repo     bool
	users         map[string]*fakeUser
	collaborators map[string]string
	protection    map[string]any
	writes        int
	nextID        int64
}

func newFakeForgejo(t *testing.T) (*fakeForgejo, Endpoint) {
	f := &fakeForgejo{admin: "admin-password", users: map[string]*fakeUser{}, collaborators: map[string]string{}}
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	return f, Endpoint{URL: server.URL, HTTP: server.Client()}
}

func (f *fakeForgejo) who(r *http.Request) string {
	if user, password, ok := r.BasicAuth(); ok {
		if user == "forgejo_admin" && password == f.admin {
			return "forgejo_admin"
		}
		if u := f.users[user]; u != nil && u.password == password {
			return user
		}
		return ""
	}
	if token := strings.TrimPrefix(r.Header.Get("Authorization"), "token "); token != "" {
		for name, u := range f.users {
			for _, tk := range u.tokens {
				if tk.sha1 == token {
					return name
				}
			}
		}
	}
	return ""
}

func (f *fakeForgejo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	if r.Method != http.MethodGet {
		f.writes++
	}
	reply := func(code int, body any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if body != nil {
			_ = json.NewEncoder(w).Encode(body)
		}
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if r.URL.Path == "/api/healthz" {
		reply(http.StatusOK, map[string]string{"status": "pass"})
		return
	}
	if path == "/version" {
		reply(http.StatusOK, map[string]string{"version": "16.0.6"})
		return
	}
	who := f.who(r)
	if who == "" {
		reply(http.StatusUnauthorized, map[string]string{"message": "unauthorized"})
		return
	}
	admin := who == "forgejo_admin"
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case path == "/orgs/ops" && r.Method == http.MethodGet && admin:
		if !f.org {
			reply(http.StatusNotFound, nil)
			return
		}
		reply(http.StatusOK, map[string]any{"username": "ops"})
	case path == "/orgs" && r.Method == http.MethodPost && admin:
		f.org = true
		reply(http.StatusCreated, map[string]any{"username": "ops"})
	case path == "/repos/ops/homelab" && r.Method == http.MethodGet:
		if !f.repo || (!admin && f.collaborators[who] == "") {
			reply(http.StatusNotFound, nil)
			return
		}
		reply(http.StatusOK, map[string]any{"name": "homelab", "private": true, "default_branch": "master"})
	case path == "/orgs/ops/repos" && r.Method == http.MethodPost && admin:
		if body["private"] != true || body["default_branch"] != "master" {
			reply(http.StatusUnprocessableEntity, map[string]any{"message": fmt.Sprint(body)})
			return
		}
		f.repo = true
		reply(http.StatusCreated, map[string]any{"name": "homelab"})
	case len(parts) == 2 && parts[0] == "users" && r.Method == http.MethodGet && admin:
		u := f.users[parts[1]]
		if u == nil {
			reply(http.StatusNotFound, nil)
			return
		}
		reply(http.StatusOK, map[string]any{"login": parts[1], "restricted": u.restricted})
	case path == "/admin/users" && r.Method == http.MethodPost && admin:
		name := body["username"].(string)
		f.users[name] = &fakeUser{password: body["password"].(string), tokens: map[int64]fakeToken{}}
		reply(http.StatusCreated, map[string]any{"login": name})
	case len(parts) == 3 && parts[0] == "admin" && parts[1] == "users" && r.Method == http.MethodPatch && admin:
		u := f.users[parts[2]]
		if p, _ := body["password"].(string); p != "" {
			u.password = p
		}
		if restricted, ok := body["restricted"].(bool); ok {
			u.restricted = restricted
		}
		reply(http.StatusOK, map[string]any{"login": parts[2]})
	case len(parts) == 6 && parts[3] == "collaborators" && parts[5] == "permission" && admin:
		permission := f.collaborators[parts[4]]
		if permission == "" {
			reply(http.StatusNotFound, nil)
			return
		}
		reply(http.StatusOK, map[string]any{"permission": permission})
	case len(parts) == 5 && parts[3] == "collaborators" && r.Method == http.MethodPut && admin:
		f.collaborators[parts[4]] = body["permission"].(string)
		reply(http.StatusNoContent, nil)
	case path == "/repos/ops/homelab/branch_protections/master" && r.Method == http.MethodGet && admin:
		if f.protection == nil {
			reply(http.StatusNotFound, nil)
			return
		}
		reply(http.StatusOK, f.protection)
	case path == "/repos/ops/homelab/branch_protections" && r.Method == http.MethodPost && admin:
		f.protection = body
		reply(http.StatusCreated, body)
	case path == "/repos/ops/homelab/branch_protections/master" && r.Method == http.MethodPatch && admin:
		for k, v := range body {
			f.protection[k] = v
		}
		reply(http.StatusOK, f.protection)
	case len(parts) >= 3 && parts[0] == "users" && parts[2] == "tokens" && who == parts[1]:
		u := f.users[who]
		switch r.Method {
		case http.MethodGet:
			list := []map[string]any{}
			for id, tk := range u.tokens {
				list = append(list, map[string]any{"id": id, "name": tk.name})
			}
			reply(http.StatusOK, list)
		case http.MethodPost:
			f.nextID++
			scopes := []string{}
			for _, s := range body["scopes"].([]any) {
				scopes = append(scopes, s.(string))
			}
			tk := fakeToken{name: body["name"].(string), sha1: fmt.Sprintf("token-%s-%d", who, f.nextID), scopes: scopes}
			u.tokens[f.nextID] = tk
			reply(http.StatusCreated, map[string]any{"id": f.nextID, "name": tk.name, "sha1": tk.sha1})
		case http.MethodDelete:
			var id int64
			_, _ = fmt.Sscan(parts[3], &id)
			delete(u.tokens, id)
			reply(http.StatusNoContent, nil)
		}
	default:
		reply(http.StatusNotFound, map[string]string{"message": r.Method + " " + path + " as " + who})
	}
}

type memoryTokens map[string]map[string]string

func (m memoryTokens) Read(_ context.Context, path string) (map[string]string, error) { return m[path], nil }
func (m memoryTokens) Write(_ context.Context, path string, data map[string]string) error {
	m[path] = data
	return nil
}
```

Add `"context"` to the fake's imports. If the SDK names a path differently (for example `DELETE /users/{name}/tokens/{name-or-id}`), follow the SDK and adjust the fake.

- [ ] **Step 3: Write the failing Sync tests**

```go
// cli/internal/forgejo/sync_test.go
package forgejo

import (
	"context"
	"reflect"
	"testing"
)

func TestSyncCreatesEverything(t *testing.T) {
	f, ep := newFakeForgejo(t)
	tokens := memoryTokens{}
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	if !f.org || !f.repo {
		t.Fatalf("org %v, repo %v", f.org, f.repo)
	}
	for name, permission := range map[string]string{"deploy": "write", "argocd": "read"} {
		if u := f.users[name]; u == nil || !u.restricted {
			t.Errorf("user %s: %+v", name, u)
		}
		if f.collaborators[name] != permission {
			t.Errorf("%s is %q on ops/homelab, want %s", name, f.collaborators[name], permission)
		}
	}
	if got := f.protection["push_whitelist_usernames"]; !reflect.DeepEqual(got, []any{"deploy"}) || f.protection["enable_push_whitelist"] != true {
		t.Errorf("protection = %v", f.protection)
	}
	for path, scope := range map[string]string{DeployPath: "write:repository", ArgoCDPath: "read:repository"} {
		token := tokens[path]["token"]
		found := false
		for _, u := range f.users {
			for _, tk := range u.tokens {
				if tk.sha1 == token && reflect.DeepEqual(tk.scopes, []string{scope}) {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("%s holds %q, not a %s token", path, token, scope)
		}
	}
}

func TestSyncTwiceWritesNothing(t *testing.T) {
	f, ep := newFakeForgejo(t)
	tokens := memoryTokens{}
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	before, deploy := f.writes, tokens[DeployPath]["token"]
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	if f.writes != before || tokens[DeployPath]["token"] != deploy {
		t.Fatalf("second Sync wrote %d times, deploy token %q → %q", f.writes-before, deploy, tokens[DeployPath]["token"])
	}
}

// Review Focus 3: OpenBao holds a token Forgejo no longer knows (Forgejo's data lost): it is replaced
func TestSyncReplacesAnInvalidToken(t *testing.T) {
	f, ep := newFakeForgejo(t)
	tokens := memoryTokens{}
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	argocd := tokens[ArgoCDPath]["token"]
	tokens[DeployPath] = map[string]string{"token": "lost"}
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	if got := tokens[DeployPath]["token"]; got == "lost" || got == "" {
		t.Fatalf("deploy token = %q", got)
	}
	if tokens[ArgoCDPath]["token"] != argocd {
		t.Fatal("a valid token was replaced")
	}
	if n := len(f.users["deploy"].tokens); n != 1 {
		t.Fatalf("deploy has %d tokens, want only the new one", n)
	}
}

func TestSyncRestoresTheProtection(t *testing.T) {
	f, ep := newFakeForgejo(t)
	tokens := memoryTokens{}
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	f.protection["push_whitelist_usernames"] = []any{"deploy", "someone"}
	f.collaborators["argocd"] = "write"
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	if got := f.protection["push_whitelist_usernames"]; !reflect.DeepEqual(got, []any{"deploy"}) {
		t.Errorf("push allowlist = %v", got)
	}
	if f.collaborators["argocd"] != "read" {
		t.Errorf("argocd is %s", f.collaborators["argocd"])
	}
}

func TestSyncRefusesAWrongAdminPassword(t *testing.T) {
	_, ep := newFakeForgejo(t)
	if err := Sync(context.Background(), ep, "wrong", memoryTokens{}); err == nil {
		t.Fatal("Sync succeeded with a wrong admin password")
	}
}
```

- [ ] **Step 4: Run them to see them fail**

Run: `direnv exec . sh -c 'cd cli && go test ./internal/forgejo/'`
Expected: FAIL (`undefined: Sync`).

- [ ] **Step 5: Implement the endpoint and Sync**

```go
// cli/internal/forgejo/endpoint.go
package forgejo

import (
	"net/http"
	"time"
)

// Endpoint is how lab reaches one environment's Forgejo: its URL, an HTTP client for the API and git's extra
// configuration (git -c values).
type Endpoint struct {
	URL  string
	HTTP *http.Client
	Git  []string
}

// ProductionEndpoint is Forgejo on the LAN Gateway, https://git.<domain>.
func ProductionEndpoint(domain string) Endpoint {
	return Endpoint{URL: "https://git." + domain, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// RepositoryURL is ops/homelab's clone URL.
func (ep Endpoint) RepositoryURL() string { return ep.URL + "/" + Org + "/" + Repo + ".git" }
```

```go
// cli/internal/forgejo/sync.go
// Package forgejo provisions the GitOps repository ops/homelab on Forgejo and deploys to it (upstream nixos's
// lab infra forgejo, without its developers team and Woodpecker client).
package forgejo

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"reflect"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
)

const (
	Org        = "ops"
	Repo       = "homelab"
	Branch     = "master"
	DeployPath = "forgejo/deploy"
	ArgoCDPath = "argocd/forgejo"
)

// Tokens is where Sync keeps the accounts' tokens (OpenBao).
type Tokens interface {
	Read(ctx context.Context, path string) (map[string]string, error)
	Write(ctx context.Context, path string, data map[string]string) error
}

type account struct {
	name       string
	permission sdk.AccessMode
	token      string
	scope      sdk.AccessTokenScope
	path       string
}

var accounts = []account{
	{name: "deploy", permission: sdk.AccessModeWrite, token: "lab", scope: sdk.AccessTokenScopeRepositoryWrite, path: DeployPath},
	{name: "argocd", permission: sdk.AccessModeRead, token: "argocd", scope: sdk.AccessTokenScopeRepositoryRead, path: ArgoCDPath},
}

// Sync makes org ops match the design: what is right is kept, what is wrong is fixed, what is missing is created.
// It waits for Forgejo until ctx ends.
func Sync(ctx context.Context, ep Endpoint, adminPassword string, tokens Tokens) error {
	if err := waitReady(ctx, ep); err != nil {
		return err
	}
	admin, err := newClient(ctx, ep, sdk.SetBasicAuth("forgejo_admin", adminPassword))
	if err != nil {
		return err
	}
	if err := ensureRepository(admin); err != nil {
		return err
	}
	for _, a := range accounts {
		if err := ensureUser(admin, a.name); err != nil {
			return err
		}
		if err := ensureCollaborator(admin, a); err != nil {
			return err
		}
	}
	if err := ensureProtection(admin); err != nil {
		return err
	}
	for _, a := range accounts {
		if err := ensureToken(ctx, ep, admin, tokens, a); err != nil {
			return err
		}
	}
	return nil
}

func newClient(ctx context.Context, ep Endpoint, auth sdk.ClientOption) (*sdk.Client, error) {
	client, err := sdk.NewClient(ep.URL, auth, sdk.SetContext(ctx), sdk.SetHTTPClient(ep.HTTP))
	if err != nil {
		return nil, fmt.Errorf("Forgejo client: %w", err)
	}
	return client, nil
}

func waitReady(ctx context.Context, ep Endpoint) error {
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.URL+"/api/healthz", nil)
		if err != nil {
			return err
		}
		if resp, err := ep.HTTP.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("Forgejo at %s is not ready: %w", ep.URL, ctx.Err())
		case <-time.After(5 * time.Second):
		}
	}
}

func notFound(resp *sdk.Response) bool { return resp != nil && resp.StatusCode == http.StatusNotFound }

func ensureRepository(c *sdk.Client) error {
	if _, resp, err := c.GetOrg(Org); err != nil {
		if !notFound(resp) {
			return fmt.Errorf("org %s: %w", Org, err)
		}
		private := sdk.VisibleTypePrivate
		if _, _, err := c.CreateOrg(sdk.CreateOrgOption{Name: Org, Visibility: private}); err != nil {
			return fmt.Errorf("create org %s: %w", Org, err)
		}
	}
	if _, resp, err := c.GetRepo(Org, Repo); err != nil {
		if !notFound(resp) {
			return fmt.Errorf("repository %s/%s: %w", Org, Repo, err)
		}
		if _, _, err := c.CreateOrgRepo(Org, sdk.CreateRepoOption{Name: Repo, Private: true, DefaultBranch: Branch}); err != nil {
			return fmt.Errorf("create repository %s/%s: %w", Org, Repo, err)
		}
	}
	return nil
}

func randomPassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ensureUser creates a restricted local user (it sees only the repositories it collaborates on) with a password
// nobody keeps: ensureToken sets a new one whenever it needs to log in as the user.
func ensureUser(c *sdk.Client, name string) error {
	user, resp, err := c.GetUserInfo(name)
	if err != nil && !notFound(resp) {
		return fmt.Errorf("user %s: %w", name, err)
	}
	if user == nil {
		password, err := randomPassword()
		if err != nil {
			return err
		}
		never := false
		private := sdk.VisibleTypePrivate
		if _, _, err := c.AdminCreateUser(sdk.CreateUserOption{
			Username: name, LoginName: name, Email: name + "@noreply.invalid", Password: password,
			MustChangePassword: &never, Visibility: &private,
		}); err != nil {
			return fmt.Errorf("create user %s: %w", name, err)
		}
	} else if user.Restricted {
		return nil
	}
	restricted := true
	if _, err := c.AdminEditUser(name, sdk.EditUserOption{LoginName: name, Restricted: &restricted}); err != nil {
		return fmt.Errorf("restrict user %s: %w", name, err)
	}
	return nil
}

func ensureCollaborator(c *sdk.Client, a account) error {
	result, resp, err := c.CollaboratorPermission(Org, Repo, a.name)
	if err != nil && !notFound(resp) {
		return fmt.Errorf("%s's permission on %s/%s: %w", a.name, Org, Repo, err)
	}
	if result != nil && result.Permission == a.permission {
		return nil
	}
	permission := a.permission
	if _, err := c.AddCollaborator(Org, Repo, a.name, sdk.AddCollaboratorOption{Permission: &permission}); err != nil {
		return fmt.Errorf("make %s a %s collaborator on %s/%s: %w", a.name, a.permission, Org, Repo, err)
	}
	return nil
}

// ensureProtection lets only deploy push to master; a protected branch cannot be deleted, and Forgejo refuses force
// pushes to it (the sandbox tests prove both).
func ensureProtection(c *sdk.Client) error {
	allow := []string{"deploy"}
	current, resp, err := c.GetBranchProtection(Org, Repo, Branch)
	if err != nil && !notFound(resp) {
		return fmt.Errorf("protection of %s: %w", Branch, err)
	}
	if current == nil {
		_, _, err := c.CreateBranchProtection(Org, Repo, sdk.CreateBranchProtectionOption{
			RuleName: Branch, EnablePush: true, EnablePushWhitelist: true, PushWhitelistUsernames: allow,
		})
		if err != nil {
			return fmt.Errorf("protect %s: %w", Branch, err)
		}
		return nil
	}
	if current.EnablePush && current.EnablePushWhitelist && reflect.DeepEqual(current.PushWhitelistUsernames, allow) &&
		len(current.PushWhitelistTeams) == 0 && !current.PushWhitelistDeployKeys {
		return nil
	}
	on, off := true, false
	_, _, err = c.EditBranchProtection(Org, Repo, Branch, sdk.EditBranchProtectionOption{
		EnablePush: &on, EnablePushWhitelist: &on, PushWhitelistUsernames: allow, PushWhitelistTeams: []string{}, PushWhitelistDeployKeys: &off,
	})
	if err != nil {
		return fmt.Errorf("restore the protection of %s: %w", Branch, err)
	}
	return nil
}

// valid tells whether token still reads ops/homelab (both accounts' scopes allow it; GET /user needs read:user).
func valid(ctx context.Context, ep Endpoint, token string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.URL+"/api/v1/repos/"+Org+"/"+Repo, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "token "+token)
	resp, err := ep.HTTP.Do(req)
	if err != nil {
		return false, fmt.Errorf("check a token against Forgejo: %w", err)
	}
	_ = resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return false, nil
	}
	return false, fmt.Errorf("check a token against Forgejo: %s", resp.Status)
}

func ensureToken(ctx context.Context, ep Endpoint, admin *sdk.Client, tokens Tokens, a account) error {
	stored, err := tokens.Read(ctx, a.path)
	if err != nil {
		return err
	}
	if token := stored["token"]; token != "" {
		ok, err := valid(ctx, ep, token)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	password, err := randomPassword()
	if err != nil {
		return err
	}
	never := false
	if _, err := admin.AdminEditUser(a.name, sdk.EditUserOption{LoginName: a.name, Password: password, MustChangePassword: &never}); err != nil {
		return fmt.Errorf("reset %s's password: %w", a.name, err)
	}
	user, err := newClient(ctx, ep, sdk.SetBasicAuth(a.name, password))
	if err != nil {
		return err
	}
	existing, _, err := user.ListAccessTokens(a.name, sdk.ListAccessTokensOptions{})
	if err != nil {
		return fmt.Errorf("list %s's tokens: %w", a.name, err)
	}
	for _, t := range existing {
		if t.Name == a.token {
			if _, err := user.DeleteAccessToken(a.name, t.ID); err != nil {
				return fmt.Errorf("delete %s's old token: %w", a.name, err)
			}
		}
	}
	created, _, err := user.CreateAccessToken(a.name, sdk.CreateAccessTokenOption{Name: a.token, Scopes: []sdk.AccessTokenScope{a.scope}})
	if err != nil {
		return fmt.Errorf("create %s's token: %w", a.name, err)
	}
	if created.Token == "" {
		return fmt.Errorf("Forgejo returned an empty token for %s", a.name)
	}
	return tokens.Write(ctx, a.path, map[string]string{"username": a.name, "token": created.Token})
}
```

Notes for the implementer: `sdk.CreateOrgOption`'s visibility field may be a plain `VisibleType` or a pointer, and `ListAccessTokens`/`DeleteAccessToken` signatures are as listed in the SDK (`user_app.go`); adjust to the SDK, keep the behaviour. `AdminEditUser` requires `LoginName` (Forgejo validates it); the SDK's `SourceID` 0 means a local user.

- [ ] **Step 6: Run the tests**

Run: `direnv exec . sh -c 'cd cli && go test -race ./internal/forgejo/'`
Expected: `ok`.

- [ ] **Step 7: Basic checks and commit**

Run: `direnv exec . scripts/ci-check all` — Expected: exit 0.

```bash
git add cli/go.mod cli/go.sum cli/internal/forgejo
git commit -m "feat(cli): forgejo.Sync provisions org ops (deploy and argocd, master's protection, tokens)"
```

---

### Task 4: push, the commands `sync` and `push`, and the sandbox endpoint

**Files:**
- Create: `cli/internal/forgejo/push.go`, `cli/internal/forgejo/push_test.go`
- Modify: `cli/internal/forgejo/endpoint.go` (sandbox endpoint), `cli/internal/cli/infra_forgejo.go` (sync, push), `cli/internal/cli/infra_forgejo_test.go` (create)

**Interfaces:**
- Consumes: `env.Env`, `forgejoEnv`, `verifyProduction` (Task 1); `secrets.Open`, `(*Store).Read/Close` (Task 2); `forgejo.Sync`, `Endpoint`, `ProductionEndpoint`, `ArgoCDPath`, `DeployPath` (Task 3).
- Produces: `forgejo.Deployed(ctx, ep, root, token string) (string, error)` (Forgejo's `master`, "" when empty); `forgejo.Push(ctx, ep, root, token, commit string) error`; `forgejo.SandboxCommit(ctx, ep, root, token string) (string, error)`; `forgejo.SandboxEndpoint(domain, node string, caPEM []byte) (Endpoint, func(), error)`; `refreshArgoCDCredential(ctx, kube kubernetes.Interface, dyn dynamic.Interface, token string, timeout time.Duration) error`.

- [ ] **Step 1: Write the failing push tests**

```go
// cli/internal/forgejo/push_test.go
package forgejo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// local is an Endpoint whose repository is a bare repository on disk (file://), and a checkout with one commit.
func local(t *testing.T) (Endpoint, string, string) {
	t.Helper()
	server := t.TempDir()
	run(t, t.TempDir(), "init", "-q", "--bare", "-b", "master", filepath.Join(server, "ops", "homelab.git"))
	checkout := t.TempDir()
	run(t, checkout, "init", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(checkout, "file"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, checkout, "add", "file")
	run(t, checkout, "commit", "-q", "-m", "one")
	return Endpoint{URL: "file://" + server}, checkout, run(t, checkout, "rev-parse", "HEAD")
}

func TestPush(t *testing.T) {
	ep, checkout, head := local(t)
	ctx := context.Background()
	if got, err := Deployed(ctx, ep, checkout, "secret-token"); err != nil || got != "" {
		t.Fatalf("Deployed() on an empty repository = %q, %v", got, err)
	}
	if err := Push(ctx, ep, checkout, "secret-token", head); err != nil {
		t.Fatal(err)
	}
	if got, err := Deployed(ctx, ep, checkout, "secret-token"); err != nil || got != head {
		t.Fatalf("Deployed() = %q, %v; want %s", got, err, head)
	}
	if err := Push(ctx, ep, checkout, "secret-token", head); err != nil {
		t.Fatalf("pushing the deployed commit again: %v", err)
	}
}

func TestPushNeverForces(t *testing.T) {
	ep, checkout, head := local(t)
	ctx := context.Background()
	if err := Push(ctx, ep, checkout, "t", head); err != nil {
		t.Fatal(err)
	}
	run(t, checkout, "commit", "-q", "--amend", "-m", "rewritten")
	if err := Push(ctx, ep, checkout, "t", run(t, checkout, "rev-parse", "HEAD")); err == nil {
		t.Fatal("a non-fast-forward push succeeded")
	}
}

// Review Focus 4: the sandbox's ops/homelab holds a commit that is not an ancestor of HEAD (a rebased branch)
func TestSandboxCommitFastForwards(t *testing.T) {
	ep, checkout, _ := local(t)
	ctx := context.Background()
	first, err := SandboxCommit(ctx, ep, checkout, "t")
	if err != nil {
		t.Fatal(err)
	}
	if err := Push(ctx, ep, checkout, "t", first); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "file"), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, checkout, "commit", "-q", "--amend", "-am", "rebased")
	second, err := SandboxCommit(ctx, ep, checkout, "t")
	if err != nil {
		t.Fatal(err)
	}
	if err := Push(ctx, ep, checkout, "t", second); err != nil {
		t.Fatalf("the sandbox push after a rebase: %v", err)
	}
	if got, want := run(t, checkout, "rev-parse", second+"^{tree}"), run(t, checkout, "rev-parse", "HEAD^{tree}"); got != want {
		t.Fatalf("the sandbox commit's tree %s is not HEAD's %s", got, want)
	}
	if got := run(t, checkout, "rev-parse", second+"^"); got != first {
		t.Fatalf("the sandbox commit's parent %s is not the deployed %s", got, first)
	}
}

func TestGitKeepsTheTokenOutOfArguments(t *testing.T) {
	ep := Endpoint{URL: "https://git.example.dev", Git: []string{"http.sslCAInfo=/ca.crt"}}
	cmd, cleanup, err := ep.git(context.Background(), t.TempDir(), "secret-token", "push", ep.RepositoryURL(), "HEAD:refs/heads/master")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if strings.Contains(strings.Join(cmd.Args, " "), "secret-token") {
		t.Fatalf("the token is in git's arguments: %v", cmd.Args)
	}
	var askpass string
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "GIT_ASKPASS=") {
			askpass = strings.TrimPrefix(e, "GIT_ASKPASS=")
		}
	}
	for prompt, want := range map[string]string{"Username for 'https://git.example.dev': ": "deploy", "Password for 'https://deploy@git.example.dev': ": "secret-token"} {
		// The askpass reads the token from the environment git passes it
		c := exec.Command(askpass, prompt)
		c.Env = cmd.Env
		out, err := c.Output()
		if err != nil || strings.TrimSpace(string(out)) != want {
			t.Errorf("askpass %q = %q, %v; want %q", prompt, out, err, want)
		}
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `direnv exec . sh -c 'cd cli && go test ./internal/forgejo/ -run "Push|Sandbox|Git"'`
Expected: FAIL (`undefined: Push`).

- [ ] **Step 3: Implement push**

```go
// cli/internal/forgejo/push.go
package forgejo

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// git runs git in dir as deploy with token, through an askpass script that reads the token from the environment:
// it reaches neither git's arguments nor its output.
func (ep Endpoint) git(ctx context.Context, dir, token string, args ...string) (*exec.Cmd, func(), error) {
	temp, err := os.MkdirTemp("", "lab-git-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(temp) }
	askpass := filepath.Join(temp, "askpass")
	script := "#!/bin/sh\ncase \"$1\" in\n  Username*) echo deploy ;;\n  *) printf '%s\\n' \"$LAB_FORGEJO_TOKEN\" ;;\nesac\n"
	if err := os.WriteFile(askpass, []byte(script), 0o700); err != nil {
		cleanup()
		return nil, nil, err
	}
	full := []string{"-C", dir, "-c", "credential.helper="}
	for _, c := range ep.Git {
		full = append(full, "-c", c)
	}
	cmd := exec.CommandContext(ctx, "git", append(full, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS="+askpass, "LAB_FORGEJO_TOKEN="+token,
		"GIT_AUTHOR_NAME=lab", "GIT_AUTHOR_EMAIL=lab@noreply.invalid", "GIT_COMMITTER_NAME=lab", "GIT_COMMITTER_EMAIL=lab@noreply.invalid")
	return cmd, cleanup, nil
}

func (ep Endpoint) output(ctx context.Context, dir, token string, args ...string) (string, error) {
	cmd, cleanup, err := ep.git(ctx, dir, token, args...)
	if err != nil {
		return "", err
	}
	defer cleanup()
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if exit, ok := err.(*exec.ExitError); ok {
			stderr = strings.TrimSpace(string(exit.Stderr))
		}
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, stderr)
	}
	return strings.TrimSpace(string(out)), nil
}

// Deployed is ops/homelab's master on Forgejo, or "" while the repository is empty.
func Deployed(ctx context.Context, ep Endpoint, root, token string) (string, error) {
	out, err := ep.output(ctx, root, token, "ls-remote", ep.RepositoryURL(), "refs/heads/"+Branch)
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", nil
	}
	return strings.Fields(out)[0], nil
}

// Push publishes commit as ops/homelab's master, never forced; it does nothing when master already is commit.
func Push(ctx context.Context, ep Endpoint, root, token, commit string) error {
	deployed, err := Deployed(ctx, ep, root, token)
	if err != nil {
		return err
	}
	if deployed == commit {
		return nil
	}
	_, err = ep.output(ctx, root, token, "push", "--quiet", ep.RepositoryURL(), commit+":refs/heads/"+Branch)
	return err
}

// SandboxCommit is a commit with HEAD's tree on top of the sandbox's deployed master (none while it is empty), so
// the sandbox push is a fast-forward even after a rebase. The sandbox is not production: it only needs the content.
func SandboxCommit(ctx context.Context, ep Endpoint, root, token string) (string, error) {
	head, err := ep.output(ctx, root, token, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	deployed, err := Deployed(ctx, ep, root, token)
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", "HEAD^{tree}", "-m", "sandbox: " + head}
	if deployed != "" {
		if _, err := ep.output(ctx, root, token, "fetch", "--quiet", ep.RepositoryURL(), Branch); err != nil {
			return "", err
		}
		tree, err := ep.output(ctx, root, token, "rev-parse", deployed+"^{tree}")
		if err != nil {
			return "", err
		}
		headTree, err := ep.output(ctx, root, token, "rev-parse", "HEAD^{tree}")
		if err != nil {
			return "", err
		}
		if tree == headTree {
			return deployed, nil
		}
		args = append(args, "-p", deployed)
	}
	return ep.output(ctx, root, token, args...)
}
```

- [ ] **Step 4: Run the push tests**

Run: `direnv exec . sh -c 'cd cli && go test -race ./internal/forgejo/'`
Expected: `ok`.

- [ ] **Step 5: Add the sandbox endpoint**

Append to `cli/internal/forgejo/endpoint.go`:

```go
// SandboxEndpoint reaches the sandbox's Forgejo on the k3d node (as the tests do: GitHub Actions runs no localhost
// proxy) and trusts the sandbox CA. The cleanup removes the CA file git reads.
func SandboxEndpoint(domain, node string, caPEM []byte) (Endpoint, func(), error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return Endpoint{}, nil, fmt.Errorf("the sandbox CA is not a PEM certificate")
	}
	file, err := os.CreateTemp("", "lab-sandbox-ca-")
	if err != nil {
		return Endpoint{}, nil, err
	}
	cleanup := func() { _ = os.Remove(file.Name()) }
	if _, err := file.Write(caPEM); err != nil {
		_ = file.Close()
		cleanup()
		return Endpoint{}, nil, err
	}
	_ = file.Close()
	host := "git." + domain
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if h, port, err := net.SplitHostPort(addr); err == nil && h == host {
				addr = net.JoinHostPort(node, port)
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}
	return Endpoint{
		URL:  "https://" + host,
		HTTP: &http.Client{Timeout: 30 * time.Second, Transport: transport},
		Git:  []string{"http.curloptResolve=" + host + ":443:" + node, "http.sslCAInfo=" + file.Name()},
	}, cleanup, nil
}
```

(imports: `context`, `crypto/tls`, `crypto/x509`, `fmt`, `net`, `os`.)

- [ ] **Step 6: Write the failing credential refresh test**

```go
// cli/internal/cli/infra_forgejo_test.go
package cli

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func externalSecret() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("external-secrets.io/v1")
	u.SetKind("ExternalSecret")
	u.SetNamespace("argocd")
	u.SetName("argocd-repo-forgejo")
	return u
}

func TestRefreshArgoCDCredential(t *testing.T) {
	kube := fake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: "argocd-repo-forgejo"},
		Data:       map[string][]byte{"password": []byte("old")},
	})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{externalSecrets: "ExternalSecretList"}, externalSecret())
	// External Secrets answers the force-sync annotation by rewriting the Secret
	dyn.PrependReactor("patch", "externalsecrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		_, err := kube.CoreV1().Secrets("argocd").Update(context.Background(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: "argocd-repo-forgejo"},
			Data:       map[string][]byte{"password": []byte("new")},
		}, metav1.UpdateOptions{})
		return true, externalSecret(), err
	})
	if err := refreshArgoCDCredential(context.Background(), kube, dyn, "new", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	patched := false
	for _, a := range dyn.Actions() {
		patched = patched || a.GetVerb() == "patch"
	}
	if !patched {
		t.Fatal("the ExternalSecret was not force-synced")
	}
}

func TestRefreshArgoCDCredentialSkipsACurrentSecret(t *testing.T) {
	kube := fake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: "argocd-repo-forgejo"},
		Data:       map[string][]byte{"password": []byte("same")},
	})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{externalSecrets: "ExternalSecretList"}, externalSecret())
	if err := refreshArgoCDCredential(context.Background(), kube, dyn, "same", time.Second); err != nil {
		t.Fatal(err)
	}
	if n := len(dyn.Actions()); n != 0 {
		t.Fatalf("%d calls for a current Secret", n)
	}
}

func TestRefreshArgoCDCredentialTimesOut(t *testing.T) {
	kube := fake.NewClientset()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{externalSecrets: "ExternalSecretList"}, externalSecret())
	if err := refreshArgoCDCredential(context.Background(), kube, dyn, "new", 2*time.Second); err == nil {
		t.Fatal("no error while the Secret never got the token")
	}
}
```

(also import `k8s.io/apimachinery/pkg/runtime/schema`.)

- [ ] **Step 7: Implement `sync` and `push` in the CLI**

Add to `cli/internal/cli/infra_forgejo.go` (and its imports: `os/exec`, `strings`, `encoding/json`, `k8s.io/apimachinery/pkg/types`, `k8s.io/apimachinery/pkg/runtime/schema`, `apierrors "k8s.io/apimachinery/pkg/api/errors"`, `metav1`, `k8s.io/client-go/dynamic`, `k8s.io/client-go/kubernetes`, `k8s.io/client-go/tools/clientcmd`, the `forgejo` and `secrets` packages):

```go
var externalSecrets = schema.GroupVersionResource{Group: "external-secrets.io", Version: "v1", Resource: "externalsecrets"}

// session is what sync and push need: the environment, its cluster, OpenBao and Forgejo's endpoint.
type session struct {
	env     env.Env
	kube    kubernetes.Interface
	dyn     dynamic.Interface
	store   *secrets.Store
	ep      forgejo.Endpoint
	cleanup func()
}

func openSession(ctx context.Context, e env.Env) (*session, error) {
	config, err := clientcmd.BuildConfigFromFlags("", e.Kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("kubeconfig %s: %w", e.Kubeconfig, err)
	}
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	ep, cleanup := forgejo.ProductionEndpoint(e.Domain), func() {}
	if e.Name == "sandbox" {
		if ep, cleanup, err = sandboxEndpoint(ctx, kube, e.Domain); err != nil {
			return nil, err
		}
	}
	store, err := secrets.Open(ctx, config)
	if err != nil {
		cleanup()
		return nil, err
	}
	return &session{env: e, kube: kube, dyn: dyn, store: store, ep: ep, cleanup: cleanup}, nil
}

func (s *session) close(ctx context.Context) error {
	defer s.cleanup()
	return s.store.Close(ctx)
}

// sandboxEndpoint dials the k3d node and trusts the CA trust-manager copies into every namespace.
func sandboxEndpoint(ctx context.Context, kube kubernetes.Interface, domain string) (forgejo.Endpoint, func(), error) {
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", "k3d-homelab-dev-server-0").Output()
	if err != nil {
		return forgejo.Endpoint{}, nil, fmt.Errorf("docker inspect k3d-homelab-dev-server-0: %w (is the sandbox running?)", err)
	}
	ca, err := kube.CoreV1().ConfigMaps("forgejo").Get(ctx, "homelab-dev-ca", metav1.GetOptions{})
	if err != nil {
		return forgejo.Endpoint{}, nil, fmt.Errorf("the sandbox CA (forgejo/homelab-dev-ca): %w", err)
	}
	return forgejo.SandboxEndpoint(domain, strings.TrimSpace(string(out)), []byte(ca.Data["ca.crt"]))
}

// refreshArgoCDCredential makes Argo CD's repository Secret hold token: it force-syncs the ExternalSecret and
// waits, so the bootstrap that follows (make infra) can already switch to Forgejo.
func refreshArgoCDCredential(ctx context.Context, kube kubernetes.Interface, dyn dynamic.Interface, token string, timeout time.Duration) error {
	current := func() (bool, error) {
		secret, err := kube.CoreV1().Secrets("argocd").Get(ctx, "argocd-repo-forgejo", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return string(secret.Data["password"]) == token, nil
	}
	if ok, err := current(); err != nil || ok {
		return err
	}
	patch, _ := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{"force-sync": time.Now().Format(time.RFC3339Nano)}}})
	if _, err := dyn.Resource(externalSecrets).Namespace("argocd").Patch(ctx, "argocd-repo-forgejo", types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("force-sync argocd/argocd-repo-forgejo: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for {
		ok, err := current()
		if err != nil || ok {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("argocd/argocd-repo-forgejo did not get the new token within %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
```

Then, inside `newInfraForgejoCmd`, before `group.AddCommand(verify)`:

```go
	sync := &cobra.Command{
		Use:   "sync",
		Short: "Provision org ops on Forgejo: ops/homelab, deploy and argocd, master's protection, their tokens in OpenBao",
		Long:  "Idempotent: what is right is kept, what is wrong is fixed, what is missing is created. Waits up to 30 minutes for Forgejo. No secret is printed.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := forgejoEnv(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 35*time.Minute)
			defer cancel()
			s, err := openSession(ctx, e)
			if err != nil {
				return err
			}
			defer func() { _ = s.close(context.Background()) }()
			admin, err := s.kube.CoreV1().Secrets("forgejo").Get(ctx, "forgejo-admin", metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("forgejo/forgejo-admin: %w", err)
			}
			syncCtx, syncCancel := context.WithTimeout(ctx, 30*time.Minute)
			defer syncCancel()
			if err := forgejo.Sync(syncCtx, s.ep, string(admin.Data["password"]), s.store); err != nil {
				return err
			}
			argocd, err := s.store.Read(ctx, forgejo.ArgoCDPath)
			if err != nil {
				return err
			}
			if err := refreshArgoCDCredential(ctx, s.kube, s.dyn, argocd["token"], 5*time.Minute); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Forgejo's org ops is in sync.")
			return err
		},
	}
	push := &cobra.Command{
		Use:   "push",
		Short: "Deploy: push HEAD to ops/homelab's master as deploy",
		Long:  "Production: only after verify's guards pass, never forced (roll back with a revert through a pull request). Sandbox: a commit with HEAD's tree on top of what the sandbox's Forgejo holds.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := forgejoEnv(cmd)
			if err != nil {
				return err
			}
			var commit string
			if e.Name == "production" {
				if commit, err = verifyProduction(cmd.Context(), e); err != nil {
					return err
				}
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			s, err := openSession(ctx, e)
			if err != nil {
				return err
			}
			defer func() { _ = s.close(context.Background()) }()
			deploy, err := s.store.Read(ctx, forgejo.DeployPath)
			if err != nil {
				return err
			}
			if deploy["token"] == "" {
				return fmt.Errorf("no deploy token in OpenBao (secret/%s): run lab infra forgejo sync first", forgejo.DeployPath)
			}
			if commit == "" {
				if commit, err = forgejo.SandboxCommit(ctx, s.ep, e.Root, deploy["token"]); err != nil {
					return err
				}
			}
			deployed, err := forgejo.Deployed(ctx, s.ep, e.Root, deploy["token"])
			if err != nil {
				return err
			}
			if deployed == commit {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "ops/homelab's master already is %.12s: nothing to push.\n", commit)
				return err
			}
			if err := forgejo.Push(ctx, s.ep, e.Root, deploy["token"], commit); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Deployed %.12s to ops/homelab.\n", commit)
			return err
		},
	}
	group.AddCommand(sync, push)
```

- [ ] **Step 8: Run the CLI tests**

Run: `direnv exec . sh -c 'cd cli && go test -race ./...'`
Expected: `ok` for every package.

- [ ] **Step 9: Try `sync` and `push` against the sandbox**

Run (from the main checkout, on this branch, sandbox up): `direnv exec . sh -c 'cd cli && go run ./cmd/lab infra forgejo sync --env sandbox && go run ./cmd/lab infra forgejo sync --env sandbox && go run ./cmd/lab infra forgejo push --env sandbox && go run ./cmd/lab infra forgejo push --env sandbox'`
Expected: two "in sync" lines, "Deployed …", then "already is … nothing to push". No token in the output.

- [ ] **Step 10: Basic checks and commit**

Run: `direnv exec . scripts/ci-check all` — Expected: exit 0.

```bash
git add cli
git commit -m "feat(cli): lab infra forgejo sync and push (deploy's token, sandbox endpoint, Argo CD's credential refreshed)"
```

---

### Task 5: the cluster side: no seed Job, Argo CD's read-only token, choose-values

**Files:**
- Delete: `platform/forgejo/templates/seed-job.yaml`, `platform/forgejo/files/seed.sh`
- Modify: `platform/forgejo/values.yaml` (the comment "restated for templates/seed-job.yaml"), `system/argocd-config/templates/forgejo-repository.yaml`, `platform/global-secrets/values.yaml`, `infra/_modules/argocd/choose-values.sh`
- Test: `test/ci_scripts_test.go` (`TestArgoCDChooseValues`)

**Interfaces:**
- Consumes: OpenBao path `argocd/forgejo` with properties `username`, `token` (Task 3).
- Produces: `choose-values.sh` contract unchanged (JSON query in, `{values, environment}` out).

- [ ] **Step 1: Rewrite the choose-values test (failing)**

Replace `TestArgoCDChooseValues` in `test/ci_scripts_test.go`:

```go
// choose-values.sh picks the values file the Argo CD bootstrap applies: the seed on a first install until Argo CD's
// own credential reads ops/homelab's master (Forgejo runs, lab synced the token and pushed), never again once Argo CD
// reads Forgejo, and never because the cluster could not be asked
func TestArgoCDChooseValues(t *testing.T) {
	const forgejo = "http://forgejo-http.forgejo:3000/ops/homelab"
	const seedValues = `{"values":"values-seed.yaml","environment":"seed"}`
	const forgejoValues = `{"values":"values-dev.yaml","environment":"dev"}`
	for _, c := range []struct {
		name, seed, rootRepo, token, master, want string
		kubectlFails, execFails, fails           bool
	}{
		{name: "sandbox: no seed", want: forgejoValues, kubectlFails: true},
		{name: "first install, no credential yet", seed: "values-seed.yaml", want: seedValues},
		{name: "credential, nothing pushed", seed: "values-seed.yaml", token: "t1", want: seedValues},
		{name: "credential reads master: switch", seed: "values-seed.yaml", rootRepo: "https://github.com/example/homelab", token: "t1", master: "t1", want: forgejoValues},
		{name: "wrong credential", seed: "values-seed.yaml", token: "t1", master: "t2", want: seedValues},
		// Review Focus 5: the Secret exists but Forgejo's pod is down
		{name: "Forgejo down", seed: "values-seed.yaml", token: "t1", master: "t1", execFails: true, want: seedValues},
		{name: "switched, Forgejo down: stay", seed: "values-seed.yaml", rootRepo: forgejo, execFails: true, want: forgejoValues},
		{name: "cluster unreachable", seed: "values-seed.yaml", kubectlFails: true, fails: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			kubectl := `case "$*" in
  *applicationset*) echo "$ROOT_REPO" ;;
  *"get secret argocd-repo-forgejo"*) [ -z "$TOKEN" ] || printf '%s' "$TOKEN" | base64 ;;
  *exec*) [ -z "$EXEC_FAILS" ] || exit 1; [ "$(cat)" = "$MASTER" ] && [ -n "$MASTER" ] ;;
  *) echo "unexpected kubectl $*" >&2; exit 2 ;;
esac`
			if c.kubectlFails {
				kubectl = `echo "unable to connect" >&2; exit 1`
			}
			_, env := fakeBin(t, map[string]string{"kubectl": kubectl})
			cmd := exec.Command("sh", "../infra/_modules/argocd/choose-values.sh")
			cmd.Env = append(env, "ROOT_REPO="+c.rootRepo, "TOKEN="+c.token, "MASTER="+c.master)
			if c.execFails {
				cmd.Env = append(cmd.Env, "EXEC_FAILS=1")
			}
			cmd.Stdin = strings.NewReader(`{"values":"values-dev.yaml","environment":"dev","seed":"` + c.seed + `","forgejo_repository":"` + forgejo + `","kubeconfig":"/dev/null"}`)
			out, err := cmd.CombinedOutput()
			if c.fails {
				require.Error(t, err, string(out))
				return
			}
			require.NoError(t, err, string(out))
			assert.JSONEq(t, c.want, string(out))
		})
	}
}
```

Run: `direnv exec . sh -c 'cd test && go test -count=1 -run TestArgoCDChooseValues ./...'`
Expected: FAIL (the script still asks for pods: `unexpected kubectl get pods …`).

- [ ] **Step 2: Change choose-values.sh**

Replace the block from `if [ "$root" != "$(get forgejo_repository)" ]; then` to its `fi` with:

```sh
	if [ "$root" != "$(get forgejo_repository)" ]; then
		# Argo CD's own credential (lab infra forgejo sync's read-only token) reads ops/homelab's master: Forgejo
		# runs, the token exists and lab infra forgejo push deployed. The token goes through stdin, not an argument.
		token=$(kubectl -n argocd get secret argocd-repo-forgejo --ignore-not-found -o jsonpath='{.data.password}')
		if [ -z "$token" ] || ! printf '%s' "$token" | base64 -d | kubectl -n forgejo exec -i deploy/forgejo -c forgejo -- \
			sh -c 'curl -sf -o /dev/null -H "Authorization: token $(cat)" http://localhost:3000/api/v1/repos/ops/homelab/branches/master' \
			>/dev/null 2>&1; then
			values=$seed
			environment=seed
		fi
	fi
```

Update the header comment: "the seed only until Argo CD's own credential reads ops/homelab's master on Forgejo, and never again once Argo CD reads Forgejo".

Run the test again — Expected: PASS. Run `direnv exec . scripts/lint` — Expected: exit 0 (shellcheck: the `$(cat)` sits in single quotes on purpose; add `# shellcheck disable=SC2016` above the `if` if shellcheck flags it).

- [ ] **Step 3: Argo CD's credential and the admin password's destinations**

Replace `system/argocd-config/templates/forgejo-repository.yaml` with:

```yaml
{{- /* Skipped by the offline bootstrap render, when external-secrets does not exist yet */}}
{{- if .Capabilities.APIVersions.Has "external-secrets.io/v1/ExternalSecret" }}
# Argo CD reads the GitOps repository on Forgejo with the read-only token lab infra forgejo sync writes (production;
# in the sandbox, which reads GitHub, the tests read it)
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: argocd-repo-forgejo
  # Argo CD's namespace (the root ApplicationSet deploys this chart there too)
  namespace: argocd
spec:
  secretStoreRef:
    kind: SecretStore
    name: openbao
  target:
    template:
      engineVersion: v2
      metadata:
        labels:
          argocd.argoproj.io/secret-type: repository
      data:
        type: git
        url: http://forgejo-http.forgejo:3000/ops/homelab
        username: {{` "{{ .username }}" `}}
        password: {{` "{{ .token }}" `}}
  data:
    - secretKey: username
      remoteRef:
        key: argocd/forgejo
        property: username
    - secretKey: token
      remoteRef:
        key: argocd/forgejo
        property: token
{{- end }}
```

In `platform/global-secrets/values.yaml`, `forgejo.admin` loses the comment "Argo CD reads the GitOps repository with it" and its `destinations` become `[forgejo/generated/admin]`.

- [ ] **Step 4: Drop the seed Job**

Run: `git rm platform/forgejo/templates/seed-job.yaml platform/forgejo/files/seed.sh` and, in `platform/forgejo/values.yaml`, replace "# Chart defaults, restated for templates/seed-job.yaml" with "# The chart defaults, pinned". Check nothing else names it: `grep -rn 'seed-job\|seed.sh\|forgejo-seed' --exclude-dir=site --exclude-dir=superpowers .` — Expected: only `test/forgejo_test.go` (rewritten in Task 7) and docs (Task 8).

- [ ] **Step 5: Render and check**

Run: `direnv exec . sh -c 'scripts/render-chart system/argocd-config | yq "select(.kind == \"ExternalSecret\") | .spec.data[].remoteRef.key"; scripts/check-secret-stores'`
Expected: `argocd/forgejo` twice; check-secret-stores 0 failures (`argocd/forgejo` is not a `generated/` key, so it needs no pusher).

- [ ] **Step 6: Basic checks and commit**

Run: `direnv exec . scripts/ci-check all` — Expected: exit 0.

```bash
git add -A platform/forgejo platform/global-secrets system/argocd-config infra/_modules/argocd/choose-values.sh test/ci_scripts_test.go
git commit -m "feat: no seed Job; Argo CD reads ops/homelab with lab's read-only token; the bootstrap switches once that token reads master"
```

---

### Task 6: `make infra env=production` deploys

**Files:**
- Modify: `Makefile`
- Test: `test/ci_scripts_test.go` (replace `TestMakeInfraProductionCheckout` with `TestMakeInfraProduction`), `scripts/ci-check` (offline list)

**Interfaces:**
- Consumes: `lab infra forgejo verify|sync|push` (Tasks 1, 4).
- Produces: Makefile variables `lab_cli` (default `cd cli && go run ./cmd/lab`) and `terragrunt` (default `terragrunt`), overridable by the test.

- [ ] **Step 1: Write the failing Makefile test**

Replace `TestMakeInfraProductionCheckout` in `test/ci_scripts_test.go`:

```go
// make infra env=production deploys in one command: the guards, the infrastructure, Forgejo's org ops, the push, then
// the Argo CD bootstrap again (the switch to Forgejo on a first install); force=1 applies the infrastructure only
func TestMakeInfraProduction(t *testing.T) {
	stubs := map[string]string{
		"lab":        `echo "lab $*" >> "$FIXTURES/calls"; [ "$*" != "infra forgejo verify" ] || [ -z "$VERIFY_FAILS" ]`,
		"terragrunt": `echo "terragrunt $(basename "$PWD") $*" >> "$FIXTURES/calls"`,
		"go":         `exit 0`,
	}
	for _, c := range []struct {
		name string
		vars []string
		env  []string
		want []string
	}{
		{"deploy", nil, nil, []string{
			"lab infra forgejo verify",
			"terragrunt production run --all apply --non-interactive -- -auto-approve",
			"lab infra forgejo sync",
			"lab infra forgejo push",
			"terragrunt argocd apply --non-interactive -- -auto-approve",
		}},
		{"force=1", []string{"force=1"}, nil, []string{
			"terragrunt production run --all apply --non-interactive -- -auto-approve",
		}},
		{"guards fail", nil, []string{"VERIFY_FAILS=1"}, []string{"lab infra forgejo verify"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, env := fakeBin(t, stubs)
			args := append([]string{"-s", "-C", "..", "infra", "env=production", "lab_cli=" + filepath.Join(dir, "lab"), "terragrunt=" + filepath.Join(dir, "terragrunt")}, c.vars...)
			cmd := exec.Command("make", args...)
			cmd.Env = append(env, c.env...)
			out, _ := cmd.CombinedOutput()
			calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
			assert.Equal(t, c.want, strings.Split(strings.TrimSpace(string(calls)), "\n"), string(out))
		})
	}
}
```

In `scripts/ci-check`, replace `TestMakeInfraProductionCheckout` with `TestMakeInfraProduction` in `offline_tests`.

Run: `direnv exec . sh -c 'cd test && go test -count=1 -run TestMakeInfraProduction ./...'`
Expected: FAIL (the Makefile runs the real `go run` and `terragrunt`, or no `lab` calls).

- [ ] **Step 2: Change the Makefile**

Replace the variable `lab = cd cli && go run ./cmd/lab test scope` with:

```make
lab_cli = cd cli && go run ./cmd/lab
lab = $(lab_cli) test scope
terragrunt = terragrunt
terragrunt_apply = $(terragrunt) run --all apply --non-interactive -- -auto-approve
```

Delete the `production-checkout` target and its comment. Replace the `infra` target with:

```make
# Every unit of infra/<env>: Argo CD's bootstrap, OpenBao's key, OpenTofu's secrets (the sandbox's run in make dev).
# Production deploys in the same command: the guards (origin/master, checks, the merged pull request's sandbox), the
# infrastructure, Forgejo's org ops, the push of this commit to ops/homelab, then the bootstrap again (the switch to
# Forgejo on a first install). force=1 applies the infrastructure only and deploys nothing (a state migration).
# Building lab first keeps a cold Go build out of credential_process, which the AWS SDK times out after a minute.
infra:
	@if [ "$(env)" = sandbox ]; then $(MAKE) -s sandbox-checkout; fi
	go -C cli build -o /dev/null ./cmd/lab
	@if [ "$(env)" = production ] && [ "$(force)" != 1 ]; then $(lab_cli) infra forgejo verify; fi
	cd infra/$(env) && $(terragrunt_apply)
	@if [ "$(env)" = production ] && [ "$(force)" != 1 ]; then \
		$(lab_cli) infra forgejo sync && $(lab_cli) infra forgejo push && \
		(cd infra/production/argocd && $(terragrunt) apply --non-interactive -- -auto-approve); \
	fi
```

Note: `lab_cli` starts with `cd cli &&`; when the test overrides it with an absolute stub path the `cd` disappears, which is fine.

- [ ] **Step 3: Run the test and the other Makefile tests**

Run: `direnv exec . sh -c 'cd test && go test -count=1 -run "TestMakeInfraProduction|TestMakeDevRevision" ./...'`
Expected: PASS. Then `make -n dev tests=Smoke | grep -c "test scope"` — Expected: 2 (make dev still uses `$(lab)`).

- [ ] **Step 4: Basic checks and commit**

Run: `direnv exec . scripts/ci-check all` — Expected: exit 0.

```bash
git add Makefile test/ci_scripts_test.go scripts/ci-check
git commit -m "feat: make infra env=production deploys (verify, apply, sync, push, the bootstrap again); force=1 applies only"
```

---

### Task 7: sandbox tests

**Files:**
- Rewrite: `test/forgejo_test.go`
- Modify: `test/scopes.yaml` (the Forgejo group: `cli/internal/forgejo/**`, `cli/internal/secrets/**`, `cli/internal/cli/infra_forgejo.go` select `Forgejo`; the offline `cli/internal/env/**`, `cli/internal/guard/**` are covered by `make -C cli test` and go to `skip`)

**Interfaces:**
- Consumes: `lab infra forgejo sync|push --env sandbox` (Task 4); the cluster side (Task 5).

- [ ] **Step 1: Write the sandbox tests**

```go
// test/forgejo_test.go
package test

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
	// The tests read OpenBao the documented way: an admin login inside OpenBao's pod, the token never printed
	script := "kubectl -n openbao create token openbao-admin --audience=openbao --duration=10m | kubectl -n openbao exec -i openbao-0 -c openbao -- " +
		"sh -c 'export BAO_TOKEN=$(bao write -field=token auth/kubernetes/login role=admin jwt=-); bao kv get -field=" + property + " secret/" + path + "; bao token revoke -self >/dev/null'"
	out, err := exec.Command("sh", "-c", script).Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
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
	lab(t, "infra", "forgejo", "sync", "--env", "sandbox")
	assert.Equal(t, deploy, baoValue(t, "forgejo/deploy", "token"), "a second sync replaced deploy's token")
	assert.Equal(t, argocd, baoValue(t, "argocd/forgejo", "token"), "a second sync replaced argocd's token")

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

	// Argo CD's credential reads master with the read-only token
	repoSecret := waitForSecret(t, "argocd", "argocd-repo-forgejo")
	assert.Equal(t, "argocd", string(repoSecret["username"]))
	assert.Equal(t, http.StatusOK, getJSON(t, "GET", "https://"+forgejoHost+"/api/v1/repos/ops/homelab/branches/master", nil,
		string(repoSecret["username"]), string(repoSecret["password"]), nil, nil))
}
```

If `configMapValue` does not exist in `test/`, add it next to `secretValue` (in `test/sso_test.go`, which defines `secretValue`):

```go
func configMapValue(t *testing.T, namespace, name, key string) string {
	t.Helper()
	out, err := exec.Command("kubectl", "-n", namespace, "get", "configmap", name, "-o", "jsonpath={.data."+strings.ReplaceAll(key, ".", `\.`)+"}").Output()
	require.NoError(t, err)
	return string(out)
}
```

`getJSON` sends basic auth with the token as the password, which Forgejo accepts for tokens.

- [ ] **Step 2: Scopes**

In `test/scopes.yaml`: the group that selects `Forgejo` gains `cli/internal/forgejo/**`, `cli/internal/secrets/**`, `cli/internal/cli/infra_forgejo*.go`, `infra/_modules/argocd/choose-values.sh` stays where it is; `skip` gains `cli/internal/env/**` and `cli/internal/guard/**`. Run `direnv exec . make -C cli check` — Expected: exit 0 (every component covered, every test named exists).

- [ ] **Step 3: Run the Forgejo tests on the sandbox**

Commit first (`make dev` refuses uncommitted changes), then run: `direnv exec . make dev tests=Forgejo`
Expected: `TestForgejoDeploy` passes.

- [ ] **Step 4: Find out whether a Forgejo site admin can push to the protected master (one-off, not a test)**

On the sandbox, as `forgejo_admin` (password from `kubectl -n forgejo get secret forgejo-admin`, through the askpass, never on the command line): clone `ops/homelab`, commit an empty commit, push it to a **new** branch `admin-probe` (allowed) and then try `HEAD:refs/heads/master`. Record the result in the PR body and the ledger. If it was accepted, `master` now holds the probe: push the same tree back with `lab infra forgejo push --env sandbox` (a sandbox commit on top, so the sandbox recovers) and delete branch `admin-probe`. The docs (Task 8) state the result.

- [ ] **Step 5: Basic checks and commit**

Run: `direnv exec . scripts/ci-check all` — Expected: exit 0.

```bash
git add test/forgejo_test.go test/sso_test.go test/scopes.yaml
git commit -m "test: lab infra forgejo sync and push against the sandbox's Forgejo (tokens kept, protection, Argo CD's credential)"
```

---

### Task 8: docs and the roadmap

**Files:**
- Modify: `docs/installation/production/deployment.md`, `docs/concepts/secrets-management.md`, `docs/reference/architecture/overview.md` (only where it names the seed Job or `forgejo_admin` for Argo CD), CLAUDE.md, `docs/superpowers/deferred-minors.md` (only if the final review defers minors)

- [ ] **Step 1: Find every mention to update**

Run: `grep -rn 'seed Job\|seed job\|forgejo-seed\|forgejo_admin\|argocd/generated/forgejo-admin\|production-checkout' --exclude-dir=site --exclude-dir=superpowers docs CLAUDE.md README.md 2>/dev/null`

- [ ] **Step 2: deployment.md**

Replace the note about Argo CD (from Task 10b) with:

```markdown
!!! note

    `make infra env=production` deploys: it refuses anything but `origin/master` checked out without changes whose
    check `checks` passed and whose pull request's check `sandbox` passed (`lab infra forgejo verify`), applies
    `infra/production`, provisions Forgejo's org `ops` (`lab infra forgejo sync`: the users `deploy` and `argocd`,
    `master`'s protection, their tokens in OpenBao), pushes the commit to `ops/homelab` (`lab infra forgejo push`),
    then applies the Argo CD bootstrap again. The first install is the same command: Argo CD reads the seed
    repository until its read-only token reads `ops/homelab`'s `master`, then switches to Forgejo and never back.
    `lab infra forgejo push` alone deploys without touching the infrastructure. Roll back with a revert through a
    pull request; nothing force-pushes. `force=1` applies the infrastructure only and deploys nothing (a state
    migration from another commit).
```

Add the site-admin result from Task 7 Step 4 ("Forgejo site admins, `forgejo_admin` and `homelab-admins`, can / cannot push to the protected `master`: …").

- [ ] **Step 3: secrets-management.md**

In the section about OpenTofu's and generated secrets, add a short paragraph: "`lab infra forgejo sync` writes two records with the admin role: `secret/forgejo/deploy` (the token `lab infra forgejo push` uses) and `secret/argocd/forgejo` (Argo CD's read-only token, read by `argocd-repo-forgejo`). Running `sync` again replaces a token Forgejo no longer accepts." Remove `argocd/generated/forgejo-admin` wherever it is listed.

- [ ] **Step 4: CLAUDE.md**

- Roadmap 10c: rewrite as done: "10c production deploys by an explicit push (upstream `nixos`'s model): `lab infra forgejo verify|sync|push` (`cli/`; org `ops`, users `deploy` and `argocd`, `master` pushable only by `deploy`, tokens in OpenBao), `make infra env=production` runs verify, apply, sync, push and the bootstrap again; no seed Job; Argo CD reads `ops/homelab` with a read-only token (done)".
- Roadmap 10e: add "the Woodpecker OAuth client (upstream's `lab infra forgejo sync` creates it)".
- Step 12: add "the staging guard: `make staging` marks the commit it proved and `lab infra forgejo verify` refuses a release touching `infra/`, `metal/`, nodes, k3s, kube-vip, Istio, storage or the deploy path without that mark (`--skip-staging=<reason>`, recorded)".
- Sandbox section: the Forgejo bullet loses the seed Job ("a seed Job copies …") and says `ops/homelab` is empty in the sandbox until `TestForgejoDeploy` runs `lab infra forgejo sync` and `push`.
- Production checklist: the Argo CD switch bullet describes the new condition (its read-only token reads `master`) and the one-command deploy; the bullet "Woodpecker and Renovate read `secret/woodpecker/forgejo` …" keeps Woodpecker for 10e.
- The `make infra env=production` / `production-checkout` sentence from 10b becomes the guard description.

- [ ] **Step 5: Docs build and basic checks, commit**

Run: `direnv exec . scripts/ci-check all` — Expected: exit 0 (includes `mkdocs build --strict`).

```bash
git add docs CLAUDE.md
git commit -m "docs: production deploys with make infra env=production (lab infra forgejo); staging guard in step 12, Woodpecker's OAuth client in 10e"
```

---

## After the tasks

1. `make dev` (the scope of the whole branch), then the final review on the most capable model over `origin/dev..HEAD`.
2. Fix Critical/Important findings; record rulings and minors in the ledger.
3. PR into `dev` with the rulings, the site-admin result, timings and deferred minors; label `sandbox`; ask which minors to fix.
