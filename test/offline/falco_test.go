package offline

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// renderFalco renders system/falco for env ("", dev or testbed) with extra helm args and runs a yq expression on it
func renderFalco(t *testing.T, env, expr string, args ...string) string {
	t.Helper()
	if env == "" {
		env = "prod"
	}
	cmd := exec.Command("sh", append([]string{"-c", `expr="$1"; shift; scripts/render-chart system/falco "$@" | yq -N "$expr"`, "sh", expr, env}, args...)...)
	cmd.Dir = repo.Root()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())
	return strings.TrimSpace(string(out))
}

// docs/superpowers/specs/2026-11-10-runtime-detection-design.md: the modern eBPF driver, JSON events from warning up, no
// download at runtime, no other component, the accepted rules mounted; the testbed renders no kind whose CRD it lacks
func TestFalcoRender(t *testing.T) {
	appVersion := strings.TrimSpace(func() string {
		cmd := exec.Command("sh", "-c", `yq '.dependencies[0] | .repository + " " + .version' system/falco/Chart.yaml | { read -r repo version; helm show chart falco --repo "$repo" --version "$version"; } | yq .appVersion`)
		cmd.Dir = repo.Root()
		out, err := cmd.Output()
		require.NoError(t, err)
		return string(out)
	}())
	for _, env := range []string{"", "dev", "testbed"} {
		t.Run("env="+env, func(t *testing.T) {
			config := renderFalco(t, env, `select(.kind == "ConfigMap" and .metadata.name == "falco") | .data["falco.yaml"]`)
			require.NotEmpty(t, config, "no ConfigMap falco")
			var falco struct {
				Engine struct {
					Kind string `yaml:"kind"`
				} `yaml:"engine"`
				JSONOutput bool     `yaml:"json_output"`
				Priority   string   `yaml:"priority"`
				RulesFiles []string `yaml:"rules_files"`
			}
			require.NoError(t, yaml.Unmarshal([]byte(config), &falco))
			assert.Equal(t, "modern_ebpf", falco.Engine.Kind)
			assert.True(t, falco.JSONOutput)
			assert.Equal(t, "warning", falco.Priority)
			assert.Contains(t, falco.RulesFiles, "/etc/falco/falco_rules.yaml")
			assert.Contains(t, falco.RulesFiles, "/etc/falco/rules.d")
			assert.Contains(t, config, "/run/k3s/containerd/containerd.sock")

			containers := renderFalco(t, env, `select(.kind == "DaemonSet") | .spec.template.spec | [.initContainers[]?.name, .containers[].name] | .[]`)
			assert.NotContains(t, containers, "falcoctl-artifact-install")
			assert.NotContains(t, containers, "falcoctl-artifact-follow")
			assert.Empty(t, renderFalco(t, env, `select(.kind == "Deployment") | .metadata.name`), "no sidekick or metacollector")
			image := renderFalco(t, env, `select(.kind == "DaemonSet") | .spec.template.spec.containers[] | select(.name == "falco") | .image`)
			assert.True(t, strings.HasSuffix(image, ":"+appVersion), "image %s pins Falco %s", image, appVersion)
			assert.Equal(t, "falco-accepted-rules",
				renderFalco(t, env, `select(.kind == "DaemonSet") | .spec.template.spec.volumes[] | select(.configMap.name == "falco-accepted-rules") | .configMap.name`))

			// The vendored rules ride in the accepted rules' ConfigMap, before the accepted entries
			files, err := filepath.Glob(filepath.Join(repo.Root(), "system/falco/files/*.yaml"))
			require.NoError(t, err)
			for _, f := range files {
				assert.NotEmpty(t, renderFalco(t, env, `select(.kind == "ConfigMap" and .metadata.name == "falco-accepted-rules") | .data["`+filepath.Base(f)+`"]`), filepath.Base(f))
			}

			exceptions := renderFalco(t, env, `select(.kind == "PolicyException") | .metadata.name`)
			monitors := renderFalco(t, env, `select(.kind == "ServiceMonitor") | .metadata.name`)
			rules := renderFalco(t, env, `select(.kind == "PrometheusRule") | .metadata.name`)
			if env == "testbed" {
				assert.Empty(t, exceptions)
				assert.Empty(t, monitors)
				assert.Empty(t, rules)
				return
			}
			assert.Len(t, strings.Fields(exceptions), 1)
			assert.NotEmpty(t, renderFalco(t, env, `select(.kind == "PolicyException") | .metadata.annotations["homelab/reason"]`))
			assert.Contains(t, renderFalco(t, env, `select(.kind == "PolicyException") | .metadata.annotations["argocd.argoproj.io/sync-options"]`),
				"SkipDryRunOnMissingResource=true")
			assert.Len(t, strings.Fields(monitors), 1)
		})
	}
}

// Every accepted event says why, and an entry narrows its rule with an exception, never disables it
func TestFalcoAccepted(t *testing.T) {
	for _, file := range []string{"values.yaml", "values-local.yaml", "values-dev.yaml"} {
		raw, err := os.ReadFile(filepath.Join(repo.Root(), "system/falco", file))
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		var values struct {
			Accepted []map[string]any `yaml:"accepted"`
		}
		require.NoError(t, yaml.Unmarshal(raw, &values))
		for i, entry := range values.Accepted {
			for _, key := range []string{"rule", "name", "reason"} {
				assert.NotEmpty(t, entry[key], "%s: accepted[%d] has no %s", file, i, key)
			}
		}
	}

	// --set-json, since render-chart passes helm arguments before the chart's values files
	fixture := `accepted=[{"rule": "Read sensitive file untrusted", "name": "fixture", "fields": ["k8s.ns.name", "proc.name"], "comps": ["=", "="], "values": [["fixture-ns", "cat"]], "reason": "a fixture"}]`
	rendered := renderFalco(t, "", `select(.kind == "ConfigMap" and .metadata.name == "falco-accepted-rules") | .data["zz-accepted.yaml"]`, "--set-json", fixture)
	var rules []struct {
		Rule       string            `yaml:"rule"`
		Exceptions []map[string]any  `yaml:"exceptions"`
		Override   map[string]string `yaml:"override"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(rendered), &rules), rendered)
	require.Len(t, rules, 1, rendered)
	assert.Equal(t, "Read sensitive file untrusted", rules[0].Rule)
	require.Len(t, rules[0].Exceptions, 1)
	assert.Equal(t, "fixture", rules[0].Exceptions[0]["name"])
	assert.Equal(t, []any{"k8s.ns.name", "proc.name"}, rules[0].Exceptions[0]["fields"])
	assert.Equal(t, map[string]string{"exceptions": "append"}, rules[0].Override)
	assert.Contains(t, rendered, "# a fixture")
	assert.NotContains(t, rendered, "enabled: false")
}
