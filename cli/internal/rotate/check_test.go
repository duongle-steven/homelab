package rotate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// stubRender stands in for scripts/render-chart: the "rendered" output of every target is two clients, one public
func stubRender(t *testing.T) {
	t.Helper()
	old := render
	t.Cleanup(func() { render = old })
	render = func(_, _ string) ([]byte, error) {
		return []byte("apiVersion: kaniop.rs/v1beta1\nkind: KanidmOAuth2Client\nmetadata: {name: newapp}\nspec: {}\n---\n" +
			"apiVersion: kaniop.rs/v1beta1\nkind: KanidmOAuth2Client\nmetadata: {name: phone}\nspec: {public: true}\n---\n" +
			"apiVersion: v1\nkind: ConfigMap\nmetadata: {name: other}\n"), nil
	}
}

func TestCheckFindsMissingSecret(t *testing.T) {
	stubRender(t)
	root := t.TempDir()
	write(t, root, "platform/generated-secrets/values.yaml", "secrets:\n  - name: new.generated\n    key: password\n    destinations: [new/generated/x]\n  - name: known.generated\n    key: password\n    destinations: [known/generated/x]\n")
	write(t, root, "platform/generated-secrets/values-dev.yaml", "extraSecrets: []\n")
	write(t, root, "platform/infra-secrets/values.yaml", "secrets:\n  - path: new/infra\n    properties: [a]\n  - path: known/infra\n    properties: [a]\n")
	write(t, root, "apps/newapp.yaml", "rawResources:\n  kanidm-client:\n    manifest:\n      kind: KanidmOAuth2Client\n")
	write(t, root, "apps/plain.yaml", "controllers: {}\n")
	entries, err := parse([]byte("- {name: known.generated, kind: generated}\n- {name: known.infra, kind: opentofu, paths: [known/infra]}\n- {name: forgejo.all, kind: forgejo-token, paths: [forgejo/deploy, argocd/forgejo, woodpecker/forgejo]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	got := Check(root, entries)
	if len(got) != 3 {
		t.Fatalf("Check = %q, want 3 problems", got)
	}
	for _, want := range []string{"new.generated", "new/infra", "kanidm-client.newapp"} {
		if !strings.Contains(strings.Join(got, "\n"), want) {
			t.Errorf("Check = %q, missing %s", got, want)
		}
	}
}

func TestCheckRepository(t *testing.T) {
	entries, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := Check("../../..", entries); len(got) != 0 {
		t.Fatalf("the catalog misses:\n%s", strings.Join(got, "\n"))
	}
}
