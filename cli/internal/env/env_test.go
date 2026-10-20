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
