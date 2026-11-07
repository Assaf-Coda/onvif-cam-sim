// Package tlscert loads the TLS certificate for the HTTPS listeners, generating a
// self-signed one next to the config file when none exists.
package tlscert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"time"
)

// Load returns the certificate at certPath/keyPath. If neither file exists a
// self-signed ECDSA P-256 certificate valid for ten years is generated with the
// given IPs (plus 127.0.0.1 and localhost) as subject alternative names and
// written to those paths. generated reports whether that happened.
func Load(certPath, keyPath string, ips ...string) (cert tls.Certificate, generated bool, err error) {
	_, errC := os.Stat(certPath)
	_, errK := os.Stat(keyPath)
	if errors.Is(errC, os.ErrNotExist) && errors.Is(errK, os.ErrNotExist) {
		if err := generate(certPath, keyPath, ips); err != nil {
			return tls.Certificate{}, false, fmt.Errorf("generate self-signed certificate: %w", err)
		}
		generated = true
	}
	cert, err = tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, generated, fmt.Errorf("load %s / %s: %w", certPath, keyPath, err)
	}
	return cert, generated, nil
}

func generate(certPath, keyPath string, ips []string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "onvif-cam-sim", Organization: []string{"onvif-cam-sim"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true, // lets a VMS import cert.pem as a trust anchor directly
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	if host != "" {
		tmpl.DNSNames = append(tmpl.DNSNames, host)
	}
	for _, s := range ips {
		if ip := net.ParseIP(s); ip != nil && !ip.IsLoopback() && !ip.IsUnspecified() {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}
