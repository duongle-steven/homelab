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

// make infra applies infra/ and renders system/argocd from the working tree: an untracked file there would reach
// production without being in the verified commit (OpenTofu's lock files excepted: they are not committed)
func TestVerifyRefusesUntrackedInfrastructure(t *testing.T) {
	// The lock-file case reaches GitHub: answer at once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusTeapot) }))
	defer server.Close()
	gh := GitHub{BaseURL: server.URL, HTTP: server.Client()}
	clone, _ := repos(t)
	lock := filepath.Join(clone, "infra", "production", "argocd", ".terraform.lock.hcl")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, []byte("lock"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(context.Background(), clone, "owner/homelab", gh); err == nil || strings.Contains(err.Error(), "untracked") {
		t.Fatalf("a lock file was refused or the guards stopped early: %v", err)
	}
	for _, path := range []string{"infra/production/extra.tf", "system/argocd/templates/extra.yaml"} {
		file := filepath.Join(clone, path)
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Verify(context.Background(), clone, "owner/homelab", gh)
		if err == nil || !strings.Contains(err.Error(), "untracked") || !strings.Contains(err.Error(), path) {
			t.Fatalf("an untracked %s: %v", path, err)
		}
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
	}
}

// A break-glass deploy (GitHub down) still refuses a dirty checkout but asks no remote
func TestCleanAsksNoRemote(t *testing.T) {
	clone, head := repos(t)
	if out, err := exec.Command("git", "-C", clone, "remote", "set-url", "origin", "https://unreachable.invalid/homelab.git").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got, err := Clean(clone)
	if err != nil || got != head {
		t.Fatalf("Clean() = %q, %v; want %s", got, err, head)
	}
	if err := os.WriteFile(filepath.Join(clone, "file"), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Clean(clone); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("an uncommitted change: %v", err)
	}
}
