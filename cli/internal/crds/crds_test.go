package crds

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The environments' ApplicationSets, as system/argocd's values files write them (nested in matrices)
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
                    - path: platform/*
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
		// the mesh renders a Monitor though the testbed runs no metrics: its CRD goes in the testbed's list
		"system/mesh":         "apiVersion: v1\nkind: ConfigMap\n---\napiVersion: metrics.example.io/v1\nkind: Monitor\n",
		"system/metrics":      "apiVersion: apps/v1\nkind: Deployment\n",
		"platform/policy":     "apiVersion: policy.example.io/v1\nkind: Policy\n",
		"apps/web.yaml":       "apiVersion: gateway.networking.k8s.io/v1\nkind: HTTPRoute\n",
		"system/secret-store": "apiVersion: secrets.example.io/v1\nkind: Store\n",
		"system/egress":       "apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\n",
	}
	render = func(_, target, _ string) ([]byte, error) { return []byte(renders[target]), nil }
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
		"policy/policies.example.io.yaml", "secrets/stores.example.io.yaml"}; !reflect.DeepEqual(lists["prod"], want) {
		t.Errorf("prod: got %v, want %v", lists["prod"], want)
	}
	// mesh's own, the Monitor its render uses, and the Store every Application's secret-store source renders; not policy
	if want := []string{"mesh/gateways.example.io.yaml", "metrics/monitors.example.io.yaml",
		"secrets/stores.example.io.yaml"}; !reflect.DeepEqual(lists["testbed"], want) {
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
