package test

import (
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type generatedEntry struct {
	Name         string   `json:"name"`
	Key          string   `json:"key"`
	Length       int      `json:"length"`
	Special      bool     `json:"special"`
	Destinations []string `json:"destinations"`
}

func renderGenerated(t *testing.T, env, expr string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", `scripts/render-chart platform/generated-secrets "$1" | yq -N -o=json -I=0 "$0"`, expr, env)
	cmd.Dir = ".."
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return string(out)
}

// Generated secrets go straight to OpenBao: per entry a Password generator and a PushSecret that runs it, writes only
// missing destinations (IfNotExists) and never deletes; no ExternalSecret or Secret holds a copy that could differ
// from OpenBao's value (ESO's CreatedOnce regenerates under a stale cache)
func TestGeneratedSecrets(t *testing.T) {
	values, err := exec.Command("yq", "-o=json", "-I=0", ".secrets + .extraSecrets", "../platform/generated-secrets/values.yaml").Output()
	require.NoError(t, err)
	var entries []generatedEntry
	require.NoError(t, json.Unmarshal(values, &entries))
	require.NotEmpty(t, entries)
	for _, env := range []string{"prod", "dev"} {
		kinds := renderGenerated(t, env, `[.kind] | .[]`)
		assert.NotContains(t, kinds, `"ExternalSecret"`, env)
		assert.NotContains(t, kinds, `"Secret"`, env)
		for _, e := range entries {
			length := e.Length
			if length == 0 {
				length = 32
			}
			symbols := 0
			if e.Special {
				symbols = 6
			}
			var gen struct {
				Length  int `json:"length"`
				Symbols int `json:"symbols"`
			}
			require.NoError(t, json.Unmarshal([]byte(renderGenerated(t, env, `select(.kind == "Password" and .metadata.name == "`+e.Name+`") | .spec`)), &gen), e.Name)
			assert.Equal(t, length, gen.Length, e.Name)
			assert.Equal(t, symbols, gen.Symbols, e.Name)

			var push struct {
				RefreshInterval string `json:"refreshInterval"`
				UpdatePolicy    string `json:"updatePolicy"`
				DeletionPolicy  string `json:"deletionPolicy"`
				SecretStoreRefs []struct {
					Name string `json:"name"`
				} `json:"secretStoreRefs"`
				Selector struct {
					GeneratorRef struct {
						Kind string `json:"kind"`
						Name string `json:"name"`
					} `json:"generatorRef"`
				} `json:"selector"`
				Data []struct {
					Match struct {
						SecretKey string `json:"secretKey"`
						RemoteRef struct {
							RemoteKey string `json:"remoteKey"`
							Property  string `json:"property"`
						} `json:"remoteRef"`
					} `json:"match"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal([]byte(renderGenerated(t, env, `select(.kind == "PushSecret" and .metadata.name == "`+e.Name+`") | .spec`)), &push), e.Name)
			assert.Equal(t, "1h", push.RefreshInterval, e.Name)
			assert.Equal(t, "IfNotExists", push.UpdatePolicy, e.Name)
			assert.Equal(t, "None", push.DeletionPolicy, e.Name)
			require.Len(t, push.SecretStoreRefs, 1, e.Name)
			assert.Equal(t, "openbao-writer", push.SecretStoreRefs[0].Name, e.Name)
			assert.Equal(t, "Password", push.Selector.GeneratorRef.Kind, e.Name)
			assert.Equal(t, e.Name, push.Selector.GeneratorRef.Name, e.Name)
			var remoteKeys []string
			for _, d := range push.Data {
				assert.Equal(t, "password", d.Match.SecretKey, e.Name)
				assert.Equal(t, e.Key, d.Match.RemoteRef.Property, e.Name)
				remoteKeys = append(remoteKeys, d.Match.RemoteRef.RemoteKey)
			}
			assert.Equal(t, e.Destinations, remoteKeys, e.Name)
		}
	}
}

// OpenBao's writer is the generated-secrets namespace's default service account, nothing else
func TestGeneratedSecretsWriterBinding(t *testing.T) {
	out, err := exec.Command("grep", "-n", "bound_service_account_namespaces = \\[\"generated-secrets\"\\]", "../platform/openbao/values.yaml").CombinedOutput()
	require.NoError(t, err, string(out))
}
