package secrets

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	authv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// fakeBao accepts the service account token "jwt-admin" for role admin and keeps KV v2 data in memory.
type fakeBao struct {
	mu      sync.Mutex
	data    map[string]map[string]any
	writes  int
	revoked bool
}

func (f *fakeBao) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/v1/auth/kubernetes/login":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["role"] != "admin" || body["jwt"] != "jwt-admin" {
			http.Error(w, `{"errors":["permission denied"]}`, http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"auth": map[string]any{"client_token": "bao-token"}})
	case r.Header.Get("X-Vault-Token") != "bao-token":
		http.Error(w, `{"errors":["permission denied"]}`, http.StatusForbidden)
	case r.URL.Path == "/v1/auth/token/revoke-self":
		f.revoked = true
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(r.URL.Path, "/v1/secret/data/"):
		path := strings.TrimPrefix(r.URL.Path, "/v1/secret/data/")
		switch r.Method {
		case http.MethodGet:
			data, ok := f.data[path]
			if !ok {
				http.Error(w, `{"errors":[]}`, http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": data, "metadata": map[string]any{"version": 1}}})
		default:
			var body struct {
				Data map[string]any `json:"data"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.data[path] = body.Data
			f.writes++
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"version": 1}})
		}
	default:
		http.NotFound(w, r)
	}
}

func kubeWithToken(t *testing.T) *fake.Clientset {
	t.Helper()
	kube := fake.NewClientset()
	kube.PrependReactor("create", "serviceaccounts", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create := action.(k8stesting.CreateAction)
		if create.GetSubresource() != "token" || create.GetNamespace() != "openbao" || create.(k8stesting.CreateActionImpl).Name != "openbao-admin" {
			t.Errorf("unexpected token request: %v", action)
		}
		request := create.GetObject().(*authv1.TokenRequest)
		if len(request.Spec.Audiences) != 1 || request.Spec.Audiences[0] != "openbao" {
			t.Errorf("audiences = %v", request.Spec.Audiences)
		}
		request.Status.Token = "jwt-admin"
		return true, request, nil
	})
	return kube
}

func TestStore(t *testing.T) {
	bao := &fakeBao{data: map[string]map[string]any{"forgejo/deploy": {"token": "old"}}}
	server := httptest.NewServer(bao)
	defer server.Close()
	ctx := context.Background()
	store, err := open(ctx, kubeWithToken(t), server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Read(ctx, "forgejo/deploy")
	if err != nil || got["token"] != "old" {
		t.Fatalf("Read() = %v, %v", got, err)
	}
	if got, err := store.Read(ctx, "argocd/forgejo"); err != nil || got != nil {
		t.Fatalf("Read() of a missing path = %v, %v", got, err)
	}
	if err := store.Write(ctx, "forgejo/deploy", map[string]string{"token": "old"}); err != nil || bao.writes != 0 {
		t.Fatalf("an unchanged Write() wrote: %v, %d", err, bao.writes)
	}
	if err := store.Write(ctx, "forgejo/deploy", map[string]string{"token": "new"}); err != nil || bao.writes != 1 {
		t.Fatalf("Write() = %v, %d writes", err, bao.writes)
	}
	if err := store.Close(ctx); err != nil || !bao.revoked {
		t.Fatalf("Close() = %v, revoked %v", err, bao.revoked)
	}
}

func TestStoreErrorsHideValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"errors":["secret-value-123"]}`, http.StatusInternalServerError)
	}))
	defer server.Close()
	_, err := open(context.Background(), kubeWithToken(t), server.URL, server.Client())
	if err == nil || strings.Contains(err.Error(), "jwt-admin") {
		t.Fatalf("open() = %v", err)
	}
}
