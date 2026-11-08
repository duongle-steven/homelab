package offline

import (
	"reflect"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/smoke"
)

func TestSmokeAppsForEnv(t *testing.T) {
	dev := []smoke.App{
		{Name: "argocd-server", Namespace: "argocd"},
		{Name: "excalidraw", Namespace: "excalidraw"},
		{Name: "kanidm", Namespace: "kanidm"},
		{Name: "paperless", Namespace: "paperless"},
		{Name: "grafana", Namespace: "grafana"},
		{Name: "zot", Namespace: "zot"},
		{Name: "actualbudget", Namespace: "actualbudget"},
		{Name: "homepage", Namespace: "homepage"},
		{Name: "pairdrop", Namespace: "pairdrop"},
		{Name: "speedtest", Namespace: "speedtest"},
		{Name: "forgejo", Namespace: "forgejo"},
		{Name: "opencloud", Namespace: "opencloud"},
		{Name: "ntfy", Namespace: "ntfy"},
	}
	if got := smoke.Apps("dev"); !reflect.DeepEqual(got, dev) {
		t.Errorf("smoke.Apps(dev) = %v, want %v", got, dev)
	}

	prod := smoke.Apps("")
	if len(prod) != 6 || prod[0].Name != "argocd-server" {
		t.Errorf("smoke.Apps(\"\") = %v, want the 6 production apps", prod)
	}
	if !reflect.DeepEqual(smoke.Apps("prod"), prod) {
		t.Errorf("smoke.Apps(prod) must equal the default list")
	}
}
