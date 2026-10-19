package scope

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// chartTree writes a repository layout with two wrapper charts, an app file and the app chart apps/matrix
func chartTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, f := range []string{
		"system/loki/Chart.yaml", "system/loki/values.yaml", "system/loki/templates/x.yaml",
		"platform/forgejo/Chart.yaml", "platform/forgejo/files/seed.sh",
		"apps/ntfy.yaml", "apps/matrix/Chart.yaml", "apps/matrix/values.yaml",
		"system/argocd/Chart.yaml", "system/argocd/values-dev.yaml",
		"scripts/render-chart", "docs/index.md",
	} {
		path := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestChartsFromChangedFiles(t *testing.T) {
	root := chartTree(t)
	got, err := Charts(root, []string{"system/loki/templates/x.yaml", "platform/forgejo/files/seed.sh", "apps/ntfy.yaml", "apps/matrix/values.yaml", "docs/index.md", "system/loki/values.yaml"}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"apps/matrix", "apps/ntfy.yaml", "platform/forgejo", "system/loki"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Charts = %v, want %v", got, want)
	}
}

func TestChartsSkipsDeleted(t *testing.T) {
	root := chartTree(t)
	got, err := Charts(root, []string{"system/gone/values.yaml", "apps/gone.yaml"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Charts = %v, want nothing for deleted charts", got)
	}
}

func TestChartsGlobalTriggersRenderAll(t *testing.T) {
	root := chartTree(t)
	all := []string{"apps/matrix", "apps/ntfy.yaml", "platform/forgejo", "system/argocd", "system/loki"}
	for _, trigger := range []string{"scripts/render-chart", "scripts/env-values", "infra/sandbox/cluster/config.json", "system/argocd/values-dev.yaml"} {
		got, err := Charts(root, []string{trigger}, false)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, all) {
			t.Fatalf("%s: Charts = %v, want every chart %v", trigger, got, all)
		}
	}
	got, err := Charts(root, nil, true)
	if err != nil || !reflect.DeepEqual(got, all) {
		t.Fatalf("all: Charts = %v, %v", got, err)
	}
}
