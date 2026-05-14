package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	batchv1alpha1 "github.com/opendatahub-io/llm-d-batch-gateway-operator/api/v1alpha1"
)

const (
	// managedSecretSuffix is appended to the LLMBatchGateway name to form the
	// name of the controller-managed Secret copy in the CR's namespace.
	managedSecretSuffix = "-credentials"
)

// resolveSecret ensures the credentials Secret is available in gw.Namespace
// and returns the name that should be passed to Helm as global.secretName.
//
// Same-namespace case (secretRef.Namespace == "" or == gw.Namespace):
//
//	Returns secretRef.Name directly — no copy needed.
//
// Cross-namespace case (secretRef.Namespace != gw.Namespace):
//
//  1. Verifies a ReferenceGrant exists in secretRef.Namespace that permits
//     this LLMBatchGateway (in gw.Namespace) to reference the named Secret.
//  2. Reads the source Secret.
//  3. Creates or updates a managed copy in gw.Namespace owned by the CR.
//  4. Returns the name of the managed copy.
//
// Returns (localSecretName, nil) on success.
// Returns ("", ErrReferenceNotPermitted) when no valid ReferenceGrant exists.
func (r *LLMBatchGatewayReconciler) resolveSecret(
	ctx context.Context,
	gw *batchv1alpha1.LLMBatchGateway,
) (string, error) {
	ref := gw.Spec.SecretRef
	secretNS := ref.Namespace
	if secretNS == "" {
		secretNS = gw.Namespace
	}

	// Same namespace — no grant check or copy needed.
	if secretNS == gw.Namespace {
		return ref.Name, nil
	}

	// Detect mutation of secretRef by inspecting the existing managed copy.
	// secretRef is immutable once a managed copy exists.
	localName := gw.Name + managedSecretSuffix
	var existing corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: localName, Namespace: gw.Namespace}, &existing); err == nil {
		wantAnnotation := fmt.Sprintf("%s/%s", secretNS, ref.Name)
		if got := existing.Annotations["batch.llm-d.ai/copied-from"]; got != wantAnnotation {
			return "", &SecretRefImmutableError{
				OldRef: got,
				NewRef: wantAnnotation,
			}
		}
	}

	// Cross-namespace: check for a permitting ReferenceGrant.
	if err := r.checkReferenceGrant(ctx, gw, ref.Name, secretNS); err != nil {
		return "", err
	}

	// Read the source Secret.
	var src corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: secretNS}, &src); err != nil {
		return "", fmt.Errorf("reading source secret %s/%s: %w", secretNS, ref.Name, err)
	}

	// Create or update the managed copy in gw.Namespace.
	if err := r.syncSecretCopy(ctx, gw, &src, localName); err != nil {
		return "", err
	}

	return localName, nil
}

// checkReferenceGrant verifies that a ReferenceGrant in secretNamespace permits
// the given LLMBatchGateway (namespace: gw.Namespace) to reference a Secret
// named secretName in secretNamespace.
//
// The grant must:
//   - Live in secretNamespace.
//   - Have a From entry with Group "batch.llm-d.ai", Kind "LLMBatchGateway",
//     Namespace == gw.Namespace.
//   - Have a To entry with Group "" (core), Kind "Secret", and either no Name
//     (wildcard) or Name == secretName.
func (r *LLMBatchGatewayReconciler) checkReferenceGrant(
	ctx context.Context,
	gw *batchv1alpha1.LLMBatchGateway,
	secretName, secretNamespace string,
) error {
	var grantList gatewayv1beta1.ReferenceGrantList
	if err := r.List(ctx, &grantList, client.InNamespace(secretNamespace)); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			// ReferenceGrant CRD not installed — treat as no grant.
			return newReferenceNotPermittedError(gw.Name, secretName, secretNamespace,
				"ReferenceGrant CRD is not installed in the cluster")
		}
		return fmt.Errorf("listing ReferenceGrants in %s: %w", secretNamespace, err)
	}

	for _, grant := range grantList.Items {
		if referenceGrantPermits(&grant, gw.Namespace, secretName) {
			return nil
		}
	}

	return newReferenceNotPermittedError(gw.Name, secretName, secretNamespace,
		fmt.Sprintf("no ReferenceGrant in namespace %q permits LLMBatchGateway %q (namespace %q) to reference Secret %q",
			secretNamespace, gw.Name, gw.Namespace, secretName))
}

// referenceGrantPermits returns true when the grant allows an LLMBatchGateway
// in fromNamespace to reference a Secret named secretName.
func referenceGrantPermits(grant *gatewayv1beta1.ReferenceGrant, fromNamespace, secretName string) bool {
	hasMatchingFrom := false
	for _, from := range grant.Spec.From {
		if string(from.Group) == batchv1alpha1.GroupVersion.Group &&
			string(from.Kind) == "LLMBatchGateway" &&
			string(from.Namespace) == fromNamespace {
			hasMatchingFrom = true
			break
		}
	}
	if !hasMatchingFrom {
		return false
	}

	for _, to := range grant.Spec.To {
		// Only core-API Secrets are supported; corev1.GroupName is the empty string "".
		if string(to.Group) != corev1.GroupName {
			continue
		}
		if string(to.Kind) != "Secret" {
			continue
		}
		// Name == nil means wildcard (all Secrets in the namespace).
		if to.Name == nil || string(*to.Name) == secretName {
			return true
		}
	}
	return false
}

// syncSecretCopy creates or updates a Secret named localName in gw.Namespace
// whose Data is a copy of src.Data. The copy is owned by the LLMBatchGateway
// so it is garbage-collected when the CR is deleted.
func (r *LLMBatchGatewayReconciler) syncSecretCopy(
	ctx context.Context,
	gw *batchv1alpha1.LLMBatchGateway,
	src *corev1.Secret,
	localName string,
) error {
	desired := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      localName,
			Namespace: gw.Namespace,
			Annotations: map[string]string{
				"batch.llm-d.ai/copied-from": fmt.Sprintf("%s/%s", src.Namespace, src.Name),
			},
		},
		Type: src.Type,
		Data: src.Data,
	}

	// Set the LLMBatchGateway as the owner so the Secret copy is automatically
	// garbage-collected when the CR is deleted.
	if err := controllerutil.SetControllerReference(gw, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on secret copy %s/%s: %w", gw.Namespace, localName, err)
	}

	if err := r.Patch(ctx, desired, client.Apply,
		client.FieldOwner(fieldOwner),
		client.ForceOwnership,
	); err != nil {
		return fmt.Errorf("syncing secret copy %s/%s: %w", gw.Namespace, localName, err)
	}
	return nil
}

// SecretRefImmutableError is returned when spec.secretRef is changed after a
// managed Secret copy already exists. The controller refuses to reconcile
// until the CR is deleted and recreated with the new secretRef.
type SecretRefImmutableError struct {
	OldRef string
	NewRef string
}

func (e *SecretRefImmutableError) Error() string {
	return fmt.Sprintf("spec.secretRef is immutable: managed copy was created from %q, cannot change to %q; delete and recreate the LLMBatchGateway to use a different Secret", e.OldRef, e.NewRef)
}

// ReferenceNotPermittedError is returned when no valid ReferenceGrant covers
// the requested cross-namespace Secret reference.
type ReferenceNotPermittedError struct {
	GatewayName     string
	SecretName      string
	SecretNamespace string
	Reason          string
}

func (e *ReferenceNotPermittedError) Error() string {
	return "reference not permitted: " + e.Reason
}

func newReferenceNotPermittedError(gatewayName, secretName, secretNamespace, reason string) *ReferenceNotPermittedError {
	return &ReferenceNotPermittedError{
		GatewayName:     gatewayName,
		SecretName:      secretName,
		SecretNamespace: secretNamespace,
		Reason:          reason,
	}
}
