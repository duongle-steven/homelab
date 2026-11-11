package scope

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The repository's own map selects what the spec's table says
func TestRepositoryMap(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Load(filepath.Join(root, "test", "scopes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	specs, err := Specs(root)
	if err != nil {
		t.Fatal(err)
	}
	full := "(argocd || backup-restore || egress || falco || forgejo || gateway || identity || logs || mesh || monitoring || node-power-cut || node-restart || ntfy || policy || public || rotation || secrets || smoke || sso || supply-chain || trivy-operator || woodpecker) && !alone"
	fullAlone := []string{"backup-restore", "node-power-cut", "node-restart", "rotation"}
	for _, tc := range []struct {
		file, main string
		alone      []string
	}{
		{"docs/index.md", "", nil},
		{"CLAUDE.md", "", nil},
		{"system/loki/values.yaml", "(logs || smoke) && !alone", nil},
		{"system/istio-system/values.yaml", "(egress || gateway || identity || mesh || node-power-cut || node-restart || smoke) && !alone", []string{"node-power-cut", "node-restart"}},
		{"infra/root.hcl", full, fullAlone},
		{"infra/sandbox/cluster/config.json", full, fullAlone},
		{"infra/_modules/k3d/main.tf", full, fullAlone},
		{"scripts/env-values", full, fullAlone},
		{"system/egress/values.yaml", full, fullAlone},
		{"platform/openbao/values.yaml", "(backup-restore || forgejo || gateway || identity || logs || monitoring || ntfy || secrets || smoke || sso || woodpecker) && !alone", []string{"backup-restore"}},
		{"platform/kanidm/values.yaml", "((forgejo && deploy) || (gateway && zot) || identity || smoke || sso || woodpecker) && !alone", nil},
		// lab's Forgejo deploy runs against the sandbox; its offline parts are skipped
		{"cli/internal/forgejo/sync.go", "(forgejo || smoke || woodpecker) && !alone", nil},
		{"cli/go.mod", "(forgejo || smoke || woodpecker) && !alone", nil},
		{"cli/internal/guard/verify.go", "", nil},
		{"platform/forgejo/values.yaml", "(backup-restore || forgejo || smoke || woodpecker) && !alone", []string{"backup-restore"}},
		{"platform/woodpecker/values.yaml", "(smoke || woodpecker) && !alone", nil},
		{"system/velero/values.yaml", "(backup-restore || smoke) && !alone", []string{"backup-restore"}},
		{"test/offline/woodpecker_test.go", "", nil},
		{"apps/paperless.yaml", "(paperless || smoke) && !alone", nil},
		{"apps/blog.yaml", "smoke && !alone", nil},
		{"system/kured/values.yaml", "smoke && !alone", nil},
		// the offline tests and the internal packages' own tests: the basic checks run them
		{"test/offline/velero_test.go", "", nil},
		{"test/internal/repo/repo_test.go", "", nil},
		// the suite's entry and what it waits on before any spec: every run
		{"test/e2e/e2e_suite_test.go", full, fullAlone},
		{"test/internal/argocd/argocd.go", full, fullAlone},
		{"justfile", full, fullAlone},
		{"scripts/something-new", full, fullAlone},
	} {
		s, err := c.Select([]string{tc.file}, FileTests(root))
		if err != nil {
			t.Fatal(err)
		}
		if main, alone := c.Filter(s, specs); main != tc.main || !reflect.DeepEqual(alone, tc.alone) {
			t.Errorf("%s: got %q %q, want %q %q", tc.file, main, alone, tc.main, tc.alone)
		}
	}
	s, err := c.Select([]string{"test/e2e/logs_test.go"}, FileTests(root))
	if err != nil {
		t.Fatal(err)
	}
	if main, _ := c.Filter(s, specs); !strings.Contains(main, "logs") || !strings.Contains(main, "smoke") {
		t.Errorf("test/e2e/logs_test.go: %q", main)
	}
	// The repository's map and specs pass the check
	if problems := c.Check(nil, specs); len(problems) > 0 {
		t.Errorf("problems: %q", problems)
	}
}

// Every target lab test charts prints is an argument scripts/render-chart takes: a directory with Chart.yaml under
// system/, platform/ or apps/, or apps/<name>.yaml
func TestRepositoryChartsAreRenderTargets(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	targets, err := Charts(root, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) < 30 {
		t.Fatalf("only %d targets: %v", len(targets), targets)
	}
	for _, target := range targets {
		if strings.HasPrefix(target, "apps/") && strings.HasSuffix(target, ".yaml") && !strings.Contains(strings.TrimPrefix(target, "apps/"), "/") {
			continue
		}
		top := strings.SplitN(target, "/", 2)[0]
		if top != "system" && top != "platform" && top != "apps" || !exists(filepath.Join(root, target, "Chart.yaml")) {
			t.Errorf("%s is not a render-chart target", target)
		}
	}
}
