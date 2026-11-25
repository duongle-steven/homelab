package crds

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The environments' ApplicationSets, as system/argocd's values files write them (nested in matrices): a second
// generator includes what the first excludes (production's stage-gated components), and the template adds fixed sources
const prodValues = `argocd-apps:
  applicationsets:
    root:
      generators:
        - matrix:
            generators:
              - git:
                  directories:
                    - path: system/*
                    - path: system/crds
                      exclude: true
                    - path: system/scan
                      exclude: true
                    - path: platform/*
        - matrix:
            generators:
              - git:
                  directories:
                    - path: system/scan
      template:
        spec:
          sources:
            - path: '{{ .path.path }}'
            - path: system/secret-store
    apps:
      generators:
        - matrix:
            generators:
              - git:
                  files:
                    - path: apps/*.yaml
`

const testbedValues = `argocd-apps:
  applicationsets:
    root:
      generators:
        - git:
            directories:
              - path: system/mesh
    apps: null
`

func crd(group, kind string) string {
	return fmt.Sprintf("apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nspec:\n  group: %s\n  names:\n    kind: %s\n", group, kind)
}

// fixture: components mesh, policy and metrics (each shipping a CRD), an app, and the renders a stub returns
func fixture(t *testing.T) string {
	root := t.TempDir()
	files := map[string]string{
		"system/argocd/values.yaml":                          prodValues,
		"system/argocd/values-testbed.yaml":                  testbedValues,
		"system/mesh/Chart.yaml":                             "name: mesh\n",
		"system/metrics/Chart.yaml":                          "name: metrics\n",
		"platform/policy/Chart.yaml":                         "name: policy\n",
		"system/crds/Chart.yaml":                             "name: crds\n",
		"system/scan/Chart.yaml":                             "name: scan\n",
		"system/crds/files/scan/reports.example.io.yaml":     crd("scan.example.io", "Report"),
		"apps/web.yaml":                                      "controllers: {}\n",
		"system/crds/files/mesh/gateways.example.io.yaml":    crd("mesh.example.io", "Gateway"),
		"system/crds/files/policy/policies.example.io.yaml":  crd("policy.example.io", "Policy"),
		"system/crds/files/metrics/monitors.example.io.yaml": crd("metrics.example.io", "Monitor"),
		"system/crds/files/secrets/stores.example.io.yaml":   crd("secrets.example.io", "Store"),
	}
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	renders := map[string]string{
		// only through the second generator: its CRD is production's
		"system/scan": "apiVersion: scan.example.io/v1\nkind: Report\n",
		// the testbed's values switch the mesh's policy on (render-chart's env reaches the render)
		"testbed system/mesh": "apiVersion: metrics.example.io/v1\nkind: Monitor\n---\napiVersion: policy.example.io/v1\nkind: Policy\n",
		// the mesh renders a Monitor though the testbed runs no metrics: its CRD goes in the testbed's list
		"system/mesh":         "apiVersion: v1\nkind: ConfigMap\n---\napiVersion: metrics.example.io/v1\nkind: Monitor\n",
		"system/metrics":      "apiVersion: apps/v1\nkind: Deployment\n",
		"platform/policy":     "apiVersion: policy.example.io/v1\nkind: Policy\n",
		"apps/web.yaml":       "apiVersion: gateway.networking.k8s.io/v1\nkind: HTTPRoute\n",
		"system/secret-store": "apiVersion: secrets.example.io/v1\nkind: Store\n",
	}
	render = func(_, target, env string) ([]byte, error) {
		if out, ok := renders[env+" "+target]; ok {
			return []byte(out), nil
		}
		return []byte(renders[target]), nil
	}
	t.Cleanup(func() { render = renderChart })
	return root
}

// An environment gets its components' CRDs and the CRDs of the kinds its renders use, nothing else
func TestListsHoldComponentsAndRenderedKinds(t *testing.T) {
	lists, err := Lists(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"mesh/gateways.example.io.yaml", "metrics/monitors.example.io.yaml",
		"policy/policies.example.io.yaml", "scan/reports.example.io.yaml", "secrets/stores.example.io.yaml"}; !reflect.DeepEqual(lists["prod"], want) {
		t.Errorf("prod: got %v, want %v", lists["prod"], want)
	}
	// mesh's own, and the Monitor and Policy its testbed render uses; no Store: the testbed's template adds no source
	if want := []string{"mesh/gateways.example.io.yaml", "metrics/monitors.example.io.yaml",
		"policy/policies.example.io.yaml"}; !reflect.DeepEqual(lists["testbed"], want) {
		t.Errorf("testbed: got %v, want %v", lists["testbed"], want)
	}
	if _, ok := lists["dev"]; ok {
		t.Error("the sandbox applies every CRD: no list")
	}
}

// A render using a kind no vendored CRD serves fails the generation: a sync would fail on it
func TestUnknownKindFails(t *testing.T) {
	root := fixture(t)
	render = func(_, target, _ string) ([]byte, error) {
		if target == "system/mesh" {
			return []byte("apiVersion: unknown.example.io/v1\nkind: Thing\n"), nil
		}
		return nil, nil
	}
	if _, err := Lists(root); err == nil || !strings.Contains(err.Error(), "unknown.example.io/Thing") {
		t.Errorf("got %v, want an error naming unknown.example.io/Thing", err)
	}
}

// Write then Check passes; a list edited by hand, or left behind after a change, fails and is named
func TestCheckFindsStaleList(t *testing.T) {
	root := fixture(t)
	if err := Write(root); err != nil {
		t.Fatal(err)
	}
	if err := Check(root); err != nil {
		t.Fatalf("after Write: %v", err)
	}
	path := filepath.Join(root, "system/crds/environments/testbed.txt")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "# Generated by lab crds generate") {
		t.Errorf("no header: %s", raw)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(raw), "metrics/monitors.example.io.yaml\n", "", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Check(root); err == nil || !strings.Contains(err.Error(), "testbed") {
		t.Errorf("got %v, want an error naming testbed", err)
	}
}

// Only Argo CD's own kinds are skipped: another argoproj.io kind without a vendored CRD fails like any other
func TestOnlyArgoCDKindsSkipped(t *testing.T) {
	root := fixture(t)
	render = func(_, target, _ string) ([]byte, error) {
		if target == "system/mesh" {
			return []byte("apiVersion: argoproj.io/v1alpha1\nkind: Application\n---\napiVersion: argoproj.io/v1alpha1\nkind: Rollout\n"), nil
		}
		return nil, nil
	}
	if _, err := Lists(root); err == nil || !strings.Contains(err.Error(), "argoproj.io/Rollout") || strings.Contains(err.Error(), "Application") {
		t.Errorf("got %v, want an error naming argoproj.io/Rollout only", err)
	}
}
