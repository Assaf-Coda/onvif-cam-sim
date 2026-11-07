package tlscert

import (
	"crypto/x509"
	"path/filepath"
	"testing"
)

func TestGenerateAndReload(t *testing.T) {
	dir := t.TempDir()
	c, k := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	cert, gen, err := Load(c, k, "192.168.1.121")
	if err != nil || !gen {
		t.Fatalf("first load: gen=%v err=%v", gen, err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := leaf.VerifyHostname("192.168.1.121"); err != nil {
		t.Fatalf("SAN missing advertised IP: %v", err)
	}
	if _, gen, err := Load(c, k, "10.0.0.1"); err != nil || gen {
		t.Fatalf("second load should reuse files: gen=%v err=%v", gen, err)
	}
}
