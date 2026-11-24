package offline

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The blog's template (docs/superpowers/specs/2026-11-21-blog-design.md): a Hugo site with PaperMod, built by the
// golden path into static-web-server on 8080 as non-root, every download pinned and checked, no person's data
func TestHugoSiteTemplate(t *testing.T) {
	root := filepath.Join(repo.Root(), "backstage", "templates")
	raw, err := os.ReadFile(filepath.Join(root, "hugo-site", "template.yaml"))
	require.NoError(t, err)
	var tmpl struct {
		Metadata struct{ Name, Description string }
		Spec     struct {
			Steps []struct{ Action string }
		}
	}
	require.NoError(t, yaml.Unmarshal(raw, &tmpl))
	assert.Equal(t, "hugo-site", tmpl.Metadata.Name)
	assert.Contains(t, tmpl.Metadata.Description, "blog", "it says the blog's repository must be named blog")
	var actions []string
	for _, s := range tmpl.Spec.Steps {
		actions = append(actions, s.Action)
	}
	assert.Equal(t, []string{"fetch:template", "publish:gitea", "catalog:register"}, actions)

	skeleton := filepath.Join(root, "hugo-site", "skeleton")
	cf, err := os.ReadFile(filepath.Join(skeleton, "Containerfile"))
	require.NoError(t, err)
	containerfile := string(cf)
	froms := regexp.MustCompile(`(?m)^FROM\s+(\S+)`).FindAllStringSubmatch(containerfile, -1)
	require.NotEmpty(t, froms)
	for _, f := range froms {
		assert.Regexp(t, `:[0-9][^:]*$`, f[1], "FROM %s: an exact version", f[1])
		assert.NotContains(t, f[1], "latest")
	}
	final := containerfile[strings.LastIndex(containerfile, "\nFROM "):]
	assert.Regexp(t, `(?m)^USER [0-9]+$`, final, "the final stage runs as a numeric non-root user")
	assert.Contains(t, final, "EXPOSE 8080")
	assert.Equal(t, 2, strings.Count(containerfile, "--checksum=sha256:"), "Hugo and the theme are checked")

	config, err := os.ReadFile(filepath.Join(skeleton, "hugo.toml"))
	require.NoError(t, err)
	assert.Contains(t, string(config), "profileMode")
	// Every icon PaperMod links is in the site (static-site-responds fails a preview on any missing asset: PaperMod's
	// defaults name five files a new site does not have)
	for _, icon := range []string{"favicon", "favicon16x16", "favicon32x32", "apple_touch_icon", "safari_pinned_tab"} {
		assert.Regexp(t, `(?m)^\s*`+icon+` = "/favicon.svg"$`, string(config), icon)
	}
	assert.FileExists(t, filepath.Join(skeleton, "static", "favicon.svg"))

	// An e-mail address, or a phone number as people write one (+84 …, (028) …): hashes and versions are neither
	personal := regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[a-z]{2,}|\+[0-9]{1,3}[ .-]?[0-9][0-9 .-]{7,}|\([0-9]{2,4}\) ?[0-9][0-9 .-]{6,}`)
	require.NoError(t, filepath.Walk(skeleton, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range personal.FindAllString(string(body), -1) {
			assert.Fail(t, "personal data in the template", "%s: %q", path, m)
		}
		return nil
	}))

	// One Location lists every template (TEMPLATES_LOCATION)
	loc, err := os.ReadFile(filepath.Join(root, "templates.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(loc), "./ci-repository/template.yaml")
	assert.Contains(t, string(loc), "./hugo-site/template.yaml")
}
