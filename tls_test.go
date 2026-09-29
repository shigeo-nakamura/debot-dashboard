package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeSelfSignedPair(t *testing.T, dir, name string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, name+".crt")
	keyPath := filepath.Join(dir, name+".key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func TestLoadServerTLSNeitherIsPlainHTTP(t *testing.T) {
	cfg, err := loadServerTLS("", "")
	if err != nil || cfg != nil {
		t.Fatalf("got cfg=%v err=%v, want nil, nil", cfg, err)
	}
}

func TestLoadServerTLSHalfConfiguredFails(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSignedPair(t, dir, "a")
	for _, tc := range [][2]string{{certPath, ""}, {"", keyPath}} {
		cfg, err := loadServerTLS(tc[0], tc[1])
		if err == nil || cfg != nil {
			t.Fatalf("loadServerTLS(%q, %q) = %v, %v; want an error, never a plain-HTTP fallback", tc[0], tc[1], cfg, err)
		}
		if !strings.Contains(err.Error(), "set together") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

func TestLoadServerTLSLoadsPair(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSignedPair(t, dir, "a")
	cfg, err := loadServerTLS(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil || len(cfg.Certificates) != 1 {
		t.Fatalf("want one loaded certificate, got %+v", cfg)
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %x, want TLS 1.2", cfg.MinVersion)
	}
}

func TestLoadServerTLSFailsAtStartupOnBadFiles(t *testing.T) {
	dir := t.TempDir()
	certA, _ := writeSelfSignedPair(t, dir, "a")
	_, keyB := writeSelfSignedPair(t, dir, "b")
	if _, err := loadServerTLS(certA, keyB); err == nil {
		t.Fatal("mismatched cert/key must fail at startup")
	}
	if _, err := loadServerTLS(filepath.Join(dir, "missing.crt"), keyB); err == nil {
		t.Fatal("missing certificate must fail at startup")
	}
}
