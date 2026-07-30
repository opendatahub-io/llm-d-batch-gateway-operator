package tls

import (
	"context"
	"crypto/tls"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestProfileValuesFromSpec(t *testing.T) {
	intermediateProfile := configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
	modernProfile := configv1.TLSProfiles[configv1.TLSProfileModernType]

	tests := []struct {
		name             string
		spec             configv1.TLSProfileSpec
		wantMinVersion   string
		wantCipherSuites string
		wantNextProtos   string
	}{
		{
			name:             "empty spec returns defaults",
			spec:             configv1.TLSProfileSpec{},
			wantMinVersion:   "",
			wantCipherSuites: "",
			wantNextProtos:   defaultNextProtos,
		},
		{
			name:             "Intermediate profile",
			spec:             *intermediateProfile,
			wantMinVersion:   string(intermediateProfile.MinTLSVersion),
			wantCipherSuites: strings.Join(intermediateProfile.Ciphers, ","),
			wantNextProtos:   defaultNextProtos,
		},
		{
			name:             "Modern profile",
			spec:             *modernProfile,
			wantMinVersion:   string(modernProfile.MinTLSVersion),
			wantCipherSuites: strings.Join(modernProfile.Ciphers, ","),
			wantNextProtos:   defaultNextProtos,
		},
		{
			name: "custom ciphers",
			spec: configv1.TLSProfileSpec{
				MinTLSVersion: "VersionTLS12",
				Ciphers:       []string{"ECDHE-ECDSA-AES128-GCM-SHA256", "ECDHE-RSA-AES256-GCM-SHA384"},
			},
			wantMinVersion:   "VersionTLS12",
			wantCipherSuites: "ECDHE-ECDSA-AES128-GCM-SHA256,ECDHE-RSA-AES256-GCM-SHA384",
			wantNextProtos:   defaultNextProtos,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ProfileValuesFromSpec(tt.spec)
			if got.MinVersion != tt.wantMinVersion {
				t.Errorf("MinVersion = %q, want %q", got.MinVersion, tt.wantMinVersion)
			}
			if got.CipherSuites != tt.wantCipherSuites {
				t.Errorf("CipherSuites = %q, want %q", got.CipherSuites, tt.wantCipherSuites)
			}
			if got.NextProtos != tt.wantNextProtos {
				t.Errorf("NextProtos = %q, want %q", got.NextProtos, tt.wantNextProtos)
			}
		})
	}
}

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = configv1.Install(s)
	return s
}

func TestResolve(t *testing.T) {
	logger := klog.NewKlogr()
	intermediateProfile := configv1.TLSProfiles[configv1.TLSProfileIntermediateType]

	// IsNoMatchError (non-OpenShift) requires a real REST mapper and is covered by integration tests.
	tests := []struct {
		name               string
		objects            []runtime.Object
		wantErr            bool
		wantProfileFetched bool
		wantMinVersion     configv1.TLSProtocolVersion
	}{
		{
			name:               "APIServer resource not found",
			wantProfileFetched: false,
			wantMinVersion:     intermediateProfile.MinTLSVersion,
		},
		{
			name: "APIServer exists with custom profile",
			objects: []runtime.Object{
				&configv1.APIServer{
					ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
					Spec: configv1.APIServerSpec{
						TLSSecurityProfile: &configv1.TLSSecurityProfile{
							Type: configv1.TLSProfileModernType,
						},
					},
				},
			},
			wantProfileFetched: true,
			wantMinVersion:     configv1.TLSProfiles[configv1.TLSProfileModernType].MinTLSVersion,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := fake.NewClientBuilder().WithScheme(testScheme())
			for _, obj := range tt.objects {
				builder = builder.WithRuntimeObjects(obj)
			}
			c := builder.Build()

			result, err := Resolve(context.Background(), c, logger)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Resolve() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if result.ProfileFetched != tt.wantProfileFetched {
				t.Errorf("ProfileFetched = %v, want %v", result.ProfileFetched, tt.wantProfileFetched)
			}
			if result.Profile.MinTLSVersion != tt.wantMinVersion {
				t.Errorf("MinTLSVersion = %v, want %v", result.Profile.MinTLSVersion, tt.wantMinVersion)
			}
			if len(result.TLSOpts) == 0 {
				t.Error("TLSOpts should not be empty")
			}
			cfg := &tls.Config{}
			for _, fn := range result.TLSOpts {
				fn(cfg)
			}
			if len(cfg.NextProtos) == 0 {
				t.Error("NextProtos should be set")
			}
		})
	}
}

func TestResolve_TLS13NoCiphers(t *testing.T) {
	logger := klog.NewKlogr()
	s := testScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(
		&configv1.APIServer{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec: configv1.APIServerSpec{
				TLSSecurityProfile: &configv1.TLSSecurityProfile{
					Type: configv1.TLSProfileModernType,
				},
			},
		},
	).Build()

	result, err := Resolve(context.Background(), c, logger)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	vals := ProfileValuesFromSpec(result.Profile)
	if vals.MinVersion != "VersionTLS13" {
		t.Errorf("MinVersion = %q, want VersionTLS13", vals.MinVersion)
	}
	if vals.NextProtos != defaultNextProtos {
		t.Errorf("NextProtos = %q, want h2,http/1.1", vals.NextProtos)
	}
}
