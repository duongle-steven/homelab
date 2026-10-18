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
	full := "Egress|Forgejo|Gateway|Identity|Logs|Mesh|Monitoring|Ntfy|SSO|Secrets|Smoke"
	for _, tc := range []struct {
		file, main, alone string
	}{
		{"docs/index.md", "", ""},
		{"CLAUDE.md", "", ""},
		{"system/loki/values.yaml", "Logs|Smoke", ""},
		{"system/istio-system/values.yaml", "Egress|Gateway|Identity|Mesh|Smoke", "NodeRestart"},
		{"infra/root.hcl", "Identity|Ntfy|SSO|Secrets|Smoke", ""},
		{"platform/forgejo/values.yaml", "Forgejo|GatewayForgejoSSH|SSOForgejo|Smoke", ""},
		{"apps/paperless.yaml", "SSOPaperless|Smoke", ""},
		{"apps/blog.yaml", "Smoke", ""},
		{"system/kured/values.yaml", "Smoke", ""},
		{"test/state_rotation_test.go", "", ""},
		{"Makefile", full, "NodeRestart"},
		{"scripts/something-new", full, "NodeRestart"},
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
