package forgejo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fakeUser struct {
	password   string
	restricted bool
	tokens     map[int64]fakeToken
}

type fakeToken struct {
	name, sha1 string
	scopes     []string
}

// fakeForgejo implements the API calls Sync makes, in memory; writes counts every mutating request.
type fakeForgejo struct {
	mu            sync.Mutex
	admin         string
	org, repo     bool
	users         map[string]*fakeUser
	collaborators map[string]string
	protection    map[string]any
	writes        int
	nextID        int64
}

func newFakeForgejo(t *testing.T) (*fakeForgejo, Endpoint) {
	f := &fakeForgejo{admin: "admin-password", users: map[string]*fakeUser{}, collaborators: map[string]string{}}
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	return f, Endpoint{URL: server.URL, HTTP: server.Client()}
}

func (f *fakeForgejo) who(r *http.Request) string {
	if user, password, ok := r.BasicAuth(); ok {
		if user == "forgejo_admin" && password == f.admin {
			return "forgejo_admin"
		}
		if u := f.users[user]; u != nil && u.password == password {
			return user
		}
		return ""
	}
	if token := strings.TrimPrefix(r.Header.Get("Authorization"), "token "); token != "" {
		for name, u := range f.users {
			for _, tk := range u.tokens {
				if tk.sha1 == token {
					return name
				}
			}
		}
	}
	return ""
}

func (f *fakeForgejo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	if r.Method != http.MethodGet {
		f.writes++
	}
	reply := func(code int, body any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if body != nil {
			_ = json.NewEncoder(w).Encode(body)
		}
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if r.URL.Path == "/api/healthz" {
		reply(http.StatusOK, map[string]string{"status": "pass"})
		return
	}
	if path == "/version" {
		reply(http.StatusOK, map[string]string{"version": "16.0.6"})
		return
	}
	who := f.who(r)
	if who == "" {
		reply(http.StatusUnauthorized, map[string]string{"message": "unauthorized"})
		return
	}
	admin := who == "forgejo_admin"
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case path == "/orgs/ops" && r.Method == http.MethodGet && admin:
		if !f.org {
			reply(http.StatusNotFound, nil)
			return
		}
		reply(http.StatusOK, map[string]any{"username": "ops"})
	case path == "/orgs" && r.Method == http.MethodPost && admin:
		f.org = true
		reply(http.StatusCreated, map[string]any{"username": "ops"})
	case path == "/repos/ops/homelab" && r.Method == http.MethodGet:
		if !f.repo || (!admin && f.collaborators[who] == "") {
			reply(http.StatusNotFound, nil)
			return
		}
		reply(http.StatusOK, map[string]any{"name": "homelab", "private": true, "default_branch": "master"})
	case (path == "/orgs/ops/repos" || path == "/org/ops/repos") && r.Method == http.MethodPost && admin:
		if body["private"] != true || body["default_branch"] != "master" {
			reply(http.StatusUnprocessableEntity, map[string]any{"message": fmt.Sprint(body)})
			return
		}
		f.repo = true
		reply(http.StatusCreated, map[string]any{"name": "homelab"})
	case len(parts) == 2 && parts[0] == "users" && r.Method == http.MethodGet && admin:
		u := f.users[parts[1]]
		if u == nil {
			reply(http.StatusNotFound, nil)
			return
		}
		reply(http.StatusOK, map[string]any{"login": parts[1], "restricted": u.restricted})
	case path == "/admin/users" && r.Method == http.MethodPost && admin:
		name := body["username"].(string)
		f.users[name] = &fakeUser{password: body["password"].(string), tokens: map[int64]fakeToken{}}
		reply(http.StatusCreated, map[string]any{"login": name})
	case len(parts) == 3 && parts[0] == "admin" && parts[1] == "users" && r.Method == http.MethodPatch && admin:
		u := f.users[parts[2]]
		if p, _ := body["password"].(string); p != "" {
			u.password = p
		}
		if restricted, ok := body["restricted"].(bool); ok {
			u.restricted = restricted
		}
		reply(http.StatusOK, map[string]any{"login": parts[2]})
	case len(parts) == 6 && parts[3] == "collaborators" && parts[5] == "permission" && admin:
		permission := f.collaborators[parts[4]]
		if permission == "" {
			reply(http.StatusNotFound, nil)
			return
		}
		reply(http.StatusOK, map[string]any{"permission": permission})
	case len(parts) == 5 && parts[3] == "collaborators" && r.Method == http.MethodPut && admin:
		f.collaborators[parts[4]] = body["permission"].(string)
		reply(http.StatusNoContent, nil)
	case path == "/repos/ops/homelab/branch_protections/master" && r.Method == http.MethodGet && admin:
		if f.protection == nil {
			reply(http.StatusNotFound, nil)
			return
		}
		reply(http.StatusOK, f.protection)
	case path == "/repos/ops/homelab/branch_protections" && r.Method == http.MethodPost && admin:
		f.protection = body
		reply(http.StatusCreated, body)
	case path == "/repos/ops/homelab/branch_protections/master" && r.Method == http.MethodPatch && admin:
		for k, v := range body {
			f.protection[k] = v
		}
		reply(http.StatusOK, f.protection)
	case len(parts) >= 3 && parts[0] == "users" && parts[2] == "tokens" && who == parts[1]:
		u := f.users[who]
		switch r.Method {
		case http.MethodGet:
			list := []map[string]any{}
			for id, tk := range u.tokens {
				list = append(list, map[string]any{"id": id, "name": tk.name})
			}
			reply(http.StatusOK, list)
		case http.MethodPost:
			f.nextID++
			scopes := []string{}
			for _, s := range body["scopes"].([]any) {
				scopes = append(scopes, s.(string))
			}
			tk := fakeToken{name: body["name"].(string), sha1: fmt.Sprintf("token-%s-%d", who, f.nextID), scopes: scopes}
			u.tokens[f.nextID] = tk
			reply(http.StatusCreated, map[string]any{"id": f.nextID, "name": tk.name, "sha1": tk.sha1})
		case http.MethodDelete:
			var id int64
			_, _ = fmt.Sscan(parts[3], &id)
			delete(u.tokens, id)
			reply(http.StatusNoContent, nil)
		}
	default:
		reply(http.StatusNotFound, map[string]string{"message": r.Method + " " + path + " as " + who})
	}
}

type memoryTokens map[string]map[string]string

func (m memoryTokens) Read(_ context.Context, path string) (map[string]string, error) { return m[path], nil }
func (m memoryTokens) Write(_ context.Context, path string, data map[string]string) error {
	m[path] = data
	return nil
}
