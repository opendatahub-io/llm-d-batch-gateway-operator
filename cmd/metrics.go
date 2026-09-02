package main

import (
	"crypto/tls"
	"fmt"
	"path/filepath"

	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// metricsServerOptions mirrors opendatahub-operator: HTTPS metrics and authn/authz
// are gated on metrics-secure, and CertDir is set only when a cert path is provided
// so xKS is not pointed at an empty optional service-ca volume.
//
// When certDir is set, TLSOpts also installs GetCertificate. controller-runtime
// v0.23.3 otherwise checks tls.crt/tls.key once at listener create and falls back
// to a self-signed cert that ServiceMonitor (service-ca) will never trust. Loading
// on each handshake picks up the service-ca files when they appear on the optional
// volume, instead of locking in that fallback.
func metricsServerOptions(bindAddress string, secure bool, certDir string, tlsOpts []func(*tls.Config)) metricsserver.Options {
	opts := metricsserver.Options{
		BindAddress:   bindAddress,
		SecureServing: secure,
		TLSOpts:       append([]func(*tls.Config){}, tlsOpts...),
	}
	if secure {
		opts.FilterProvider = filters.WithAuthenticationAndAuthorization
	}
	if certDir != "" {
		opts.CertDir = certDir
		opts.TLSOpts = append(opts.TLSOpts, loadMetricsCertificate(certDir))
	}
	return opts
}

func loadMetricsCertificate(certDir string) func(*tls.Config) {
	certPath := filepath.Join(certDir, "tls.crt")
	keyPath := filepath.Join(certDir, "tls.key")
	return func(c *tls.Config) {
		c.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert, err := tls.LoadX509KeyPair(certPath, keyPath)
			if err != nil {
				return nil, fmt.Errorf("loading metrics serving certificate: %w", err)
			}
			return &cert, nil
		}
	}
}
