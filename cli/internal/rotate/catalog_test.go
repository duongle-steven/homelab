package rotate

import (
	"strings"
	"testing"
)

func TestLoadRefusesBadEntries(t *testing.T) {
	tests := []struct{ name, yaml, want string }{
		{"duplicate", "- {name: a, kind: generated}\n- {name: a, kind: generated}\n", "duplicate name"},
		{"unknown kind", "- {name: a, kind: telepathy}\n", "unknown kind"},
		{"class 3 without workaround", "- {name: a, kind: generated, class: 3}\n", "workaround"},
		{"period on manual", "- {name: a, kind: manual, period: 2160h}\n", "manual"},
		{"period with people", "- {name: a, kind: generated, period: 2160h, readers: [{people: true}]}\n", "people"},
		{"never without reason", "- {name: a, kind: never}\n", "reason"},
		{"unknown field", "- {name: a, kind: generated, colour: red}\n", "colour"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("parse error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestLoadAcceptsClass3WithWorkaround(t *testing.T) {
	if _, err := parse([]byte("- {name: a, kind: generated, class: 3, settings: {workaround: second user}}\n")); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogLoads(t *testing.T) {
	entries, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	e, err := Find(entries, "ntfy.alerts-token")
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != KindNtfyOverlap || e.Period.Hours() != 2160 {
		t.Fatalf("ntfy.alerts-token = %+v", e)
	}
	if _, err := Find(entries, "nothing"); err == nil {
		t.Fatal("Find of an unknown name succeeded")
	}
}
