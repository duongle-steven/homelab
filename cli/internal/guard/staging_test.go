package guard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// stagingRepo is a repository with what the gate reads: release-paths.yaml, staging's values (kiali and excalidraw
// left out), a component staging runs (rook-ceph), one it leaves out (kiali), an app it runs and one it leaves out.
type stagingRepo struct {
	t   *testing.T
	dir string
}

const stagingValuesFixture = `argocd-apps:
  applicationsets:
    root:
      generators:
        - matrix:
            generators:
              - git:
                  directories:
                    - path: system/*
                    - path: system/argocd
                      exclude: true
                    - path: platform/*
                    - path: apps/*
                    - path: system/kiali
                      exclude: true
    apps:
      generators:
        - matrix:
            generators:
              - git:
                  files:
                    - path: apps/actualbudget.yaml
`

func newStagingRepo(t *testing.T) *stagingRepo {
	r := &stagingRepo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "master")
	r.write(map[string]string{
		"infra/staging/release-paths.yaml":  "paths:\n  - infra/**\n  - system/argocd/**\n",
		"system/argocd/values-staging.yaml": stagingValuesFixture,
		"system/rook-ceph/values.yaml":      "a: 1\n",
		"system/kiali/values.yaml":          "a: 1\n",
		"apps/actualbudget.yaml":            "a: 1\n",
		"apps/excalidraw.yaml":              "a: 1\n",
		"docs/guide.md":                     "one\n",
	})
	return r
}

func (r *stagingRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// write commits files and returns the new commit
func (r *stagingRepo) write(files map[string]string) string {
	r.t.Helper()
	for name, body := range files {
		path := filepath.Join(r.dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "change")
	return r.git("rev-parse", "HEAD")
}

// statuses serves commit statuses: state per "<sha> <context>"
func statuses(t *testing.T, states map[string]string) GitHub {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/repos/owner/homelab/commits/"), "/")
		if len(parts) != 2 || parts[1] != "status" {
			http.NotFound(w, r)
			return
		}
		var list []map[string]string
		for key, state := range states {
			if sha, ctx, _ := strings.Cut(key, " "); sha == parts[0] {
				list = append(list, map[string]string{"context": ctx, "state": state})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"statuses": list})
	}))
	t.Cleanup(server.Close)
	return GitHub{BaseURL: server.URL, HTTP: server.Client()}
}

// A mark on an earlier commit holds when only paths staging does not run changed since (a docs merge into dev)
func TestStagingGateAcceptsEarlierMark(t *testing.T) {
	r := newStagingRepo(t)
	deployed := r.git("rev-parse", "HEAD")
	proved := r.write(map[string]string{"system/rook-ceph/values.yaml": "a: 2\n"})
	release := r.write(map[string]string{"docs/guide.md": "two\n"})
	gh := statuses(t, map[string]string{proved + " staging": "success"})
	if err := StagingGate(t.Context(), r.dir, "owner/homelab", deployed, release, []string{release, proved}, gh); err != nil {
		t.Fatal(err)
	}
}

// A change to something staging runs, with no mark, is refused, naming the path
func TestStagingGateRefusesUnmarked(t *testing.T) {
	r := newStagingRepo(t)
	deployed := r.git("rev-parse", "HEAD")
	release := r.write(map[string]string{"system/rook-ceph/values.yaml": "a: 2\n"})
	err := StagingGate(t.Context(), r.dir, "owner/homelab", deployed, release, []string{release}, statuses(t, nil))
	if err == nil || !strings.Contains(err.Error(), "system/rook-ceph/values.yaml") {
		t.Fatalf("err = %v, want one naming system/rook-ceph/values.yaml", err)
	}
}

// A change only to what staging leaves out (an app, a component) needs no mark: staging cannot prove it
func TestStagingGateExcludedApp(t *testing.T) {
	r := newStagingRepo(t)
	deployed := r.git("rev-parse", "HEAD")
	release := r.write(map[string]string{"apps/excalidraw.yaml": "a: 2\n", "system/kiali/values.yaml": "a: 2\n"})
	if err := StagingGate(t.Context(), r.dir, "owner/homelab", deployed, release, []string{release}, statuses(t, nil)); err != nil {
		t.Fatal(err)
	}
}

// The gated set is what the proved commit's staging ran: a mark taken while kiali was left out does not cover a
// release that also brings kiali in (system/argocd's values changed), but covers kiali's own changes while it stays out
func TestStagingGateUsesProvedExclusions(t *testing.T) {
	r := newStagingRepo(t)
	deployed := r.git("rev-parse", "HEAD")
	proved := r.write(map[string]string{"system/rook-ceph/values.yaml": "a: 2\n"})
	release := r.write(map[string]string{"system/kiali/values.yaml": "a: 2\n"})
	gh := statuses(t, map[string]string{proved + " staging": "success"})
	if err := StagingGate(t.Context(), r.dir, "owner/homelab", deployed, release, []string{release, proved}, gh); err != nil {
		t.Fatalf("kiali's change while left out: %v", err)
	}
	included := r.write(map[string]string{"system/argocd/values-staging.yaml": strings.Replace(stagingValuesFixture,
		"                    - path: system/kiali\n                      exclude: true\n", "", 1)})
	if err := StagingGate(t.Context(), r.dir, "owner/homelab", deployed, included, []string{included, release, proved}, gh); err == nil {
		t.Fatal("bringing kiali into staging passed on a mark taken without it")
	}
}

// With nothing deployed yet (a first install: no Forgejo), everything counts as changed: the mark is required
func TestStagingGateNoDeployedCommit(t *testing.T) {
	r := newStagingRepo(t)
	release := r.git("rev-parse", "HEAD")
	if err := StagingGate(t.Context(), r.dir, "owner/homelab", "", release, []string{release}, statuses(t, nil)); err == nil {
		t.Fatal("a first install passed without staging's mark")
	}
	gh := statuses(t, map[string]string{release + " staging": "success"})
	if err := StagingGate(t.Context(), r.dir, "owner/homelab", "", release, []string{release}, gh); err != nil {
		t.Fatal(err)
	}
}

// A failed or pending staging status is no mark
func TestStagingGateIgnoresFailedMark(t *testing.T) {
	r := newStagingRepo(t)
	deployed := r.git("rev-parse", "HEAD")
	release := r.write(map[string]string{"system/rook-ceph/values.yaml": "a: 2\n"})
	gh := statuses(t, map[string]string{release + " staging": "failure"})
	if err := StagingGate(t.Context(), r.dir, "owner/homelab", deployed, release, []string{release}, gh); err == nil {
		t.Fatal("a failed staging status passed")
	}
}

// --skip-staging needs a reason someone can read later
func TestSkipStagingNeedsReason(t *testing.T) {
	for _, reason := range []string{"", " ", "\t\n"} {
		if err := SkipStagingReason(reason); err == nil {
			t.Errorf("reason %q accepted", reason)
		}
	}
	if err := SkipStagingReason("hotfix: the Tunnel is down"); err != nil {
		t.Fatal(err)
	}
}

// The commits a mark may sit on: the release, then dev's first-parent history back to what production runs (newest
// first); with nothing deployed, the history is bounded
func TestStagingCandidates(t *testing.T) {
	r := newStagingRepo(t)
	deployed := r.git("rev-parse", "HEAD")
	a := r.write(map[string]string{"docs/guide.md": "a\n"})
	b := r.write(map[string]string{"docs/guide.md": "b\n"})
	got, err := StagingCandidates(r.dir, deployed, b)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != b+" "+a {
		t.Fatalf("candidates %v, want [%s %s]", got, b, a)
	}
	got, err = StagingCandidates(r.dir, "", b)
	if err != nil || len(got) != 3 {
		t.Fatalf("with nothing deployed: %v, %v", got, err)
	}
}
