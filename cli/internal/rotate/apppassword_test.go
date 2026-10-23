package rotate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/cli/internal/forgejo"
	"github.com/duongle-steven/homelab/cli/internal/workload"
)

// fakeApp records the change in the shared log, so the order against OpenBao's writes shows
type fakeApp struct {
	log           *[]string
	current, next *string
}

func (f fakeApp) SetPassword(_ context.Context, _ Entry, current, next string) error {
	*f.log = append(*f.log, "app")
	*f.current, *f.next = current, next
	return nil
}

func TestAppPasswordChangesTheAppFirst(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"forgejo.admin", "grafana.admin", "paperless.admin"} {
		t.Run(name, func(t *testing.T) {
			e := catalogEntry(t, name)
			key := e.Settings["key"]
			log := &[]string{}
			kv := newKV(log, map[string]map[string]string{e.Paths[0]: {key: "old"}})
			var current, next string
			n := 0
			m := AppPassword{
				Store: kv, Workloads: fakeReaders{log: log}, Random: fixed(&n),
				Apps: map[string]App{e.Settings["app"]: fakeApp{log: log, current: &current, next: &next}},
			}
			if err := m.Prepare(ctx, e); err != nil {
				t.Fatal(err)
			}
			app, write := slices.Index(*log, "app"), slices.Index(*log, "write "+e.Paths[0])
			if app < 0 || write < 0 || app > write {
				t.Fatalf("log %q: the app must change before OpenBao's %s", *log, e.Paths[0])
			}
			if current != "old" || next != newValue || kv.data[e.Paths[0]][key] != newValue {
				t.Fatal("the app was not given the current and the new password, or OpenBao lacks the new one")
			}
			// Resumed after the write: the app is not changed again, no new value is made
			if err := m.Prepare(ctx, e); err != nil {
				t.Fatal(err)
			}
			if n != 1 || strings.Count(strings.Join(*log, "\n"), "app") != 1 {
				t.Fatalf("a resumed Prepare made %d values, log %q", n, *log)
			}
			if err := m.Switch(ctx, e); err != nil {
				t.Fatal(err)
			}
			for _, l := range *log {
				if strings.HasPrefix(l, "restart") {
					t.Fatalf("Switch restarted a workload: %q", *log)
				}
			}
			if !slices.ContainsFunc(*log, func(l string) bool { return strings.HasPrefix(l, "refresh "+e.Readers[0].Namespace+"/") }) {
				t.Fatalf("Switch did not refresh the ExternalSecrets: %q", *log)
			}
			if err := m.Revoke(ctx, e); err != nil {
				t.Fatal(err)
			}
			if kv.data[RecordPath(e)][pendingKey] != "" {
				t.Error("Revoke left the pending value")
			}
		})
	}
}

// fakeExec is a pod's exec: it logs the command and keeps stdin; failing returns stdin in its error
type fakeExec struct {
	log     *[]string
	stdin   *string
	stdout  string
	failing bool
}

func (f fakeExec) Exec(_ context.Context, namespace string, w workload.Ref, container string, command []string, stdin io.Reader, stdout io.Writer) error {
	var in []byte
	if stdin != nil {
		in, _ = io.ReadAll(stdin)
	}
	if f.failing {
		return fmt.Errorf("exec in %s/%s failed, stdin was %q", namespace, w.Name, in)
	}
	*f.log = append(*f.log, fmt.Sprintf("exec %s/%s %s %s", namespace, w.Name, container, strings.Join(command, " ")))
	if f.stdin != nil {
		*f.stdin = string(in)
	}
	if stdout != nil {
		_, _ = io.WriteString(stdout, f.stdout)
	}
	return nil
}

func TestExecAppsPassThePasswordOnStdin(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct{ name, stdin string }{
		{"grafana.admin", newValue + "\n"},
		{"paperless.admin", newValue + "\n" + newValue + "\n"},
	} {
		e := catalogEntry(t, tc.name)
		log, stdin := &[]string{}, ""
		x := fakeExec{log: log, stdin: &stdin}
		apps := map[string]App{"grafana": GrafanaAdmin{Exec: x}, "paperless": PaperlessAdmin{Exec: x}}
		if err := apps[e.Settings["app"]].SetPassword(ctx, e, "old", newValue); err != nil {
			t.Fatal(err)
		}
		if stdin != tc.stdin {
			t.Errorf("%s: stdin is not the new password", tc.name)
		}
		if len(*log) != 1 || strings.Contains((*log)[0], newValue) {
			t.Errorf("%s: the command holds the password or ran %d times", tc.name, len(*log))
		}
		if !strings.Contains((*log)[0], e.Readers[0].Namespace+"/"+e.Readers[0].Workloads[0].Name) {
			t.Errorf("%s: ran in %q, not the reader's workload", tc.name, (*log)[0])
		}
	}
}

// forgejoServer is Forgejo's API: it accepts basic auth with *password and sets it with the admin API; failing
// answers 500 with the request body
func forgejoServer(t *testing.T, password *string, failing bool) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/version" {
			_, _ = io.WriteString(w, `{"version":"13.0.0"}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if failing && r.Method == http.MethodGet {
			http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if failing {
			_, p, _ := r.BasicAuth()
			http.Error(w, fmt.Sprintf(`{"message":"no: %s %s"}`, p, body), http.StatusInternalServerError)
			return
		}
		if u, p, ok := r.BasicAuth(); !ok || u != "forgejo_admin" || p != *password {
			http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/user":
			_, _ = io.WriteString(w, `{"login":"forgejo_admin"}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/admin/users/forgejo_admin":
			var opt struct {
				Password  string `json:"password"`
				LoginName string `json:"login_name"`
			}
			if err := json.Unmarshal(body, &opt); err != nil || opt.Password == "" || opt.LoginName != "forgejo_admin" {
				http.Error(w, `{"message":"bad"}`, http.StatusUnprocessableEntity)
				return
			}
			*password = opt.Password
			_, _ = io.WriteString(w, `{"login":"forgejo_admin"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func forgejoAdmin(s *httptest.Server) ForgejoAdmin {
	return ForgejoAdmin{Endpoint: func(context.Context) (forgejo.Endpoint, error) {
		return forgejo.Endpoint{URL: s.URL, HTTP: s.Client()}, nil
	}}
}

func TestForgejoAdminSetsThePassword(t *testing.T) {
	ctx := context.Background()
	e := catalogEntry(t, "forgejo.admin")
	password := "old"
	f := forgejoAdmin(forgejoServer(t, &password, false))
	if err := f.SetPassword(ctx, e, "old", newValue); err != nil {
		t.Fatal(err)
	}
	if password != newValue {
		t.Fatal("Forgejo's password did not change")
	}
	// Resumed after the change, with OpenBao still holding the old one: the new one already works
	if err := f.SetPassword(ctx, e, "old", newValue); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.Works(ctx, newValue); err != nil || !ok {
		t.Fatalf("Works(new) = %v, %v", ok, err)
	}
	if ok, err := f.Works(ctx, "old"); err != nil || ok {
		t.Fatalf("Works(old) = %v, %v", ok, err)
	}
}

// failingApps are the package's App implementations over failing fakes
func failingApps(t *testing.T, log *[]string) map[string]App {
	password := ""
	x := fakeExec{log: log, failing: true}
	return map[string]App{
		"forgejo":   forgejoAdmin(forgejoServer(t, &password, true)),
		"grafana":   GrafanaAdmin{Exec: x},
		"paperless": PaperlessAdmin{Exec: x},
	}
}
