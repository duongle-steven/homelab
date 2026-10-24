package rotate

import (
	"errors"
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
	if got := append(Check("../../..", entries), CheckScheduled("../../..", entries)...); len(got) != 0 {
		t.Fatalf("the catalog misses:\n%s", strings.Join(got, "\n"))
	}
}

func TestCheckReferences(t *testing.T) {
	root := t.TempDir()
	write(t, root, "infra/_modules/x/main.tf", "resource \"cloudflare_account_token\" \"real\" {\n}\n")
	write(t, root, "apps/a.yaml", "externalSecrets:\n  x:\n    forceRename: real-es\n")
	write(t, root, "platform/b/templates/e.yaml", "kind: ExternalSecret\nmetadata:\n  name: other-es\n")
	entries, err := parse([]byte(`
- {name: good, kind: opentofu, settings: {resource: cloudflare_account_token.real}, readers: [{namespace: a, externalSecrets: [real-es, other-es]}]}
- {name: bad.resource, kind: opentofu, settings: {resource: cloudflare_account_token.gone}}
- {name: bad.es, kind: generated, readers: [{namespace: a, externalSecrets: [gone-es]}]}
- {name: later, kind: opentofu, pending: a later task, settings: {resource: x.y}, readers: [{namespace: a, externalSecrets: [nope]}]}
`))
	if err != nil {
		t.Fatal(err)
	}
	got := checkReferences(root, entries)
	if len(got) != 2 || !strings.Contains(got[0]+got[1], "cloudflare_account_token.gone") || !strings.Contains(got[0]+got[1], "gone-es") {
		t.Fatalf("checkReferences = %q", got)
	}
}

func TestCheckReportsRenderError(t *testing.T) {
	old := render
	t.Cleanup(func() { render = old })
	render = func(_, target string) ([]byte, error) { return nil, errors.New("helm fell over on " + target) }
	root := t.TempDir()
	write(t, root, "platform/generated-secrets/values.yaml", "secrets: []\n")
	write(t, root, "platform/infra-secrets/values.yaml", "secrets: []\n")
	write(t, root, "apps/newapp.yaml", "kind: KanidmOAuth2Client\n")
	got := Check(root, nil)
	if !strings.Contains(strings.Join(got, "\n"), "helm fell over on apps/newapp.yaml") {
		t.Fatalf("Check = %q", got)
	}
}

func TestCheckReportsMissingValues(t *testing.T) {
	stubRender(t)
	got := Check(t.TempDir(), nil)
	if !strings.Contains(strings.Join(got, "\n"), "no values files") {
		t.Fatalf("Check = %q", got)
	}
}

// The scheduled rotation may touch exactly what its entries need: platform/secrets-rotation's readers are the
// namespaces of the periodic entries' readers, and OpenBao's policy rotator names each of their paths
func TestCheckScheduledAccess(t *testing.T) {
	root := t.TempDir()
	write(t, root, "platform/secrets-rotation/values.yaml", "readers: [ntfy, extra]\n")
	write(t, root, "platform/openbao/values.yaml", `policy = "path \"secret/data/ntfy/generated/alerts-token\" { capabilities = [\"read\"] }"`+"\n")
	entries, err := parse([]byte(`
- {name: a, kind: ntfy-overlap, period: 2160h, paths: [ntfy/generated/alerts-token], readers: [{namespace: ntfy}]}
- {name: b, kind: generated, period: 2160h, paths: [zot/generated/ci], readers: [{namespace: zot}]}
- {name: c, kind: generated, paths: [paperless/generated/admin], readers: [{namespace: paperless}]}
- {name: d, kind: opentofu, period: 2160h, paths: [cert-manager/cloudflare], readers: [{namespace: cert-manager}], settings: {unit: u, resource: r}}
`))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(CheckScheduled(root, entries), "\n")
	for _, want := range []string{"zot: b's readers", "extra: no scheduled entry", "zot/generated/ci: b's path"} {
		if !strings.Contains(got, want) {
			t.Errorf("CheckScheduled = %q, missing %q", got, want)
		}
	}
	for _, unwanted := range []string{"paperless", "cert-manager"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("CheckScheduled = %q names %s, which the CronJob never rotates", got, unwanted)
		}
	}
}
