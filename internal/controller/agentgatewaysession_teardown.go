package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
)

// DefaultFinalizerTimeout is how long teardown retries removing the
// registration before giving up.
//
// Bounded deliberately. A finalizer that cannot complete blocks the entire
// session namespace in Terminating, and Educates makes that visible precisely
// because the namespace owns session objects. Trading a rare leaked hash for
// never wedging a cluster is the right way round: a leaked registration is
// recoverable by an out-of-band sweep, a wedged namespace needs operator
// intervention per session (ADR-0002).
const DefaultFinalizerTimeout = 2 * time.Minute

// reconcileDelete removes the registration and releases the finalizer.
//
// The participant key Secret is deliberately not touched: it is owned by the
// session namespace, so it cascades on teardown and needs no finalizer.
func (r *AgentGatewaySessionReconciler) reconcileDelete(ctx context.Context, session *agentgatewayv1alpha1.AgentGatewaySession) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(session, agentgatewayv1alpha1.SessionFinalizer) {
		return ctrl.Result{}, nil
	}

	session.Status.Phase = agentgatewayv1alpha1.SessionTerminating

	gatewayNamespace, err := r.gatewayNamespaceForTeardown(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}

	// A platform that is gone means the gateway namespace is gone with it, and
	// there is nothing left to clean up. Counted as success rather than retried,
	// so platform teardown does not strand every session behind a finalizer.
	if gatewayNamespace == "" {
		log.Info("no gateway namespace remains; treating registration cleanup as complete",
			"session", session.Name)
		return r.releaseSessionFinalizer(ctx, session)
	}

	removeErr := r.removeRegistration(ctx, session, gatewayNamespace)
	if removeErr == nil {
		return r.releaseSessionFinalizer(ctx, session)
	}

	// The retry window is measured from the deletion timestamp, so it survives
	// operator restarts: a fresh pod does not restart the clock and hold the
	// namespace for another full window.
	elapsed := time.Since(session.DeletionTimestamp.Time)
	timeout := r.finalizerTimeout()

	if elapsed < timeout {
		log.Info("registration cleanup failed; will retry within the bounded window",
			"session", session.Name,
			"namespace", gatewayNamespace,
			"elapsed", elapsed.String(),
			"timeout", timeout.String(),
			"error", removeErr.Error())
		return ctrl.Result{RequeueAfter: requeueShort}, nil
	}

	// The window has expired. Log loudly and let deletion proceed: what leaks
	// is a SHA-256 hash, not a credential: the plaintext died with the
	// workshop namespace, and every key carries a TTL that closes even that
	// window.
	log.Error(removeErr,
		"GIVING UP on removing a key registration after the bounded retry window; "+
			"releasing the finalizer to avoid wedging the session namespace. "+
			"A stale registration has been left behind and can be removed with: "+
			fmt.Sprintf("kubectl delete configmap -n %s -l %s=%s",
				gatewayNamespace, agentgatewayv1alpha1.SessionLabel, session.Name),
		"session", session.Name,
		"namespace", gatewayNamespace,
		"registration", session.ResourceName(),
		"elapsed", elapsed.String())

	return r.releaseSessionFinalizer(ctx, session)
}

// gatewayNamespaceForTeardown finds where the registration lives.
//
// Returns an empty string when the platform or its namespace is gone, which
// teardown treats as nothing-left-to-do.
func (r *AgentGatewaySessionReconciler) gatewayNamespaceForTeardown(ctx context.Context) (string, error) {
	platform := &agentgatewayv1alpha1.AgentGatewayPlatform{}
	if err := r.Get(ctx, types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName}, platform); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return "", nil
		}
		return "", err
	}

	namespace := platform.Status.GatewayNamespace
	if namespace == "" {
		namespace = platform.GatewayNamespace()
	}
	if namespace == "" {
		return "", nil
	}

	// A gateway namespace that no longer exists, or is on its way out, counts as
	// cleanup success: deleting a ConfigMap inside a Terminating namespace would
	// fail forever.
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: namespace}, ns); err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	if !ns.DeletionTimestamp.IsZero() {
		return "", nil
	}

	return namespace, nil
}

// removeRegistration deletes the session's registration ConfigMap.
func (r *AgentGatewaySessionReconciler) removeRegistration(ctx context.Context, session *agentgatewayv1alpha1.AgentGatewaySession, gatewayNamespace string) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      session.ResourceName(),
			Namespace: gatewayNamespace,
		},
	}
	if err := r.Delete(ctx, cm); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil
		}
		return err
	}
	return nil
}

// releaseSessionFinalizer removes the finalizer so deletion can complete.
func (r *AgentGatewaySessionReconciler) releaseSessionFinalizer(ctx context.Context, session *agentgatewayv1alpha1.AgentGatewaySession) (ctrl.Result, error) {
	live := &agentgatewayv1alpha1.AgentGatewaySession{}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: session.Namespace, Name: session.Name,
	}, live); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	controllerutil.RemoveFinalizer(live, agentgatewayv1alpha1.SessionFinalizer)
	if err := r.Update(ctx, live); err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
			// Gone, or changed under us; either way the next pass settles it.
			// Requeued promptly rather than immediately, because an immediate
			// requeue on a conflict would spin against whoever is writing.
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *AgentGatewaySessionReconciler) finalizerTimeout() time.Duration {
	if r.FinalizerTimeout > 0 {
		return r.FinalizerTimeout
	}
	return DefaultFinalizerTimeout
}
