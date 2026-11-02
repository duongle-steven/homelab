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

// proofRepo is a repository with what the gate reads: release-paths.yaml, the testbed's values (kiali and excalidraw
// left out), a component the testbed runs (rook-ceph), one it leaves out (kiali), an app it runs and one it leaves out.
type proofRepo struct {
	t   *testing.T
	dir string
}

const proofValuesFixture = `argocd-apps:
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

func newProofRepo(t *testing.T) *proofRepo {
	r := &proofRepo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "master")
	r.write(map[string]string{
		"infra/testbed/release-paths.yaml":  "paths:\n  - infra/**\n  - system/argocd/**\n",
		"system/argocd/values-testbed.yaml": proofValuesFixture,
		"system/rook-ceph/values.yaml":      "a: 1\n",
		"system/kiali/values.yaml":          "a: 1\n",
		"apps/actualbudget.yaml":            "a: 1\n",
		"apps/excalidraw.yaml":              "a: 1\n",
		"docs/guide.md":                     "one\n",
	})
	return r
}

func (r *proofRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// write commits files and returns the new commit
func (r *proofRepo) write(files map[string]string) string {
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
func TestProofGateAcceptsEarlierMark(t *testing.T) {
	r := newProofRepo(t)
	deployed := r.git("rev-parse", "HEAD")
	proved := r.write(map[string]string{"system/rook-ceph/values.yaml": "a: 2\n"})
	release := r.write(map[string]string{"docs/guide.md": "two\n"})
	gh := statuses(t, map[string]string{proved + " testbed": "success"})
	if err := ProofGate(t.Context(), r.dir, "owner/homelab", "testbed", deployed, release, []string{release, proved}, gh); err != nil {
		t.Fatal(err)
	}
}

// A change to something the testbed runs, with no mark, is refused, naming the path
func TestProofGateRefusesUnmarked(t *testing.T) {
	r := newProofRepo(t)
	deployed := r.git("rev-parse", "HEAD")
	release := r.write(map[string]string{"system/rook-ceph/values.yaml": "a: 2\n"})
	err := ProofGate(t.Context(), r.dir, "owner/homelab", "testbed", deployed, release, []string{release}, statuses(t, nil))
	if err == nil || !strings.Contains(err.Error(), "system/rook-ceph/values.yaml") {
		t.Fatalf("err = %v, want one naming system/rook-ceph/values.yaml", err)
	}
}

// A change only to what the testbed leaves out (an app, a component) needs no mark: the testbed cannot prove it
func TestProofGateExcludedApp(t *testing.T) {
	r := newProofRepo(t)
	deployed := r.git("rev-parse", "HEAD")
	release := r.write(map[string]string{"apps/excalidraw.yaml": "a: 2\n", "system/kiali/values.yaml": "a: 2\n"})
	if err := ProofGate(t.Context(), r.dir, "owner/homelab", "testbed", deployed, release, []string{release}, statuses(t, nil)); err != nil {
		t.Fatal(err)
	}
}

// The gated set is what the proved commit's testbed ran: a mark taken while kiali was left out does not cover a
// release that also brings kiali in (system/argocd's values changed), but covers kiali's own changes while it stays out
func TestProofGateUsesProvedExclusions(t *testing.T) {
	r := newProofRepo(t)
	deployed := r.git("rev-parse", "HEAD")
	proved := r.write(map[string]string{"system/rook-ceph/values.yaml": "a: 2\n"})
	release := r.write(map[string]string{"system/kiali/values.yaml": "a: 2\n"})
	gh := statuses(t, map[string]string{proved + " testbed": "success"})
	if err := ProofGate(t.Context(), r.dir, "owner/homelab", "testbed", deployed, release, []string{release, proved}, gh); err != nil {
		t.Fatalf("kiali's change while left out: %v", err)
	}
	included := r.write(map[string]string{"system/argocd/values-testbed.yaml": strings.Replace(proofValuesFixture,
		"                    - path: system/kiali\n                      exclude: true\n", "", 1)})
	if err := ProofGate(t.Context(), r.dir, "owner/homelab", "testbed", deployed, included, []string{included, release, proved}, gh); err == nil {
		t.Fatal("bringing kiali into the testbed passed on a mark taken without it")
	}
}

// With nothing deployed yet (a first install: no Forgejo), everything counts as changed: the mark is required
func TestProofGateNoDeployedCommit(t *testing.T) {
	r := newProofRepo(t)
	release := r.git("rev-parse", "HEAD")
	if err := ProofGate(t.Context(), r.dir, "owner/homelab", "testbed", "", release, []string{release}, statuses(t, nil)); err == nil {
		t.Fatal("a first install passed without the mark")
	}
	gh := statuses(t, map[string]string{release + " testbed": "success"})
	if err := ProofGate(t.Context(), r.dir, "owner/homelab", "testbed", "", release, []string{release}, gh); err != nil {
		t.Fatal(err)
	}
}

// A failed or pending status is no mark
func TestProofGateIgnoresFailedMark(t *testing.T) {
	r := newProofRepo(t)
	deployed := r.git("rev-parse", "HEAD")
	release := r.write(map[string]string{"system/rook-ceph/values.yaml": "a: 2\n"})
	gh := statuses(t, map[string]string{release + " testbed": "failure"})
	if err := ProofGate(t.Context(), r.dir, "owner/homelab", "testbed", deployed, release, []string{release}, gh); err == nil {
		t.Fatal("a failed staging status passed")
	}
}

// --skip-proof needs a reason someone can read later
func TestSkipProofNeedsReason(t *testing.T) {
	for _, reason := range []string{"", " ", "\t\n"} {
		if err := SkipProofReason(reason); err == nil {
			t.Errorf("reason %q accepted", reason)
		}
	}
	if err := SkipProofReason("hotfix: the Tunnel is down"); err != nil {
		t.Fatal(err)
	}
}

// The commits a mark may sit on: the release, then dev's first-parent history back to what production runs (newest
// first); with nothing deployed, the history is bounded
func TestProofCandidates(t *testing.T) {
	r := newProofRepo(t)
	deployed := r.git("rev-parse", "HEAD")
	a := r.write(map[string]string{"docs/guide.md": "a\n"})
	b := r.write(map[string]string{"docs/guide.md": "b\n"})
	got, err := ProofCandidates(r.dir, deployed, b)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != b+" "+a {
		t.Fatalf("candidates %v, want [%s %s]", got, b, a)
	}
	got, err = ProofCandidates(r.dir, "", b)
	if err != nil || len(got) != 3 {
		t.Fatalf("with nothing deployed: %v, %v", got, err)
	}
}

// An allow-list is how a small testbed says what it runs: a change inside it needs the mark, one outside it does not
func TestProofGateAllowList(t *testing.T) {
	r := newProofRepo(t)
	r.write(map[string]string{"system/argocd/values-testbed.yaml": `argocd-apps:
  applicationsets:
    root:
      generators:
        - matrix:
            generators:
              - git:
                  directories:
                    - path: system/rook-ceph
`})
	deployed := r.git("rev-parse", "HEAD")
	outside := r.write(map[string]string{"system/kiali/values.yaml": "a: 2\n"})
	if err := ProofGate(t.Context(), r.dir, "owner/homelab", "testbed", deployed, outside, []string{outside}, statuses(t, nil)); err != nil {
		t.Fatal(err)
	}
	inside := r.write(map[string]string{"system/rook-ceph/values.yaml": "a: 2\n"})
	if err := ProofGate(t.Context(), r.dir, "owner/homelab", "testbed", deployed, inside, []string{inside, outside}, statuses(t, nil)); err == nil {
		t.Fatal("a change to an allow-listed component passed without a mark")
	}
}

// A component switched off inside its chart is not proved, though the list names it: not_run takes it out, and the
// release that brings it back needs a mark, since release-paths.yaml is gated
func TestProofGateNotRun(t *testing.T) {
	r := newProofRepo(t)
	r.write(map[string]string{
		"system/monitoring-system/values.yaml": "a: 1\n",
		"infra/testbed/release-paths.yaml":     "paths:\n  - infra/**\n  - system/argocd/**\nnot_run:\n  - system/monitoring-system/**\n",
	})
	deployed := r.git("rev-parse", "HEAD")
	release := r.write(map[string]string{"system/monitoring-system/values.yaml": "a: 2\n"})
	if err := ProofGate(t.Context(), r.dir, "owner/homelab", "testbed", deployed, release, []string{release}, statuses(t, nil)); err != nil {
		t.Fatal(err)
	}
	back := r.write(map[string]string{"infra/testbed/release-paths.yaml": "paths:\n  - infra/**\n  - system/argocd/**\n"})
	if err := ProofGate(t.Context(), r.dir, "owner/homelab", "testbed", deployed, back, []string{back, release}, statuses(t, nil)); err == nil {
		t.Fatal("bringing a component back passed without a mark")
	}
}

// A staging is the same gate with its own files and its own status
func TestProofGateStaging(t *testing.T) {
	r := newProofRepo(t)
	r.write(map[string]string{
		"infra/staging/release-paths.yaml":  "paths:\n  - infra/**\n",
		"system/argocd/values-staging.yaml": proofValuesFixture,
	})
	deployed := r.git("rev-parse", "HEAD")
	release := r.write(map[string]string{"system/rook-ceph/values.yaml": "a: 2\n"})
	if err := ProofGate(t.Context(), r.dir, "owner/homelab", "staging", deployed, release, []string{release}, statuses(t, map[string]string{release + " testbed": "success"})); err == nil {
		t.Fatal("the testbed's mark passed a release that staging must prove")
	}
	if err := ProofGate(t.Context(), r.dir, "owner/homelab", "staging", deployed, release, []string{release}, statuses(t, map[string]string{release + " staging": "success"})); err != nil {
		t.Fatal(err)
	}
}
