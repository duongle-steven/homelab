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
	"sync"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"
)

// fakeWoodpecker is Woodpecker's API as lab uses it: the admin's token, the agents, org ci's secrets
type fakeWoodpecker struct {
	mu      sync.Mutex
	token   string
	agents  map[int64]string // id → name
	secrets map[string]string
	resets  int
	log     []string
}

func (f *fakeWoodpecker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	f.log = append(f.log, r.Method+" "+r.URL.Path)
	switch {
	case r.Method == http.MethodDelete && r.URL.Path == "/api/user/token":
		f.resets++
		f.token = fmt.Sprintf("woodpecker-token-%d", f.resets)
		_, _ = io.WriteString(w, f.token)
	case r.Method == http.MethodGet && r.URL.Path == "/api/user":
		_ = json.NewEncoder(w).Encode(map[string]any{"login": "forgejo_admin", "admin": true})
	case r.Method == http.MethodGet && r.URL.Path == "/api/agents":
		list := []map[string]any{}
		for id, name := range f.agents {
			list = append(list, map[string]any{"id": id, "name": name})
		}
		_ = json.NewEncoder(w).Encode(list)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/agents/"):
		var id int64
		_, _ = fmt.Sscan(strings.TrimPrefix(r.URL.Path, "/api/agents/"), &id)
		delete(f.agents, id)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && r.URL.Path == "/api/orgs/lookup/ci":
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "name": "ci"})
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/orgs/7/secrets/"):
		name := strings.TrimPrefix(r.URL.Path, "/api/orgs/7/secrets/")
		if _, ok := f.secrets[name]; !ok {
			http.NotFound(w, r)
			return
		}
		var body struct{ Value string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.secrets[name] = body.Value
		_ = json.NewEncoder(w).Encode(map[string]any{"name": name})
	default:
		http.NotFound(w, r)
	}
}

func woodpeckerFixture(t *testing.T) (*fakeWoodpecker, *fakeKV, WoodpeckerClient) {
	f := &fakeWoodpecker{token: "woodpecker-token-0", agents: map[int64]string{1: "woodpecker-agent-0", 2: "woodpecker-agent-1"}, secrets: map[string]string{"registry_password": "old"}}
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	kv := newKV(&[]string{}, map[string]map[string]string{"woodpecker/admin-token": {"token": "woodpecker-token-0"}})
	return f, kv, WoodpeckerClient{URL: func(context.Context) (string, *http.Client, error) { return server.URL, server.Client(), nil }, Store: kv}
}

// The admin's token is reset once (a rerun reuses it) and kept in OpenBao; the reset ends the old one
func TestWoodpeckerTokenResetsOnce(t *testing.T) {
	ctx := context.Background()
	e := catalogEntry(t, "woodpecker.admin-token")
	f, kv, api := woodpeckerFixture(t)
	m := WoodpeckerToken{API: api, Store: kv}
	for range 2 {
		if err := m.Prepare(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if f.resets != 1 || kv.data["woodpecker/admin-token"]["token"] != "woodpecker-token-1" {
		t.Fatalf("resets %d, stored %v", f.resets, kv.data["woodpecker/admin-token"])
	}
	for _, phase := range []func(context.Context, Entry) error{m.Switch, m.Verify, m.Revoke} {
		if err := phase(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
}

// A new agent secret: the agents stop, the server restarts on the new secret, every agent row (which keeps the old
// secret as its token) and the agents' ids go, then the agents start and register again
func TestWoodpeckerAgentDeletesTheRows(t *testing.T) {
	ctx := context.Background()
	e := catalogEntry(t, "woodpecker.agent")
	f, kv, api := woodpeckerFixture(t)
	kv.data["woodpecker/generated/agent"] = map[string]string{"secret": "old-agent-secret"}
	agents := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "woodpecker", Name: "woodpecker-agent"},
		Spec: appsv1.StatefulSetSpec{Replicas: ptr.To[int32](2), VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
			{ObjectMeta: metav1.ObjectMeta{Name: "agent-config"}},
		}},
	}
	claim := func(name string) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "woodpecker", Name: name}}
	}
	pod := func(name string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "woodpecker", Name: name}}
	}
	kube := fake.NewClientset(agents, claim("agent-config-woodpecker-agent-0"), claim("agent-config-woodpecker-agent-1"), claim("data-woodpecker-server-0"),
		pod("woodpecker-agent-0"), pod("woodpecker-agent-1"), pod("woodpecker-server-0"))
	log := &[]string{}
	n := 0
	m := WoodpeckerAgent{API: api, Kube: kube, Store: kv, Workloads: fakeReaders{log: log}, Waits: fastWaiter{}, Random: fixed(&n)}
	if err := m.Prepare(ctx, e); err != nil {
		t.Fatal(err)
	}
	if kv.data["woodpecker/generated/agent"]["secret"] != newValue {
		t.Fatalf("OpenBao holds %v", kv.data["woodpecker/generated/agent"])
	}
	if err := m.Switch(ctx, e); err != nil {
		t.Fatal(err)
	}
	if len(f.agents) != 0 {
		t.Fatalf("agent rows left: %v", f.agents)
	}
	// The agents start again on new claims (workload.ResetClaims): their ids only, not the server's data
	if !slices.Contains(*log, "reset woodpecker/agent-config-woodpecker-agent-0,agent-config-woodpecker-agent-1") {
		t.Fatalf("readers: %v", *log)
	}
	// Never scaled: Argo CD would put the replicas back at once
	sts, _ := kube.AppsV1().StatefulSets("woodpecker").Get(ctx, "woodpecker-agent", metav1.GetOptions{})
	if *sts.Spec.Replicas != 2 || slices.ContainsFunc(kube.Actions(), func(a k8stesting.Action) bool {
		return a.GetVerb() == "patch" && a.GetResource().Resource == "statefulsets"
	}) {
		t.Fatalf("the agents were scaled: replicas %d", *sts.Spec.Replicas)
	}
	if !slices.Contains(*log, "refresh woodpecker/woodpecker-secret,woodpecker-agent") || !slices.Contains(*log, "restart woodpecker/woodpecker-server") {
		t.Fatalf("readers: %v", *log)
	}
	// Running again after the switch (an interruption) finds nothing more to delete
	if err := m.Switch(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := m.Verify(ctx, e); err == nil {
		t.Fatal("Verify passed before any agent registered again")
	}
	f.agents[3] = "woodpecker-agent-0"
	for _, phase := range []func(context.Context, Entry) error{m.Verify, m.Revoke} {
		if err := phase(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
}

// zot's ci password and Woodpecker's org secret registry_password change together
func TestRegistryCIUpdatesTheOrgSecret(t *testing.T) {
	ctx := context.Background()
	e := catalogEntry(t, "registry.ci")
	f, kv, api := woodpeckerFixture(t)
	kv.data["zot/generated/ci"] = map[string]string{"password": "old"}
	log := &[]string{}
	n := 0
	m := RegistryCI{Generated: Generated{Store: kv, Workloads: fakeReaders{log: log}, Random: fixed(&n)}, API: api}
	for _, phase := range []func(context.Context, Entry) error{m.Prepare, m.Switch, m.Verify, m.Revoke} {
		if err := phase(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if f.secrets["registry_password"] != newValue || kv.data["zot/generated/ci"]["password"] != newValue {
		t.Fatalf("org secret %q, OpenBao %v", f.secrets["registry_password"], kv.data["zot/generated/ci"])
	}
	// Without the org secret (nobody set it yet) there is nothing to update in Woodpecker
	delete(f.secrets, "registry_password")
	if err := m.Switch(ctx, e); err != nil {
		t.Fatal(err)
	}
}

// Without a working admin token (production's needs a person once) a Woodpecker rotation changes nothing: no new
// agent secret, no new zot password, so CI keeps working
func TestWoodpeckerNeedsTheAdminTokenFirst(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"woodpecker.agent", "registry.ci"} {
		e := catalogEntry(t, name)
		f, kv, api := woodpeckerFixture(t)
		kv.data[e.Paths[0]] = map[string]string{"secret": "old", "password": "old"}
		f.token = "the-token-someone-reset"
		n := 0
		var m Method = WoodpeckerAgent{API: api, Kube: fake.NewClientset(), Store: kv, Workloads: fakeReaders{log: &[]string{}}, Waits: fastWaiter{}, Random: fixed(&n)}
		if name == "registry.ci" {
			m = RegistryCI{Generated: Generated{Store: kv, Workloads: fakeReaders{log: &[]string{}}, Random: fixed(&n)}, API: api}
		}
		err := m.Prepare(ctx, e)
		if err == nil || !strings.Contains(err.Error(), "admin token") {
			t.Fatalf("%s: Prepare = %v", name, err)
		}
		if n != 0 || kv.data[e.Paths[0]]["secret"] != "old" || kv.data[e.Paths[0]]["password"] != "old" {
			t.Fatalf("%s changed something: %v", name, kv.data[e.Paths[0]])
		}
	}
}
