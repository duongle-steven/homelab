package offline

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The blog on the app road, released blue-green (docs/superpowers/specs/2026-11-21-blog-design.md): its hashed
// assets cannot be mixed across versions, so every visitor switches at once, after the preview passed
// static-site-responds, and back if gateway-success-rate fails after the switch
func TestBlogOnTheRoad(t *testing.T) {
	for _, env := range []string{"", "dev"} {
		render := func(expr string) string { return renderVelero(t, "apps/blog.yaml", env, expr) }
		bg := `select(.kind == "Rollout") | .spec.strategy.blueGreen | `
		assert.Equal(t, "blog-active blog-preview true", render(bg+`.activeService + " " + .previewService + " " + (.autoPromotionEnabled | tostring)`), env)
		assert.Equal(t, "static-site-responds", render(bg+`.prePromotionAnalysis.templates[0].templateName`), env)
		assert.Equal(t, "gateway-success-rate", render(bg+`.postPromotionAnalysis.templates[0].templateName`), env)
		assert.Equal(t, "true", render(`select(.kind == "Rollout") | .spec.progressDeadlineAbort`), env)
		assert.Equal(t, "false", render(`select(.kind == "Rollout") | .spec.strategy | has("canary")`), env)

		assert.Equal(t, "blog-active:8080", render(`select(.kind == "HTTPRoute") | .spec.rules[].backendRefs[] | .name + ":" + (.port | tostring)`), env)
		assert.Regexp(t, `^www\.`, render(`select(.kind == "HTTPRoute") | .spec.hostnames[]`), env)
		assert.Empty(t, render(`select(.kind == "PolicyException") | .metadata.name`), env, "the image is non-root: no exception")
		assert.Equal(t, "true", render(`select(.kind == "Deployment") | .spec.template.spec.securityContext.runAsNonRoot`), env)
		assert.Equal(t, `["^[0-9a-f]{40}$"]`, render(`select(.kind == "Warehouse") | .spec.subscriptions[0].image.allowTagsRegexes | @json`), env)
		// Before the first promotion, /public is the "coming soon" page
		assert.Equal(t, "/public", render(`select(.kind == "Deployment") | .spec.template.spec.containers[0].volumeMounts[] | select(.name == "coming-soon") | .mountPath`), env)
	}
}

// The release file Kargo writes turns the "coming soon" mount off, so it never shadows the real site
func TestBlogReleaseDropsComingSoon(t *testing.T) {
	contents := renderVelero(t, "apps/blog.yaml", "", `select(.kind == "Stage" and .metadata.name == "production") | .spec.promotionTemplate.spec.steps[] | select(.uses == "file-write") | .config.contents`)
	require.NotEmpty(t, contents)
	release := regexp.MustCompile(`\$\{\{[^}]*Tag[^}]*\}\}@\$\{\{[^}]*\}\}`).ReplaceAllString(contents, "0123abc@sha256:"+strings.Repeat("0", 64))
	release = regexp.MustCompile(`\$\{\{[^}]*\}\}`).ReplaceAllString(release, "registry.example.com/blog")
	file := filepath.Join(t.TempDir(), "blog.yaml")
	require.NoError(t, os.WriteFile(file, []byte(release), 0o644))
	cmd := exec.Command("sh", "-c", `scripts/render-chart apps/blog.yaml "" -f "$0" | yq -o=json -I=0 'select(.kind == "Deployment") | .spec.template.spec'`, file)
	cmd.Dir = repo.Root()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.NotContains(t, string(out), "coming-soon", "the real site is not shadowed")
	assert.Contains(t, string(out), `"image":"registry.example.com/blog:0123abc@sha256:`)
}
