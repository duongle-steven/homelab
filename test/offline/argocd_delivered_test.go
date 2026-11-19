package offline

import (
	"strings"
	"testing"
	"text/template"

	sprig "github.com/go-task/slim-sprig/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// argoTemplate evaluates one of an ApplicationSet's goTemplate strings for a generated app, as Argo CD does (sprig)
func argoTemplate(t *testing.T, s string, params map[string]any) string {
	t.Helper()
	tmpl, err := template.New("").Funcs(sprig.TxtFuncMap()).Option("missingkey=error").Parse(s)
	require.NoError(t, err, s)
	var b strings.Builder
	require.NoError(t, tmpl.Execute(&b, params), s)
	return b.String()
}

// docs/superpowers/specs/2026-11-18-app-delivery-design.md: an app on the app road reads its version from Forgejo's
// ops/releases after its own file (missing before the first promotion), Kargo may sync it, and its namespace is a
// Kargo project's; every other app reads nothing more and needs no Forgejo
func TestDeliveredAppsReadReleases(t *testing.T) {
	for _, env := range []string{"", "dev"} {
		appset := renderVelero(t, "system/argocd", env, `select(.kind == "ApplicationSet" and .metadata.name == "apps") | .spec`)
		var spec struct {
			TemplatePatch string `yaml:"templatePatch"`
			Template      struct {
				Spec struct {
					Sources []struct {
						RepoURL        string `yaml:"repoURL"`
						TargetRevision string `yaml:"targetRevision"`
						Ref            string `yaml:"ref"`
						Helm           struct {
							ValueFiles              []string `yaml:"valueFiles"`
							IgnoreMissingValueFiles bool     `yaml:"ignoreMissingValueFiles"`
						} `yaml:"helm"`
					} `yaml:"sources"`
				} `yaml:"spec"`
			} `yaml:"template"`
		}
		require.NoError(t, yaml.Unmarshal([]byte(appset), &spec))
		var releases, chart = -1, -1
		for i, s := range spec.Template.Spec.Sources {
			switch {
			case s.Ref == "releases":
				releases = i
			case len(s.Helm.ValueFiles) > 0:
				chart = i
			}
		}
		require.NotEqual(t, -1, releases, "env=%s: no releases source", env)
		require.NotEqual(t, -1, chart, "env=%s", env)
		assert.True(t, spec.Template.Spec.Sources[chart].Helm.IgnoreMissingValueFiles, env)

		for _, c := range []struct {
			file, dir string
			delivered bool
		}{{"blog.yaml", "apps", true}, {"homepage.yaml", "apps", false}, {"delivery-test.yaml", "test/fixtures/apps", true}} {
			params := map[string]any{
				"path":     map[string]any{"filename": c.file, "path": c.dir},
				"metadata": map[string]any{"annotations": map[string]any{"repository": "https://github.com/x/homelab", "revision": "abc"}},
			}
			app := strings.TrimSuffix(c.file, ".yaml")
			src := spec.Template.Spec.Sources[releases]
			files := spec.Template.Spec.Sources[chart].Helm.ValueFiles
			assert.Equal(t, "$values/"+c.dir+"/"+c.file, argoTemplate(t, files[0], params), "env=%s %s: its own file first", env, app)
			assert.Equal(t, "$releases/"+app+"/production.yaml", argoTemplate(t, files[len(files)-1], params), "env=%s %s", env, app)
			patch := argoTemplate(t, spec.TemplatePatch, params)
			if c.delivered {
				assert.Equal(t, "http://forgejo-http.forgejo:3000/ops/releases", argoTemplate(t, src.RepoURL, params), "env=%s %s", env, app)
				assert.Equal(t, "main", argoTemplate(t, src.TargetRevision, params), "env=%s %s", env, app)
				assert.Contains(t, patch, "kargo.akuity.io/authorized-stage: "+app+":production", "env=%s %s", env, app)
				assert.Contains(t, patch, `kargo.akuity.io/project: "true"`, "env=%s %s", env, app)
			} else {
				assert.Equal(t, "https://github.com/x/homelab", argoTemplate(t, src.RepoURL, params), "env=%s %s: no Forgejo for it", env, app)
				assert.Equal(t, "abc", argoTemplate(t, src.TargetRevision, params), "env=%s %s", env, app)
				assert.NotContains(t, patch, "kargo", "env=%s %s", env, app)
			}
		}
	}
}
