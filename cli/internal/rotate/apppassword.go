package rotate

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/duongle-steven/homelab/cli/internal/forgejo"
	"github.com/duongle-steven/homelab/cli/internal/workload"
)

// App changes an app's own admin password (Settings app names it).
type App interface {
	// SetPassword makes next the admin password; current is OpenBao's value, which the app may need to authorise
	// the change. It runs again when a rotation resumes, so it succeeds when next is already set.
	SetPassword(ctx context.Context, e Entry, current, next string) error
}

// Checker is an App that can tell whether a password works.
type Checker interface {
	Works(ctx context.Context, password string) (bool, error)
}

// Executor runs a command in a running pod of a workload (workload.Exec): stdin carries what must never be an
// argument.
type Executor interface {
	Exec(ctx context.Context, namespace string, w workload.Ref, container string, command []string, stdin io.Reader, stdout io.Writer) error
}

// AppPassword replaces an app's admin password: Prepare changes it in the app first, then writes it to OpenBao (so
// OpenBao never holds a password the app refuses, and a failed change leaves both on the old one); Switch refreshes
// the ExternalSecrets with no restart (the app already uses it; the Secret is read again only at the next start);
// Verify asks the app when it can tell; Revoke only drops the pending value (the app accepts one password). The
// value's length and character set are the generated secret's (Settings).
type AppPassword struct {
	Store     KV
	Workloads Readers
	Random    func(Entry) (string, error) // Password when nil
	Apps      map[string]App              // by Settings app
}

func (a AppPassword) app(e Entry) (App, error) {
	app := a.Apps[e.Settings["app"]]
	if app == nil {
		return nil, fmt.Errorf("%s: no way to change the password of app %q", e.Name, e.Settings["app"])
	}
	return app, nil
}

func (a AppPassword) Prepare(ctx context.Context, e Entry) error {
	app, err := a.app(e)
	if err != nil {
		return err
	}
	next, err := pending(ctx, a.Store, a.Random, e)
	if err != nil {
		return hide(err, next)
	}
	current, err := a.current(ctx, e)
	if err != nil {
		return hide(err, next)
	}
	// Equal once OpenBao holds the new one: the app was changed before
	if current != next {
		if err := app.SetPassword(ctx, e, current, next); err != nil {
			return fmt.Errorf("change the password in %s: %w", e.Settings["app"], hide(err, next, current))
		}
	}
	return hide(writeValue(ctx, a.Store, e, next), next, current)
}

func (a AppPassword) current(ctx context.Context, e Entry) (string, error) {
	if len(e.Paths) == 0 {
		return "", fmt.Errorf("%s has no paths", e.Name)
	}
	data, err := a.Store.Read(ctx, e.Paths[0])
	if err != nil {
		return "", fmt.Errorf("read secret/%s failed", e.Paths[0])
	}
	return data[e.Settings["key"]], nil
}

func (a AppPassword) Switch(ctx context.Context, e Entry) error { return refresh(ctx, a.Workloads, e) }

func (a AppPassword) Verify(ctx context.Context, e Entry) error {
	app, err := a.app(e)
	if err != nil {
		return err
	}
	checker, ok := app.(Checker)
	if !ok {
		return nil
	}
	current, err := a.current(ctx, e)
	if err != nil {
		return err
	}
	works, err := checker.Works(ctx, current)
	if err != nil {
		return hide(err, current)
	}
	if !works {
		return fmt.Errorf("%s refuses the password OpenBao holds at secret/%s", e.Settings["app"], e.Paths[0])
	}
	return nil
}

func (a AppPassword) Revoke(ctx context.Context, e Entry) error { return dropPending(ctx, a.Store, e) }

// target is the workload whose pod runs an app's command: the first reader's first workload
func target(e Entry) (string, workload.Ref, error) {
	for _, r := range namespaced(e) {
		if len(r.Workloads) > 0 {
			return r.Namespace, r.Workloads[0], nil
		}
	}
	return "", workload.Ref{}, fmt.Errorf("%s has no workload to run the app's command in", e.Name)
}

// ForgejoAdmin changes Forgejo's admin password through its admin API, authenticated with the current one.
// Forgejo's init container sets the password from the Secret at every start (passwordMode keepUpdated), so the
// entry rotates while it and lab infra forgejo sync are not running (its class).
type ForgejoAdmin struct {
	User     string // forgejo_admin when empty (the Secret forgejo-admin's username)
	Endpoint func(ctx context.Context) (forgejo.Endpoint, error)
}

func (f ForgejoAdmin) user() string { return cmp.Or(f.User, "forgejo_admin") }

// Works reports whether Forgejo accepts password for the admin.
func (f ForgejoAdmin) Works(ctx context.Context, password string) (bool, error) {
	ep, err := f.Endpoint(ctx)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.URL+"/api/v1/user", nil)
	if err != nil {
		return false, err
	}
	req.SetBasicAuth(f.user(), password)
	resp, err := ep.HTTP.Do(req)
	if err != nil {
		return false, fmt.Errorf("Forgejo at %s: %w", ep.URL, hide(err, password))
	}
	_ = resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return false, nil
	}
	return false, fmt.Errorf("Forgejo at %s answered %s to the admin's login", ep.URL, resp.Status)
}

func (f ForgejoAdmin) SetPassword(ctx context.Context, _ Entry, current, next string) error {
	// A resumed run: Forgejo has the new one already
	if ok, err := f.Works(ctx, next); err != nil {
		return err
	} else if ok {
		return nil
	}
	ep, err := f.Endpoint(ctx)
	if err != nil {
		return err
	}
	client, err := sdk.NewClient(ep.URL, sdk.SetBasicAuth(f.user(), current), sdk.SetContext(ctx), sdk.SetHTTPClient(ep.HTTP))
	if err != nil {
		return fmt.Errorf("Forgejo client: %w", hide(err, current, next))
	}
	never := false
	if _, err := client.AdminEditUser(f.user(), sdk.EditUserOption{LoginName: f.user(), Password: next, MustChangePassword: &never}); err != nil {
		return fmt.Errorf("set %s's password: %w", f.user(), hide(err, current, next))
	}
	return nil
}

// GrafanaAdmin sets Grafana's admin password with its own CLI in the Grafana container, the password on stdin
// (grafana cli admin reset-admin-password --password-from-stdin, Grafana's documented reset). Not the HTTP admin API:
// it takes basic auth, which the API server's service proxy consumes, and Grafana's namespace admits no caller
// from outside the mesh.
type GrafanaAdmin struct{ Exec Executor }

func (g GrafanaAdmin) SetPassword(ctx context.Context, e Entry, _, next string) error {
	namespace, w, err := target(e)
	if err != nil {
		return err
	}
	// The image is distroless (no shell): the CLI with the image's paths, so it reads the same grafana.ini and database
	// as the server (the pod's GF_PATHS_DATA reaches it through exec)
	command := []string{"grafana", "cli", "--homepath", "/usr/share/grafana", "--config", "/etc/grafana/grafana.ini",
		"admin", "reset-admin-password", "--password-from-stdin"}
	if err := g.Exec.Exec(ctx, namespace, w, "grafana", command, strings.NewReader(next+"\n"), nil); err != nil {
		return hide(err, next)
	}
	return nil
}

// PaperlessAdmin sets paperless's admin password with Django's manage.py changepassword in the paperless container,
// as the paperless user (like the image's init scripts). Without a terminal, Django's getpass reads the password
// and its confirmation from stdin.
type PaperlessAdmin struct {
	Exec Executor
	User string // admin when empty (PAPERLESS_ADMIN_USER)
}

func (p PaperlessAdmin) SetPassword(ctx context.Context, e Entry, _, next string) error {
	namespace, w, err := target(e)
	if err != nil {
		return err
	}
	if strings.ContainsAny(next, "\r\n") {
		return errors.New("the new password holds a line break")
	}
	script := `cd "${PAPERLESS_SRC_DIR:-/usr/src/paperless/src}" || exit 1
if [ "$(id -u)" = 0 ]; then exec s6-setuidgid paperless python3 manage.py changepassword "$1"; fi
exec python3 manage.py changepassword "$1"`
	command := []string{"sh", "-c", script, "sh", cmp.Or(p.User, "admin")}
	if err := p.Exec.Exec(ctx, namespace, w, "main", command, strings.NewReader(next+"\n"+next+"\n"), nil); err != nil {
		return hide(err, next)
	}
	return nil
}
