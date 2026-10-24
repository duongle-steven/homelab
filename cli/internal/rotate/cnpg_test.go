package rotate

import (
	"context"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// CloudNativePG applies the app Secret's password to the role, but leaves the derived keys (uri, pgpass...) alone:
// the rotation writes them too, and Forgejo restarts on them
func TestCNPGPasswordWritesDerivedKeys(t *testing.T) {
	ctx := context.Background()
	e := catalogEntry(t, "forgejo-db")
	const old = "0ldPassw0rd0ldPassw0rd0ldPassw0r"
	kube := fake.NewClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "forgejo", Name: "forgejo-db-app"}, Data: map[string][]byte{
		"username": []byte("forgejo"),
		"password": []byte(old),
		"uri":      []byte("postgresql://forgejo:" + old + "@forgejo-db-rw.forgejo:5432/forgejo"),
		"pgpass":   []byte("forgejo-db-rw:5432:forgejo:forgejo:" + old + "\n"),
		"jdbc-uri": []byte("jdbc:postgresql://forgejo-db-rw.forgejo:5432/forgejo?password=" + old + "&user=forgejo"),
	}})
	log := &[]string{}
	n := 0
	m := CNPGPassword{Kube: kube, Store: newKV(&[]string{}, nil), Workloads: fakeReaders{log: log}, Random: fixed(&n)}
	for range 2 {
		if err := m.Prepare(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := kube.CoreV1().Secrets("forgejo").Get(ctx, "forgejo-db-app", metav1.GetOptions{})
	if n != 1 || string(s.Data["password"]) != newValue || string(s.Data["username"]) != "forgejo" {
		t.Fatalf("values made %d, Secret %v", n, s.Data)
	}
	for key, v := range s.Data {
		if strings.Contains(string(v), old) || (key != "username" && !strings.Contains(string(v), newValue)) {
			t.Errorf("%s = %q", key, v)
		}
	}
	for _, phase := range []func(context.Context, Entry) error{m.Switch, m.Verify, m.Revoke} {
		if err := phase(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(*log, []string{"restart forgejo/forgejo", "wait forgejo/forgejo"}) {
		t.Fatalf("readers: %v", *log)
	}
}
