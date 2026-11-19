package offline

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runIn(t *testing.T, script string, args ...string) string {
	t.Helper()
	cmd := exec.Command("sh", append([]string{"-c", script}, args...)...)
	cmd.Dir = repo.Root()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())
	return strings.TrimSpace(string(out))
}

// docs/superpowers/specs/2026-11-18-app-delivery-design.md: images in zot come only from the CI (user ci) or an admin;
// editors and users read, and so does the read-only account puller that pods and Kargo pull with
func TestZotWriters(t *testing.T) {
	for _, e := range []string{"", "dev"} {
		var config struct {
			HTTP struct {
				AccessControl struct {
					Repositories map[string]struct {
						Policies []struct {
							Users   []string `json:"users"`
							Groups  []string `json:"groups"`
							Actions []string `json:"actions"`
						} `json:"policies"`
					} `json:"repositories"`
				} `json:"accessControl"`
			} `json:"http"`
		}
		raw := runIn(t, `scripts/render-chart platform/zot $0 | yq 'select(.kind == "ConfigMap" and .metadata.name == "zot-config") | .data["config.json"]'`, e)
		require.NoError(t, json.Unmarshal([]byte(raw), &config))
		writers, readers := []string{}, []string{}
		for _, repository := range config.HTTP.AccessControl.Repositories {
			for _, p := range repository.Policies {
				who := append(append([]string{}, p.Users...), p.Groups...)
				for _, a := range p.Actions {
					switch a {
					case "create", "update":
						writers = append(writers, who...)
					case "read":
						readers = append(readers, who...)
					}
				}
			}
		}
		assert.ElementsMatch(t, []string{"homelab-admins", "homelab-admins", "ci", "ci"}, writers, "env=%s: only the CI and admins push", e)
		assert.Subset(t, readers, []string{"homelab-editors", "homelab-users", "puller"}, "env=%s", e)
		assert.Contains(t, runIn(t, `scripts/render-chart platform/zot $0 | yq 'select(.kind == "ExternalSecret" and .metadata.name == "zot-auth") | .spec.target.template.data.htpasswd'`, e),
			`htpasswd "puller"`, "env=%s: puller is in zot's htpasswd", e)
	}
}

// renderSecretStoreFor renders system/secret-store into namespace as an Application does
func renderSecretStoreFor(t *testing.T, namespace, expr string) string {
	t.Helper()
	return runIn(t, `helm template secret-store system/secret-store --namespace "$0" --set global.baseDomain=example.com | yq -N "$1"`, namespace, expr)
}

// A namespace that pulls our images gets Secret zot-pull (dockerconfigjson for registry.<domain>, user puller) from its
// own branch of OpenBao; any other namespace gets none
func TestZotPullSecret(t *testing.T) {
	pull := `select(.kind == "ExternalSecret" and .metadata.name == "zot-pull")`
	listed := strings.Fields(runIn(t, `yq '.zotPull[]' system/secret-store/values.yaml`))
	require.NotEmpty(t, listed, "no namespace pulls from zot yet: the list should name at least kargo")
	ns := listed[0]
	assert.Equal(t, "kubernetes.io/dockerconfigjson", renderSecretStoreFor(t, ns, pull+` | .spec.target.template.type`))
	assert.Equal(t, ns+"/generated/zot-puller", renderSecretStoreFor(t, ns, pull+` | .spec.data[0].remoteRef.key`))
	assert.Contains(t, renderSecretStoreFor(t, ns, pull+` | .spec.target.template.data[".dockerconfigjson"]`), "registry.example.com")
	assert.Empty(t, renderSecretStoreFor(t, "grafana", pull+` | .metadata.name`))
}

// Pods resolve registry.<domain> to the LAN Gateway before LAN DNS exists (production's names are not in public DNS);
// the sandbox keeps its whole-domain rewrite
func TestRegistryResolvesInPods(t *testing.T) {
	production := runIn(t, `scripts/render-chart system/istio-system | yq 'select(.kind == "ConfigMap" and .metadata.name == "coredns-custom") | .data | to_entries | map(.value) | join("\n")'`)
	assert.Contains(t, production, `rewrite name exact registry.hoangduong.dev gateway-istio.istio-system.svc.cluster.local`)
	assert.NotContains(t, production, "regex", "production rewrites only the registry")
	sandbox := runIn(t, `scripts/render-chart system/istio-system dev | yq 'select(.kind == "ConfigMap" and .metadata.name == "coredns-custom") | .data | to_entries | map(.value) | join("\n")'`)
	assert.Contains(t, sandbox, `rewrite name regex (.*)\.127-0-0-1\.nip\.io`)
}
