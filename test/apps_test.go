package test

import (
	"reflect"
	"testing"
)

func TestSmokeAppsForEnv(t *testing.T) {
	dev := []smokeApp{
		{"argocd-server", "argocd"},
		{"excalidraw", "excalidraw"},
		{"kanidm", "kanidm"},
		{"dex", "dex"},
	}
	if got := smokeApps("dev"); !reflect.DeepEqual(got, dev) {
		t.Errorf("smokeApps(dev) = %v, want %v", got, dev)
	}

	prod := smokeApps("")
	if len(prod) != 6 || prod[0].name != "argocd-server" {
		t.Errorf("smokeApps(\"\") = %v, want the 6 production apps", prod)
	}
	if !reflect.DeepEqual(smokeApps("prod"), prod) {
		t.Errorf("smokeApps(prod) must equal the default list")
	}
}
