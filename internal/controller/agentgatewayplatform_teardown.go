package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
)

// reconcileDelete drains everything this operator installed, then releases the
// finalizer.
//
// Order is the strict reverse of install. Every step tolerates a missing object
// and a missing kind, because the Educates installer's teardown guard does not
// wait for this operator and can remove cluster services, and the CRDs behind
// these kinds, first.
func (r *AgentGatewayPlatformReconciler) reconcileDelete(ctx context.Context, platform *agentgatewayv1alpha1.AgentGatewayPlatform) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(platform, PlatformFinalizer) {
		return ctrl.Result{}, nil
	}

	platform.Status.Phase = agentgatewayv1alpha1.PlatformUninstalling
	setCondition(&platform.Status.Conditions, platform.Generation,
		agentgatewayv1alpha1.ConditionReady, metav1.ConditionFalse,
		agentgatewayv1alpha1.ReasonUninstalling, "uninstalling the gateway")
	if err := r.updateStatus(ctx, platform); err != nil {
		log.V(1).Info("could not record uninstalling status; continuing with teardown", "error", err)
	}

	namespace := platform.GatewayNamespace()

	// An external gateway was never installed here, so there is nothing to
	// drain but the operator's own additions.
	if platform.Spec.Provider == agentgatewayv1alpha1.ProviderExternal {
		if err := r.drainConfiguration(ctx, namespace); err != nil {
			return ctrl.Result{}, err
		}
		return r.releaseFinalizer(ctx, platform)
	}

	if err := r.drainAll(ctx, namespace); err != nil {
		return ctrl.Result{}, err
	}
	return r.releaseFinalizer(ctx, platform)
}

// drainAll removes everything, in strict reverse install order.
//
// Idempotent throughout: a retried drain after a partial failure re-attempts
// only what is still there, because every step tolerates absence.
func (r *AgentGatewayPlatformReconciler) drainAll(ctx context.Context, namespace string) error {
	// 7, 6: the policy and the Gateway, which takes its data-plane pods with it
	// by owner reference.
	if err := r.drainConfiguration(ctx, namespace); err != nil {
		return err
	}

	// 5: the rate-limit service and its counter store.
	if err := r.uninstallRelease(ctx, namespace, RateLimitReleaseName); err != nil {
		return err
	}

	// 4, 3: the control plane, then its CRDs.
	if err := r.uninstallRelease(ctx, namespace, AgentgatewayReleaseName); err != nil {
		return err
	}
	if err := r.uninstallRelease(ctx, namespace, AgentgatewayCRDsReleaseName); err != nil {
		return err
	}

	// The namespace is deleted last, and only because this operator created it.
	// Nothing agentgateway installs carries a finalizer, so nothing here can
	// block the namespace delete (ADR-0005).
	return r.deleteIfPresent(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: namespace},
	})
}

// drainConfiguration removes the objects this operator creates directly, as
// opposed to via Helm.
func (r *AgentGatewayPlatformReconciler) drainConfiguration(ctx context.Context, namespace string) error {
	policy := newPolicy()
	policy.SetName(PolicyName)
	policy.SetNamespace(namespace)
	if err := r.deleteIfPresent(ctx, policy); err != nil {
		return fmt.Errorf("delete API-key policy: %w", err)
	}

	gw := newGateway()
	gw.SetName(GatewayName)
	gw.SetNamespace(namespace)
	if err := r.deleteIfPresent(ctx, gw); err != nil {
		return fmt.Errorf("delete Gateway: %w", err)
	}

	// Also the pre-rename Gateway, in case teardown is the first thing an
	// upgraded operator does and reconcile never got the chance to prune it.
	// Label-guarded exactly as pruneLegacyGateway is: a Gateway of that name
	// this operator did not create is not ours to remove, on teardown either.
	if err := r.pruneLegacyGateway(ctx, namespace); err != nil {
		return fmt.Errorf("delete legacy Gateway: %w", err)
	}

	params := newParameters()
	params.SetName(ParametersName)
	params.SetNamespace(namespace)
	if err := r.deleteIfPresent(ctx, params); err != nil {
		return fmt.Errorf("delete parameters overlay: %w", err)
	}

	if err := r.drainModels(ctx, namespace); err != nil {
		return fmt.Errorf("delete catalog models: %w", err)
	}

	return nil
}

// drainModels removes the models the catalog rendered.
//
// Unlike everything else here these have no fixed name, a catalog names them,
// so they are found by label rather than constructed. They also carry no
// ownerReferences, so nothing collects them on their own: without this they
// outlive the Gateway they were parented to, and a later platform in the same
// namespace inherits models from a catalog that may since have changed.
//
// This runs on a provider switch as well as a full teardown, which is right in
// both cases: the models point at a Gateway that is going away either way, and
// the catalog re-renders against the new one once it is ready.
//
// Label-scoped, so a model a cluster operator wrote by hand is left alone, and
// tolerant of a missing kind: agentgateway's CRDs may already be gone by the
// time this runs (ADR-0005).
func (r *AgentGatewayPlatformReconciler) drainModels(ctx context.Context, namespace string) error {
	models := newModelList()
	if err := r.List(ctx, models,
		client.InNamespace(namespace),
		client.MatchingLabels{ManagedByLabel: ManagedByValue},
	); err != nil {
		if meta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	for i := range models.Items {
		if err := r.deleteIfPresent(ctx, &models.Items[i]); err != nil {
			return err
		}
	}
	return nil
}

// drainBundled removes the bundled releases without touching the namespace.
//
// Called when the provider is switched to external: the release must be
// uninstalled rather than orphaned, which is the bug in v4's cluster-services
// path that its platform-extras path does not have (ADR-0005).
func (r *AgentGatewayPlatformReconciler) drainBundled(ctx context.Context, namespace string) error {
	for _, releaseName := range []string{
		RateLimitReleaseName,
		AgentgatewayReleaseName,
		AgentgatewayCRDsReleaseName,
	} {
		if err := r.uninstallRelease(ctx, namespace, releaseName); err != nil {
			return err
		}
	}
	return nil
}

// uninstallRelease removes one Helm release, tolerating its absence.
func (r *AgentGatewayPlatformReconciler) uninstallRelease(ctx context.Context, namespace, releaseName string) error {
	hc, err := r.HelmClientFor(namespace)
	if err != nil {
		return fmt.Errorf("build helm client for %s: %w", namespace, err)
	}
	if err := hc.Uninstall(releaseName); err != nil {
		return fmt.Errorf("uninstall %s: %w", releaseName, err)
	}
	return nil
}

// releaseFinalizer removes the finalizer so deletion can proceed.
func (r *AgentGatewayPlatformReconciler) releaseFinalizer(ctx context.Context, platform *agentgatewayv1alpha1.AgentGatewayPlatform) (ctrl.Result, error) {
	live := &agentgatewayv1alpha1.AgentGatewayPlatform{}
	if err := r.Get(ctx, types.NamespacedName{Name: platform.Name}, live); err != nil {
		return ctrl.Result{}, nil
	}
	controllerutil.RemoveFinalizer(live, PlatformFinalizer)
	if err := r.Update(ctx, live); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}
