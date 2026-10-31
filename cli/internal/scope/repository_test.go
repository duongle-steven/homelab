package scope

import (
	"path/filepath"
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
	full := "Egress|Forgejo|Gateway|Identity|Logs|Mesh|Monitoring|Ntfy|Policy|SSO|Secrets|Smoke|SupplyChain|TrivyOperator|Woodpecker"
	for _, tc := range []struct {
		file, main, alone string
	}{
		{"docs/index.md", "", ""},
		{"CLAUDE.md", "", ""},
		{"system/loki/values.yaml", "Logs|Smoke", ""},
		{"system/istio-system/values.yaml", "Egress|Gateway|Identity|Mesh|Smoke", "NodePowerCut|NodeRestart"},
		{"infra/root.hcl", full, "BackupRestore|NodePowerCut|NodeRestart|Rotation"},
		{"infra/sandbox/cluster/config.json", full, "BackupRestore|NodePowerCut|NodeRestart|Rotation"},
		{"infra/_modules/k3d/main.tf", full, "BackupRestore|NodePowerCut|NodeRestart|Rotation"},
		{"scripts/env-values", full, "BackupRestore|NodePowerCut|NodeRestart|Rotation"},
		{"system/egress/values.yaml", full, "BackupRestore|NodePowerCut|NodeRestart|Rotation"},
		{"platform/openbao/values.yaml", "Forgejo|Gateway|Identity|Logs|Monitoring|Ntfy|SSO|Secrets|Smoke|Woodpecker", "BackupRestore"},
		{"platform/kanidm/values.yaml", "ForgejoDeploy|GatewayZotLargePush|Identity|SSO|Smoke|Woodpecker", ""},
		// lab's Forgejo deploy runs against the sandbox; its offline parts are skipped
		{"cli/internal/forgejo/sync.go", "Forgejo|Smoke|Woodpecker", ""},
		{"cli/go.mod", "Forgejo|Smoke|Woodpecker", ""},
		{"cli/internal/guard/verify.go", "", ""},
		{"platform/forgejo/values.yaml", "Forgejo|GatewayForgejoSSH|SSOForgejo|Smoke|Woodpecker", "BackupRestore"},
		{"platform/woodpecker/values.yaml", "Smoke|Woodpecker", ""},
		{"test/woodpecker_test.go", "", ""},
		{"apps/paperless.yaml", "SSOPaperless|Smoke", ""},
		{"apps/blog.yaml", "Smoke", ""},
		{"system/kured/values.yaml", "Smoke", ""},
		{"test/state_rotation_test.go", "", ""},
		{"Makefile", full, "BackupRestore|NodePowerCut|NodeRestart|Rotation"},
		{"scripts/something-new", full, "BackupRestore|NodePowerCut|NodeRestart|Rotation"},
	} {
		s, err := c.Select([]string{tc.file}, FileTests(root))
		if err != nil {
			t.Fatal(err)
		}
		if main, alone := c.Regex(s, false), c.Regex(s, true); main != tc.main || alone != tc.alone {
			t.Errorf("%s: got %q %q, want %q %q", tc.file, main, alone, tc.main, tc.alone)
		}
	}
	s, err := c.Select([]string{"test/logs_test.go"}, FileTests(root))
	if err != nil {
		t.Fatal(err)
	}
	if main := c.Regex(s, false); !strings.Contains(main, "TestLogsPodLines") || !strings.Contains(main, "Smoke") {
		t.Errorf("test/logs_test.go: %q", main)
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
