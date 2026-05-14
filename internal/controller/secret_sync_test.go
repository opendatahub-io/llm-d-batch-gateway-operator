package controller

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	batchv1alpha1 "github.com/opendatahub-io/llm-d-batch-gateway-operator/api/v1alpha1"
)

const testSecretValue = "value"

// buildScheme returns a Scheme with all types needed by secret_sync.go.
func buildScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := scheme.AddToScheme(s); err != nil {
		t.Fatalf("adding k8s scheme: %v", err)
	}
	if err := batchv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("adding batchv1alpha1 scheme: %v", err)
	}
	if err := gatewayv1beta1.Install(s); err != nil {
		t.Fatalf("adding gateway-api scheme: %v", err)
	}
	return s
}

// makeGrant builds a ReferenceGrant in "creds-ns" that allows
// LLMBatchGateways in fromNamespace to reference a Secret.
// Pass secretName="" to get a wildcard To entry.
func makeGrant(name, fromNamespace, secretName string) *gatewayv1beta1.ReferenceGrant {
	to := gatewayv1beta1.ReferenceGrantTo{
		Group: corev1.GroupName,
		Kind:  "Secret",
	}
	if secretName != "" {
		n := gatewayv1beta1.ObjectName(secretName)
		to.Name = &n
	}
	return &gatewayv1beta1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "creds-ns",
		},
		Spec: gatewayv1beta1.ReferenceGrantSpec{
			From: []gatewayv1beta1.ReferenceGrantFrom{{
				Group:     gatewayv1beta1.Group(batchv1alpha1.GroupVersion.Group),
				Kind:      "LLMBatchGateway",
				Namespace: gatewayv1beta1.Namespace(fromNamespace),
			}},
			To: []gatewayv1beta1.ReferenceGrantTo{to},
		},
	}
}

// ---- referenceGrantPermits (pure logic, no API) ----

func TestReferenceGrantPermits(t *testing.T) {
	cases := []struct {
		name          string
		grant         *gatewayv1beta1.ReferenceGrant
		fromNamespace string
		secretName    string
		want          bool
	}{
		{
			name:          "exact match",
			grant:         makeGrant("g", "default", "my-secret"),
			fromNamespace: "default",
			secretName:    "my-secret",
			want:          true,
		},
		{
			name:          "wildcard to (no name filter)",
			grant:         makeGrant("g", "default", ""),
			fromNamespace: "default",
			secretName:    "any-secret",
			want:          true,
		},
		{
			name:          "wrong secret name",
			grant:         makeGrant("g", "default", "other-secret"),
			fromNamespace: "default",
			secretName:    "my-secret",
			want:          false,
		},
		{
			name:          "wrong from namespace",
			grant:         makeGrant("g", "other-ns", "my-secret"),
			fromNamespace: "default",
			secretName:    "my-secret",
			want:          false,
		},
		{
			name: "wrong from group",
			grant: &gatewayv1beta1.ReferenceGrant{
				ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: "creds-ns"},
				Spec: gatewayv1beta1.ReferenceGrantSpec{
					From: []gatewayv1beta1.ReferenceGrantFrom{{
						Group:     "wrong.group.io",
						Kind:      "LLMBatchGateway",
						Namespace: "default",
					}},
					To: []gatewayv1beta1.ReferenceGrantTo{{Group: "", Kind: "Secret"}},
				},
			},
			fromNamespace: "default",
			secretName:    "my-secret",
			want:          false,
		},
		{
			name: "wrong to kind",
			grant: &gatewayv1beta1.ReferenceGrant{
				ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: "creds-ns"},
				Spec: gatewayv1beta1.ReferenceGrantSpec{
					From: []gatewayv1beta1.ReferenceGrantFrom{{
						Group:     gatewayv1beta1.Group(batchv1alpha1.GroupVersion.Group),
						Kind:      "LLMBatchGateway",
						Namespace: "default",
					}},
					To: []gatewayv1beta1.ReferenceGrantTo{{Group: "", Kind: "ConfigMap"}},
				},
			},
			fromNamespace: "default",
			secretName:    "my-secret",
			want:          false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := referenceGrantPermits(tc.grant, tc.fromNamespace, tc.secretName)
			if got != tc.want {
				t.Errorf("referenceGrantPermits() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---- resolveSecret ----

func TestResolveSecret(t *testing.T) {
	const (
		gwName      = "my-gw"
		gwNamespace = "default"
		credsNS     = "creds-ns"
		secretName  = "src-secret"
	)

	ctx := context.Background()

	srcSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: credsNS},
		Data:       map[string][]byte{"key": []byte(testSecretValue)},
	}

	gwBase := func(gwName, secretNS, secretNameOverride string) *batchv1alpha1.LLMBatchGateway {
		gw := newTestGateway(gwName)
		gw.UID = types.UID(gwName + "-uid")
		sName := secretName
		if secretNameOverride != "" {
			sName = secretNameOverride
		}
		gw.Spec.SecretRef = corev1.SecretReference{Name: sName, Namespace: secretNS}
		return gw
	}

	// Same-namespace and error cases use the fake client (no SSA needed).

	t.Run("same namespace returns secret name directly", func(t *testing.T) {
		s := buildScheme(t)
		localSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: gwNamespace},
		}
		c := fake.NewClientBuilder().WithScheme(s).WithObjects(localSecret).Build()
		r := &LLMBatchGatewayReconciler{Client: c, Scheme: s}

		gw := gwBase(gwName, gwNamespace, "")
		got, err := r.resolveSecret(ctx, gw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != secretName {
			t.Errorf("got %q, want %q", got, secretName)
		}
	})

	t.Run("empty namespace treated as same namespace", func(t *testing.T) {
		s := buildScheme(t)
		localSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: gwNamespace},
		}
		c := fake.NewClientBuilder().WithScheme(s).WithObjects(localSecret).Build()
		r := &LLMBatchGatewayReconciler{Client: c, Scheme: s}

		gw := gwBase(gwName, "", "")
		got, err := r.resolveSecret(ctx, gw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != secretName {
			t.Errorf("got %q, want %q", got, secretName)
		}
	})

	t.Run("cross-namespace without ReferenceGrant returns ReferenceNotPermittedError", func(t *testing.T) {
		s := buildScheme(t)
		c := fake.NewClientBuilder().WithScheme(s).WithObjects(srcSecret).Build()
		r := &LLMBatchGatewayReconciler{Client: c, Scheme: s}

		gw := gwBase(gwName, credsNS, "")
		_, err := r.resolveSecret(ctx, gw)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		var refErr *ReferenceNotPermittedError
		if !errors.As(err, &refErr) {
			t.Errorf("expected ReferenceNotPermittedError, got %T: %v", err, err)
		}
	})

	t.Run("cross-namespace with grant for wrong secret returns error", func(t *testing.T) {
		s := buildScheme(t)
		grant := makeGrant("test-grant", gwNamespace, "different-secret")
		c := fake.NewClientBuilder().WithScheme(s).WithObjects(srcSecret, grant).Build()
		r := &LLMBatchGatewayReconciler{Client: c, Scheme: s}

		gw := gwBase(gwName, credsNS, "")
		_, err := r.resolveSecret(ctx, gw)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		var refErr *ReferenceNotPermittedError
		if !errors.As(err, &refErr) {
			t.Errorf("expected ReferenceNotPermittedError, got %T: %v", err, err)
		}
	})

	// Secret-copy cases use k8sClient (envtest) because syncSecretCopy uses SSA.

	// Ensure creds-ns exists once for all envtest subtests. The namespace is
	// intentionally not cleaned up here: envtest tears down the entire API
	// server at the end of TestMain, so there is nothing to leak. Each subtest
	// cleans up only the objects it creates (secrets, grants) via t.Cleanup.
	if err := k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: credsNS}}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("creating namespace %s: %v", credsNS, err)
	}

	t.Run("cross-namespace with exact ReferenceGrant copies secret", func(t *testing.T) {
		src := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "src-secret-exact", Namespace: credsNS},
			Data:       map[string][]byte{"key": []byte(testSecretValue)},
		}
		grant := makeGrant("grant-exact", gwNamespace, src.Name)

		if err := k8sClient.Create(ctx, src); err != nil {
			t.Fatalf("creating src secret: %v", err)
		}
		if err := k8sClient.Create(ctx, grant); err != nil {
			t.Fatalf("creating grant: %v", err)
		}
		t.Cleanup(func() {
			_ = k8sClient.Delete(ctx, src)
			_ = k8sClient.Delete(ctx, grant)
		})

		r := &LLMBatchGatewayReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		gw := gwBase("gw-exact", credsNS, src.Name)

		got, err := r.resolveSecret(ctx, gw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		wantName := "gw-exact" + managedSecretSuffix
		if got != wantName {
			t.Errorf("got %q, want %q", got, wantName)
		}

		// Verify copy exists with correct data, annotation, and owner reference.
		var copied corev1.Secret
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: wantName, Namespace: gwNamespace}, &copied); err != nil {
			t.Fatalf("getting secret copy: %v", err)
		}
		if string(copied.Data["key"]) != testSecretValue {
			t.Errorf("copy data[key] = %q, want %q", copied.Data["key"], testSecretValue)
		}
		wantAnnotation := credsNS + "/" + src.Name
		if copied.Annotations["batch.llm-d.ai/copied-from"] != wantAnnotation {
			t.Errorf("annotation = %q, want %q", copied.Annotations["batch.llm-d.ai/copied-from"], wantAnnotation)
		}
		hasOwner := false
		for _, ref := range copied.OwnerReferences {
			if ref.Name == gw.Name && ref.Controller != nil && *ref.Controller {
				hasOwner = true
				break
			}
		}
		if !hasOwner {
			t.Errorf("copied secret missing controller ownerReference pointing to %q", gw.Name)
		}
	})

	t.Run("cross-namespace with wildcard ReferenceGrant copies secret", func(t *testing.T) {
		src := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "src-secret-wildcard", Namespace: credsNS},
			Data:       map[string][]byte{"key": []byte(testSecretValue)},
		}
		grant := makeGrant("grant-wildcard", gwNamespace, "") // wildcard

		if err := k8sClient.Create(ctx, src); err != nil {
			t.Fatalf("creating src secret: %v", err)
		}
		if err := k8sClient.Create(ctx, grant); err != nil {
			t.Fatalf("creating grant: %v", err)
		}
		t.Cleanup(func() {
			_ = k8sClient.Delete(ctx, src)
			_ = k8sClient.Delete(ctx, grant)
		})

		r := &LLMBatchGatewayReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		gw := gwBase("gw-wildcard", credsNS, src.Name)

		got, err := r.resolveSecret(ctx, gw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		wantName := "gw-wildcard" + managedSecretSuffix
		if got != wantName {
			t.Errorf("got %q, want %q", got, wantName)
		}

		// Verify copy exists with correct data, annotation, and owner reference.
		var copied corev1.Secret
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: wantName, Namespace: gwNamespace}, &copied); err != nil {
			t.Fatalf("getting secret copy: %v", err)
		}
		if string(copied.Data["key"]) != testSecretValue {
			t.Errorf("copy data[key] = %q, want %q", copied.Data["key"], testSecretValue)
		}
		wantAnnotation := credsNS + "/" + src.Name
		if copied.Annotations["batch.llm-d.ai/copied-from"] != wantAnnotation {
			t.Errorf("annotation = %q, want %q", copied.Annotations["batch.llm-d.ai/copied-from"], wantAnnotation)
		}
		hasOwner := false
		for _, ref := range copied.OwnerReferences {
			if ref.Name == gw.Name && ref.Controller != nil && *ref.Controller {
				hasOwner = true
				break
			}
		}
		if !hasOwner {
			t.Errorf("copied secret missing controller ownerReference pointing to %q", gw.Name)
		}
	})

	t.Run("mutating secretRef after copy exists returns SecretRefImmutableError", func(t *testing.T) {
		// Create two source secrets in credsNS.
		src1 := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "src-immutable-1", Namespace: credsNS},
			Data:       map[string][]byte{"key": []byte("v1")},
		}
		src2 := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "src-immutable-2", Namespace: credsNS},
			Data:       map[string][]byte{"key": []byte("v2")},
		}
		grant := makeGrant("grant-immutable", gwNamespace, "") // wildcard covers both

		for _, obj := range []client.Object{src1, src2, grant} {
			if err := k8sClient.Create(ctx, obj); err != nil {
				t.Fatalf("creating %s: %v", obj.GetName(), err)
			}
		}
		t.Cleanup(func() {
			for _, obj := range []client.Object{src1, src2, grant} {
				_ = k8sClient.Delete(ctx, obj)
			}
		})

		r := &LLMBatchGatewayReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

		// First reconcile — creates managed copy pointing at src1.
		gw := gwBase("gw-immutable", credsNS, src1.Name)
		if _, err := r.resolveSecret(ctx, gw); err != nil {
			t.Fatalf("first resolveSecret: %v", err)
		}

		// Second reconcile — same gateway CR but secretRef now points at src2.
		gw2 := gwBase("gw-immutable", credsNS, src2.Name)
		_, err := r.resolveSecret(ctx, gw2)
		if err == nil {
			t.Fatal("expected SecretRefImmutableError, got nil")
		}
		var immErr *SecretRefImmutableError
		if !errors.As(err, &immErr) {
			t.Errorf("expected SecretRefImmutableError, got %T: %v", err, err)
		}
	})
}
