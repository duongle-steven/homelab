package forgejo

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"
)

func testCA(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"}, NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestSandboxEndpoint(t *testing.T) {
	ca := testCA(t)
	ep, cleanup, err := SandboxEndpoint("127-0-0-1.nip.io", "172.28.0.2", ca)
	if err != nil {
		t.Fatal(err)
	}
	if ep.URL != "https://git.127-0-0-1.nip.io" {
		t.Errorf("URL = %s", ep.URL)
	}
	var caFile string
	for _, c := range ep.Git {
		if strings.HasPrefix(c, "http.sslCAInfo=") {
			caFile = strings.TrimPrefix(c, "http.sslCAInfo=")
		}
	}
	if got, err := os.ReadFile(caFile); err != nil || string(got) != string(ca) {
		t.Fatalf("git's CA file %q: %v", caFile, err)
	}
	if !strings.Contains(strings.Join(ep.Git, " "), "http.curloptResolve=git.127-0-0-1.nip.io:443:172.28.0.2") {
		t.Errorf("git does not resolve Forgejo to the node: %v", ep.Git)
	}
	cleanup()
	if _, err := os.Stat(caFile); !os.IsNotExist(err) {
		t.Errorf("the CA file outlived the cleanup: %v", err)
	}
	if _, _, err := SandboxEndpoint("127-0-0-1.nip.io", "172.28.0.2", []byte("not a certificate")); err == nil {
		t.Error("a non-PEM CA was accepted")
	}
}
