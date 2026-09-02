package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMetricsServerOptions(t *testing.T) {
	tlsOpts := []func(*tls.Config){func(c *tls.Config) { c.MinVersion = tls.VersionTLS12 }}

	t.Run("HTTP skips auth filter and cert dir", func(t *testing.T) {
		opts := metricsServerOptions(":8080", false, "", tlsOpts)
		if opts.BindAddress != ":8080" {
			t.Errorf("BindAddress = %q, want :8080", opts.BindAddress)
		}
		if opts.SecureServing {
			t.Error("SecureServing = true, want false")
		}
		if opts.FilterProvider != nil {
			t.Error("FilterProvider should be nil when metrics-secure is false")
		}
		if opts.CertDir != "" {
			t.Errorf("CertDir = %q, want empty", opts.CertDir)
		}
		if len(opts.TLSOpts) != 1 {
			t.Errorf("TLSOpts len = %d, want 1", len(opts.TLSOpts))
		}
	})

	t.Run("HTTPS enables auth filter without requiring a cert dir", func(t *testing.T) {
		opts := metricsServerOptions(":8443", true, "", tlsOpts)
		if !opts.SecureServing {
			t.Error("SecureServing = false, want true")
		}
		if opts.FilterProvider == nil {
			t.Error("FilterProvider should be set when metrics-secure is true")
		}
		if opts.CertDir != "" {
			t.Errorf("CertDir = %q, want empty when cert path is unset", opts.CertDir)
		}
		if len(opts.TLSOpts) != 1 {
			t.Errorf("TLSOpts len = %d, want 1", len(opts.TLSOpts))
		}
	})

	t.Run("HTTPS with cert path sets CertDir and loads certs on handshake", func(t *testing.T) {
		certDir := t.TempDir()
		opts := metricsServerOptions(":8443", true, certDir, tlsOpts)
		if !opts.SecureServing {
			t.Error("SecureServing = false, want true")
		}
		if opts.FilterProvider == nil {
			t.Error("FilterProvider should be set when metrics-secure is true")
		}
		if opts.CertDir != certDir {
			t.Errorf("CertDir = %q, want %q", opts.CertDir, certDir)
		}
		if len(opts.TLSOpts) != 2 {
			t.Errorf("TLSOpts len = %d, want 2", len(opts.TLSOpts))
		}

		cfg := &tls.Config{}
		for _, opt := range opts.TLSOpts {
			opt(cfg)
		}
		if cfg.GetCertificate == nil {
			t.Fatal("GetCertificate should be set when cert path is provided")
		}

		if _, err := cfg.GetCertificate(&tls.ClientHelloInfo{}); err == nil {
			t.Fatal("GetCertificate() succeeded with empty cert dir, want error")
		}

		writeTestServingCerts(t, certDir)
		cert, err := cfg.GetCertificate(&tls.ClientHelloInfo{})
		if err != nil {
			t.Fatalf("GetCertificate() after writing certs: %v", err)
		}
		if cert == nil || len(cert.Certificate) == 0 {
			t.Fatal("GetCertificate() returned empty certificate")
		}
	})
}

func writeTestServingCerts(t *testing.T, dir string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"localhost"},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating certificate: %v", err)
	}
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshalling key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tls.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("writing tls.crt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tls.key"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}), 0o600); err != nil {
		t.Fatalf("writing tls.key: %v", err)
	}
}
