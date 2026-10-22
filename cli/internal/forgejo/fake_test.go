package forgejo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

type fakeUser struct {
	loginName  string
	sourceID   int64
	password   string
	restricted bool
	tokens     map[int64]fakeToken
}

type fakeTeam struct {
	org                     string
	name, permission        string
	canCreateOrgRepo        bool
	includesAllRepositories bool
	units                   map[string]string
}

type fakeApp struct {
	name, clientID, secret string
	confidential           bool
	redirects              []string
}

type fakeToken struct {
	name, sha1 string
	scopes     []string
}

// fakeForgejo implements the API calls Sync makes, in memory; writes counts every mutating request.
type fakeForgejo struct {
	mu    sync.Mutex
	admin string
	// orgs holds each org's visibility
	orgs          map[string]string
	repo          bool
	users         map[string]*fakeUser
	collaborators map[string]string
	// teams grants a permission through a team (Forgejo reports the higher one)
	teams      map[string]string
	protection map[string]any
	orgTeams   map[int64]*fakeTeam
	apps       map[int64]*fakeApp
	writes     int
	nextID     int64
}

func newFakeForgejo(t *testing.T) (*fakeForgejo, Endpoint) {
	f := &fakeForgejo{admin: "admin-password", orgs: map[string]string{}, orgTeams: map[int64]*fakeTeam{}, apps: map[int64]*fakeApp{}, users: map[string]*fakeUser{}, collaborators: map[string]string{}, teams: map[string]string{}}
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
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if r.URL.Path == "/api/healthz" {
		reply(http.StatusOK, map[string]string{"status": "pass"})
		return
	}
	if path == "/version" {
		reply(http.StatusOK, map[string]string{"version": "16.0.6"})
		return
	}
	if r.URL.Path == "/login/oauth/access_token" && r.Method == http.MethodPost {
		// As Forgejo with a code it never issued: refused either way, "invalid client secret" for a wrong secret.
		// It changes nothing, so it is no write
		f.writes--
		_ = r.ParseForm()
		description := "client is not authorized"
		for _, app := range f.apps {
			if app.clientID == r.PostForm.Get("client_id") && app.secret != r.PostForm.Get("client_secret") {
				description = "invalid client secret"
			}
		}
		reply(http.StatusBadRequest, map[string]string{"error": "unauthorized_client", "error_description": description})
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
	case len(parts) == 2 && parts[0] == "orgs" && r.Method == http.MethodGet && admin:
		if _, ok := f.orgs[parts[1]]; !ok {
			reply(http.StatusNotFound, nil)
			return
		}
		reply(http.StatusOK, map[string]any{"username": parts[1], "visibility": f.orgs[parts[1]]})
	case path == "/orgs" && r.Method == http.MethodPost && admin:
		name := body["username"].(string)
		visibility, _ := body["visibility"].(string)
		f.orgs[name] = visibility
		reply(http.StatusCreated, map[string]any{"username": name, "visibility": visibility})
	case len(parts) == 3 && parts[0] == "orgs" && parts[2] == "teams" && admin:
		if _, ok := f.orgs[parts[1]]; !ok {
			reply(http.StatusNotFound, nil)
			return
		}
		if r.Method == http.MethodPost {
			f.nextID++
			f.orgTeams[f.nextID] = &fakeTeam{org: parts[1]}
			f.orgTeams[f.nextID].edit(body)
			reply(http.StatusCreated, f.orgTeams[f.nextID].json(f.nextID))
			return
		}
		list := []map[string]any{}
		for id, team := range f.orgTeams {
			if team.org == parts[1] {
				list = append(list, team.json(id))
			}
		}
		reply(http.StatusOK, page(r, list))
	case len(parts) == 2 && parts[0] == "teams" && r.Method == http.MethodPatch && admin:
		var id int64
		_, _ = fmt.Sscan(parts[1], &id)
		team := f.orgTeams[id]
		if team == nil {
			reply(http.StatusNotFound, nil)
			return
		}
		team.edit(body)
		reply(http.StatusOK, team.json(id))
	case path == "/user/applications/oauth2" && r.Method == http.MethodGet && admin:
		// As Forgejo: the list never holds a secret
		list := []map[string]any{}
		for id, app := range f.apps {
			list = append(list, map[string]any{"id": id, "name": app.name, "client_id": app.clientID,
				"confidential_client": app.confidential, "redirect_uris": app.redirects})
		}
		reply(http.StatusOK, page(r, list))
	case strings.HasPrefix(path, "/user/applications/oauth2") && (r.Method == http.MethodPost || r.Method == http.MethodPatch) && admin:
		var id int64
		if r.Method == http.MethodPost {
			f.nextID++
			id = f.nextID
			f.apps[id] = &fakeApp{clientID: fmt.Sprintf("client-%d", id)}
		} else if _, err := fmt.Sscan(parts[len(parts)-1], &id); err != nil || f.apps[id] == nil {
			reply(http.StatusNotFound, nil)
			return
		}
		app := f.apps[id]
		app.name, _ = body["name"].(string)
		app.confidential, _ = body["confidential_client"].(bool)
		app.redirects = nil
		for _, u := range body["redirect_uris"].([]any) {
			app.redirects = append(app.redirects, u.(string))
		}
		// As Forgejo: creating or updating an application issues a new secret
		f.nextID++
		app.secret = fmt.Sprintf("oauth-secret-%d", f.nextID)
		reply(http.StatusCreated, map[string]any{"id": id, "name": app.name, "client_id": app.clientID, "client_secret": app.secret,
			"confidential_client": app.confidential, "redirect_uris": app.redirects})
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
		reply(http.StatusOK, map[string]any{"login": parts[1], "login_name": u.loginName, "source_id": u.sourceID, "restricted": u.restricted})
	case path == "/admin/users" && r.Method == http.MethodPost && admin:
		name := body["username"].(string)
		login, _ := body["login_name"].(string)
		f.users[name] = &fakeUser{loginName: login, password: body["password"].(string), tokens: map[int64]fakeToken{}}
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
		// As Forgejo: 200 "none" for a user without access, the highest of collaborator and team otherwise
		rank := map[string]int{"": 0, "none": 0, "read": 1, "write": 2, "admin": 3}
		permission := f.collaborators[parts[4]]
		if rank[f.teams[parts[4]]] > rank[permission] {
			permission = f.teams[parts[4]]
		}
		if permission == "" {
			permission = "none"
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
		// As Forgejo: a null field is left unchanged
		for k, v := range body {
			if v != nil {
				f.protection[k] = v
			}
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

func (t *fakeTeam) edit(body map[string]any) {
	t.name, _ = body["name"].(string)
	t.permission, _ = body["permission"].(string)
	t.canCreateOrgRepo, _ = body["can_create_org_repo"].(bool)
	t.includesAllRepositories, _ = body["includes_all_repositories"].(bool)
	t.units = map[string]string{}
	units, _ := body["units_map"].(map[string]any)
	for k, v := range units {
		t.units[k] = v.(string)
	}
}

func (t *fakeTeam) json(id int64) map[string]any {
	return map[string]any{"id": id, "name": t.name, "permission": t.permission, "can_create_org_repo": t.canCreateOrgRepo,
		"includes_all_repositories": t.includesAllRepositories, "units_map": t.units}
}

// page serves one page of list (Forgejo's page and limit), so callers must follow pages
func page(r *http.Request, list []map[string]any) []map[string]any {
	sort.Slice(list, func(i, j int) bool { return fmt.Sprint(list[i]["id"]) < fmt.Sprint(list[j]["id"]) })
	n, size := 1, 50
	_, _ = fmt.Sscan(r.URL.Query().Get("page"), &n)
	_, _ = fmt.Sscan(r.URL.Query().Get("limit"), &size)
	start := (n - 1) * size
	if start >= len(list) {
		return []map[string]any{}
	}
	return list[start:min(start+size, len(list))]
}

type memoryTokens map[string]map[string]string

func (m memoryTokens) Read(_ context.Context, path string) (map[string]string, error) {
	return m[path], nil
}
func (m memoryTokens) Write(_ context.Context, path string, data map[string]string) error {
	m[path] = data
	return nil
}
