package controller

import (
	"context"
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	batchv1alpha1 "github.com/opendatahub-io/llm-d-batch-gateway-operator/api/v1alpha1"
)

const (
	ConditionReady              = "Ready"
	ConditionAPIServerAvailable = "APIServerAvailable"
	ConditionProcessorAvailable = "ProcessorAvailable"
	ConditionGCAvailable        = "GCAvailable"

	fieldOwner = "llmbatchgateway-controller"
)

// managedGVKs must be a superset of all GVK types the Helm chart can produce.
// Update this list when adding new resource types to the chart.
var managedGVKs = []schema.GroupVersionKind{
	{Group: "apps", Version: "v1", Kind: "Deployment"},
	{Group: "", Version: "v1", Kind: "Service"},
	{Group: "", Version: "v1", Kind: "ConfigMap"},
	{Group: "", Version: "v1", Kind: "ServiceAccount"},
	{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"},
	{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"},
	{Group: "monitoring.coreos.com", Version: "v1", Kind: "ServiceMonitor"},
	{Group: "monitoring.coreos.com", Version: "v1", Kind: "PodMonitor"},
	{Group: "monitoring.coreos.com", Version: "v1", Kind: "PrometheusRule"},
}

type resourceKey struct {
	Group string
	Kind  string
	Name  string
}

// +kubebuilder:rbac:groups=batch.llm-d.ai,resources=llmbatchgateways,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=batch.llm-d.ai,resources=llmbatchgateways/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=batch.llm-d.ai,resources=llmbatchgateways/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;configmaps;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// +kubebuilder:rbac:groups=cert-manager.io,resources=certificates,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors;podmonitors;prometheusrules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

type LLMBatchGatewayReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	HelmRenderer *HelmRenderer
}

func (r *LLMBatchGatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var gw batchv1alpha1.LLMBatchGateway
	if err := r.Get(ctx, req.NamespacedName, &gw); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching LLMBatchGateway: %w", err)
	}

	if err := validateSpec(&gw); err != nil {
		for _, condType := range []string{ConditionReady, ConditionAPIServerAvailable, ConditionProcessorAvailable} {
			meta.SetStatusCondition(&gw.Status.Conditions, metav1.Condition{
				Type:               condType,
				Status:             metav1.ConditionFalse,
				Reason:             "ValidationFailed",
				Message:            err.Error(),
				ObservedGeneration: gw.Generation,
			})
		}
		if statusErr := r.Status().Update(ctx, &gw); statusErr != nil {
			return ctrl.Result{}, fmt.Errorf("updating status after validation failure: %w", statusErr)
		}
		return ctrl.Result{}, nil
	}

	objects, err := r.HelmRenderer.RenderChart(&gw)
	if err != nil {
		meta.SetStatusCondition(&gw.Status.Conditions, metav1.Condition{
			Type:               ConditionReady,
			Status:             metav1.ConditionFalse,
			Reason:             "RenderFailed",
			Message:            err.Error(),
			ObservedGeneration: gw.Generation,
		})
		if statusErr := r.Status().Update(ctx, &gw); statusErr != nil {
			logger.Error(statusErr, "failed to update status after render failure")
		}
		return ctrl.Result{}, fmt.Errorf("rendering chart: %w", err)
	}

	for _, obj := range objects {
		obj.SetNamespace(gw.Namespace)

		if err := controllerutil.SetControllerReference(&gw, obj, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("setting owner reference on %s/%s: %w", obj.GetKind(), obj.GetName(), err)
		}

		if err := r.Patch(ctx, obj, client.Apply, client.FieldOwner(fieldOwner), client.ForceOwnership); err != nil {
			if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
				logger.V(1).Info("skipping resource (CRD not installed)", "kind", obj.GetKind(), "name", obj.GetName())
				continue
			}
			return ctrl.Result{}, fmt.Errorf("applying %s/%s: %w", obj.GetKind(), obj.GetName(), err)
		}
		logger.V(2).Info("applied resource", "kind", obj.GetKind(), "name", obj.GetName())
	}

	if err := r.deleteOrphanedResources(ctx, &gw, objects); err != nil {
		return ctrl.Result{}, fmt.Errorf("deleting orphaned resources: %w", err)
	}

	if err := r.updateStatus(ctx, &gw); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}

	return ctrl.Result{}, nil
}

func (r *LLMBatchGatewayReconciler) deleteOrphanedResources(
	ctx context.Context,
	gw *batchv1alpha1.LLMBatchGateway,
	renderedObjects []*unstructured.Unstructured,
) error {
	logger := log.FromContext(ctx)

	desired := make(map[resourceKey]struct{}, len(renderedObjects))
	for _, obj := range renderedObjects {
		gvk := obj.GroupVersionKind()
		desired[resourceKey{
			Group: gvk.Group,
			Kind:  gvk.Kind,
			Name:  obj.GetName(),
		}] = struct{}{}
	}

	var errs []error
	for _, gvk := range managedGVKs {
		var list unstructured.UnstructuredList
		list.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   gvk.Group,
			Version: gvk.Version,
			Kind:    gvk.Kind + "List",
		})

		// TODO: use client.MatchingLabels for GVKs that have consistent labels to reduce API server load
		if err := r.List(ctx, &list, client.InNamespace(gw.Namespace)); err != nil {
			if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
				logger.V(1).Info("skipping orphan check (CRD not installed)", "kind", gvk.Kind)
				continue
			}
			errs = append(errs, fmt.Errorf("listing %s: %w", gvk.Kind, err))
			continue
		}

		for i := range list.Items {
			item := &list.Items[i]
			if !isControllerOwnedBy(item, gw) {
				continue
			}

			key := resourceKey{
				Group: gvk.Group,
				Kind:  gvk.Kind,
				Name:  item.GetName(),
			}
			if _, ok := desired[key]; ok {
				continue
			}

			logger.Info("deleting orphaned resource", "kind", gvk.Kind, "name", item.GetName())
			if err := r.Delete(ctx, item); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				errs = append(errs, fmt.Errorf("deleting %s/%s: %w", gvk.Kind, item.GetName(), err))
			}
		}
	}

	return errors.Join(errs...)
}

func (r *LLMBatchGatewayReconciler) updateStatus(ctx context.Context, gw *batchv1alpha1.LLMBatchGateway) error {
	var deployments appsv1.DeploymentList
	if err := r.List(ctx, &deployments, client.InNamespace(gw.Namespace), client.MatchingLabels{
		"app.kubernetes.io/instance": gw.Name,
	}); err != nil {
		return fmt.Errorf("listing deployments: %w", err)
	}

	componentStatus := &batchv1alpha1.ComponentStatus{}
	for i := range deployments.Items {
		d := &deployments.Items[i]
		if !isOwnedBy(d, gw) {
			continue
		}

		component, ok := d.Labels["app.kubernetes.io/component"]
		if !ok {
			continue
		}

		status := &batchv1alpha1.ComponentReplicaStatus{
			Replicas:      d.Status.Replicas,
			ReadyReplicas: d.Status.ReadyReplicas,
		}

		switch component {
		case "apiserver":
			componentStatus.APIServer = status
		case "processor":
			componentStatus.Processor = status
		case "gc":
			componentStatus.GC = status
		}
	}

	gw.Status.ComponentStatus = componentStatus
	gw.Status.ObservedGeneration = gw.Generation

	apiAvailable := componentStatus.APIServer != nil && componentStatus.APIServer.ReadyReplicas >= 1
	meta.SetStatusCondition(&gw.Status.Conditions, metav1.Condition{
		Type:               ConditionAPIServerAvailable,
		Status:             conditionStatus(apiAvailable),
		Reason:             conditionReason(apiAvailable, "Available", "Unavailable"),
		Message:            conditionMessage(apiAvailable, "API server has at least one ready replica", "API server has no ready replicas"),
		ObservedGeneration: gw.Generation,
	})

	procAvailable := componentStatus.Processor != nil && componentStatus.Processor.ReadyReplicas >= 1
	meta.SetStatusCondition(&gw.Status.Conditions, metav1.Condition{
		Type:               ConditionProcessorAvailable,
		Status:             conditionStatus(procAvailable),
		Reason:             conditionReason(procAvailable, "Available", "Unavailable"),
		Message:            conditionMessage(procAvailable, "Processor has at least one ready replica", "Processor has no ready replicas"),
		ObservedGeneration: gw.Generation,
	})

	gcAvailable := componentStatus.GC != nil && componentStatus.GC.ReadyReplicas >= 1
	meta.SetStatusCondition(&gw.Status.Conditions, metav1.Condition{
		Type:               ConditionGCAvailable,
		Status:             conditionStatus(gcAvailable),
		Reason:             conditionReason(gcAvailable, "Available", "Unavailable"),
		Message:            conditionMessage(gcAvailable, "GC has at least one ready replica", "GC has no ready replicas"),
		ObservedGeneration: gw.Generation,
	})

	ready := apiAvailable && procAvailable && gcAvailable
	meta.SetStatusCondition(&gw.Status.Conditions, metav1.Condition{
		Type:               ConditionReady,
		Status:             conditionStatus(ready),
		Reason:             conditionReason(ready, "AllComponentsReady", "ComponentsNotReady"),
		Message:            conditionMessage(ready, "All components have at least one ready replica", "One or more components have no ready replicas"),
		ObservedGeneration: gw.Generation,
	})

	return r.Status().Update(ctx, gw)
}

func (r *LLMBatchGatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&batchv1alpha1.LLMBatchGateway{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.ServiceAccount{}).
		Complete(r)
}

func isControllerOwnedBy(obj metav1.Object, owner *batchv1alpha1.LLMBatchGateway) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.UID == owner.UID &&
			ref.Kind == "LLMBatchGateway" &&
			ref.APIVersion == batchv1alpha1.GroupVersion.String() &&
			ref.Controller != nil &&
			*ref.Controller {
			return true
		}
	}
	return false
}

func isOwnedBy(obj metav1.Object, owner *batchv1alpha1.LLMBatchGateway) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.UID == owner.UID {
			return true
		}
	}
	return false
}

func conditionStatus(ok bool) metav1.ConditionStatus {
	if ok {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

func conditionReason(ok bool, trueReason, falseReason string) string {
	if ok {
		return trueReason
	}
	return falseReason
}

func validateSpec(gw *batchv1alpha1.LLMBatchGateway) error {
	hasGlobal := gw.Spec.Processor.GlobalInferenceGateway != nil
	hasModel := len(gw.Spec.Processor.ModelGateways) > 0
	if !hasGlobal && !hasModel {
		return fmt.Errorf("processor must have either globalInferenceGateway or modelGateways configured")
	}
	if hasGlobal && hasModel {
		return fmt.Errorf("processor cannot have both globalInferenceGateway and modelGateways configured")
	}
	return nil
}

func conditionMessage(ok bool, trueMsg, falseMsg string) string {
	if ok {
		return trueMsg
	}
	return falseMsg
}

var _ reconcile.Reconciler = (*LLMBatchGatewayReconciler)(nil)
