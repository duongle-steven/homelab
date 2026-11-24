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
		cmd.Env = []string{"URL=" + srv.URL + "/", "PATH=" + os.Getenv("PATH")}
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
}
