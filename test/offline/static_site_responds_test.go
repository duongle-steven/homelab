package offline

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
)

// static-site-responds (system/argo-rollouts) checks a blue-green preview before the switch: its page and every
// same-origin stylesheet, script and image the page names answer 2xx; other origins are not its to check
func TestStaticSiteRespondsChecksAssets(t *testing.T) {
	script := filepath.Join(repo.Root(), "system", "argo-rollouts", "files", "static-site-responds.sh")
	page := `<!doctype html><html><head>
<link rel="stylesheet" href="/css/a.css" integrity="sha256-x">
<link rel="stylesheet" href="https://fonts.example.com/f.css">
<link rel="icon" href="/favicon.ico">
<script src="js/b.js"></script>
</head><body><img src="/img/me.png" alt=""></body></html>`
	siteOrigin := "https://www.example.test"
	run := func(missing string, pageStatus int) (string, error) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/":
				w.WriteHeader(pageStatus)
				_, _ = w.Write([]byte(page))
			case r.URL.Path == missing:
				http.NotFound(w, r)
			default:
				_, _ = w.Write([]byte("ok"))
			}
		}))
		defer srv.Close()
		cmd := exec.Command("sh", script)
		cmd.Env = []string{"URL=" + srv.URL + "/", "SITE_ORIGIN=" + siteOrigin, "PATH=" + os.Getenv("PATH")}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := run("", http.StatusOK)
	assert.NoError(t, err, "every asset answers (the other origin is not fetched): %s", out)
	out, err = run("/css/a.css", http.StatusOK)
	assert.Error(t, err, "a missing stylesheet")
	assert.Contains(t, out, "/css/a.css")
	out, err = run("/js/b.js", http.StatusOK)
	assert.Error(t, err, "a missing script, named relatively")
	assert.Contains(t, out, "js/b.js")
	_, err = run("", http.StatusInternalServerError)
	assert.Error(t, err, "the page itself fails")

	// hugo --minify drops attribute quotes (PaperMod's real page: href=/assets/css/stylesheet.<hash>.css)
	page = `<!doctype html><html><head><link crossorigin=anonymous href=/assets/css/s.abc.css integrity=sha256-x rel="preload stylesheet" as=style><link rel=icon href='/favicon.ico'></head><body><img src=/img/me.png alt></body></html>`
	out, err = run("/assets/css/s.abc.css", http.StatusOK)
	assert.Error(t, err, "a missing stylesheet named without quotes")
	assert.Contains(t, out, "/assets/css/s.abc.css")
	out, err = run("/img/me.png", http.StatusOK)
	assert.Error(t, err, "a missing image named without quotes")
	out, err = run("", http.StatusOK)
	assert.NoError(t, err, out)
	assert.Contains(t, out, "3 assets", "every asset counted, quoted or not")

	// A real baseURL makes Hugo name every asset by the public origin: fetched from the preview, never skipped as
	// another origin (the final review, 2026-11-24)
	page = `<!doctype html><html><head><link href=https://www.example.test/assets/css/s.abc.css rel=stylesheet><link href=//www.example.test/favicon.ico rel=icon></head><body></body></html>`
	out, err = run("/assets/css/s.abc.css", http.StatusOK)
	assert.Error(t, err, "a missing stylesheet named by the public origin")
	assert.Contains(t, out, "s.abc.css")
	out, err = run("", http.StatusOK)
	assert.NoError(t, err, out)
	assert.Contains(t, out, "2 assets", "the public origin's assets, absolute and protocol-relative")

	// A page naming nothing of its own checks nothing: refused rather than passed
	page = `<!doctype html><html><head><link rel="stylesheet" href="https://fonts.example.com/f.css"></head><body>hi</body></html>`
	out, err = run("", http.StatusOK)
	assert.Error(t, err, "no asset on the site's origin")
	assert.Contains(t, out, "no asset")

	// The "coming soon" page (apps/blog.yaml) on a preview means its release file left it mounted over the site
	page = `<!doctype html><html><head><meta name="homelab-coming-soon"><link rel=icon href=/favicon.ico></head><body>Coming soon.</body></html>`
	out, err = run("", http.StatusOK)
	assert.Error(t, err, "the coming soon page is never switched to")
	assert.Contains(t, out, "coming soon")
}
