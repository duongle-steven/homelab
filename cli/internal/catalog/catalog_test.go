package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// repo writes a minimal repository: files maps a path to its content
func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range files {
		full := filepath.Join(root, path)
		must(t, os.MkdirAll(filepath.Dir(full), 0o755))
		must(t, os.WriteFile(full, []byte(content), 0o644))
	}
	return root
}

const homepage = `controllers: {}
route:
  main:
    hostnames:
      - home.{{ .Values.global.baseDomain }}
configMaps:
  config:
    data:
      widgets.yaml: |
        - search: {}
      services.yaml: |
        - Old:
          - Gone: {}
      bookmarks.yaml: |
        - Homelab: []
homelab:
  - route: main
    name: Homepage
    description: The family's start page
`

const media = `route:
  main:
    hostnames:
      - media.{{ .Values.global.baseDomain }}
  requests:
    hostnames:
      - requests.{{ .Values.global.baseDomain }}
homelab:
  - route: main
    name: Media
    description: Films and series
    group: Popular
    icon: jellyfin.svg
  - route: requests
    name: Requests
    description: Ask for a film
    group: Popular
    icon: jellyseerr.svg
`

const identity = `apiVersion: v2
name: identity
version: 0.0.0
annotations:
  homelab/name: Identity
  homelab/description: Who is who
  homelab/group: Management
  homelab/icon: kanidm.svg
  homelab/host: auth
`

const mesh = `apiVersion: v2
name: mesh
version: 0.0.0
annotations:
  homelab/name: Mesh
  homelab/description: mTLS between pods
`

func fixture(t *testing.T) string {
	return repo(t, map[string]string{
		"apps/homepage.yaml":           homepage,
		"apps/media.yaml":              media,
		"platform/identity/Chart.yaml": identity,
		"system/mesh/Chart.yaml":       mesh,
	})
}

func TestGenerateFromApps(t *testing.T) {
	files, err := Generate(fixture(t))
	must(t, err)

	apps := string(files["catalog/apps.yaml"])
	// A file's main route takes the file's name, any other route <file>-<route>
	contains(t, apps, "name: media\n")
	contains(t, apps, "name: media-requests\n")
	contains(t, apps, "homelab/host: requests\n")
	contains(t, apps, "argocd/app-name: media\n")
	contains(t, apps, "backstage.io/kubernetes-namespace: media\n")
	contains(t, apps, "owner: group:default/homelab-admins\n")
	contains(t, apps, "system: homelab-apps\n")

	services := servicesBlock(t, files["apps/homepage.yaml"])
	contains(t, services, "href: https://media.{{ .Values.global.baseDomain }}")
	contains(t, services, "href: https://requests.{{ .Values.global.baseDomain }}")
	if strings.Contains(services, "Gone") {
		t.Errorf("the hand-written list is replaced")
	}
}

func TestGenerateFromCharts(t *testing.T) {
	files, err := Generate(fixture(t))
	must(t, err)

	contains(t, string(files["catalog/platform.yaml"]), "name: identity\n")
	contains(t, string(files["catalog/system.yaml"]), "name: mesh\n")
	services := servicesBlock(t, files["apps/homepage.yaml"])
	contains(t, services, "href: https://auth.{{ .Values.global.baseDomain }}")
	if strings.Contains(services, "Mesh") {
		t.Errorf("a component without a host or group has no homepage entry")
	}
	// Groups keep homepage's order, whatever order the files come in
	if strings.Index(services, "- Popular:") > strings.Index(services, "- Management:") {
		t.Errorf("Popular after Management:\n%s", services)
	}
}

func TestHomepageKeepsTheRest(t *testing.T) {
	files, err := Generate(fixture(t))
	must(t, err)
	page := string(files["apps/homepage.yaml"])
	contains(t, page, "      widgets.yaml: |\n        - search: {}\n")
	contains(t, page, "      bookmarks.yaml: |\n        - Homelab: []\n")
	contains(t, page, "homelab:\n  - route: main\n")
}

func TestEveryAppAndComponentDescribed(t *testing.T) {
	root := fixture(t)
	must(t, os.WriteFile(filepath.Join(root, "apps/silent.yaml"), []byte("route: {}\n"), 0o644))
	_, err := Generate(root)
	errorContains(t, err, "apps/silent.yaml")
}

func TestUnknownGroupRefused(t *testing.T) {
	root := fixture(t)
	must(t, os.WriteFile(filepath.Join(root, "system/mesh/Chart.yaml"),
		[]byte(strings.Replace(mesh, "homelab/description: mTLS between pods", "homelab/description: x\n  homelab/group: Misc\n  homelab/icon: x.svg\n  homelab/host: mesh", 1)), 0o644))
	_, err := Generate(root)
	errorContains(t, err, "Misc")
}

func TestCheckReportsDrift(t *testing.T) {
	root := fixture(t)
	must(t, Write(root))
	must(t, Check(root))

	must(t, os.WriteFile(filepath.Join(root, "apps/media.yaml"),
		[]byte(strings.Replace(media, "Films and series", "Films", 1)), 0o644))
	err := Check(root)
	errorContains(t, err, "catalog/apps.yaml")
	errorContains(t, err, "lab catalog generate")
}

// servicesBlock is the services.yaml block of a generated homepage app file
func servicesBlock(t *testing.T, page []byte) string {
	t.Helper()
	s := string(page)
	start := strings.Index(s, "services.yaml: |")
	end := strings.Index(s[max(start, 0):], "bookmarks.yaml: |")
	if start < 0 || end <= 0 {
		t.Fatalf("no services.yaml block:\n%s", s)
	}
	return s[start : start+end]
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func contains(t *testing.T, s, want string) {
	t.Helper()
	if !strings.Contains(s, want) {
		t.Errorf("missing %q in:\n%s", want, s)
	}
}

func errorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("error %v does not name %q", err, want)
	}
}

// A description is free text: homepage's services.yaml stays valid YAML whatever it holds
func TestHomepageQuotesText(t *testing.T) {
	root := fixture(t)
	must(t, os.WriteFile(filepath.Join(root, "apps/media.yaml"),
		[]byte(strings.Replace(media, "description: Films and series", `description: "Films: and series # all"`, 1)), 0o644))
	files, err := Generate(root)
	must(t, err)
	block := servicesBlock(t, files["apps/homepage.yaml"])
	_, body, _ := strings.Cut(block, "\n")
	var parsed []map[string][]map[string]map[string]string
	must(t, yaml.Unmarshal([]byte(strings.ReplaceAll(body, "\n        ", "\n")[8:]), &parsed))
	if got := parsed[0]["Popular"][0]["Media"]["description"]; got != "Films: and series # all" {
		t.Errorf("description %q", got)
	}
}

// The repository's root holds the catalog's entry point: its Domain carries the docs (TechDocs reads mkdocs.yml from the
// entity's own directory), and its Location the generated files
func TestRootEntryPoint(t *testing.T) {
	files, err := Generate(fixture(t))
	must(t, err)
	root := string(files["catalog-info.yaml"])
	contains(t, root, "kind: Domain\n")
	contains(t, root, "backstage.io/techdocs-ref: dir:.\n")
	contains(t, root, "- ./catalog/apps.yaml\n")
	if _, ok := files["catalog/all.yaml"]; ok {
		t.Error("catalog/all.yaml is replaced by catalog-info.yaml")
	}
	if strings.Contains(string(files["catalog/systems.yaml"]), "kind: Domain") {
		t.Error("the Domain moved to catalog-info.yaml")
	}
}
