package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
	"github.com/educates/agentgateway-educates-operator/internal/participantkey"
)

// AgentGatewaySessionReconciler gives one attendee their own working key.
//
// Two objects in two namespaces, because two Kubernetes namespaces will not let
// them live together (ADR-0002):
//
//   - the participant key Secret in the *workshop* namespace, holding the
//     plaintext, because the attendee's pod resolves a secretKeyRef only within
//     its own namespace
//   - the key registration ConfigMap in the *gateway* namespace, holding only
//     the hash, because agentgateway resolves API-key credentials only within
//     its own namespace
type AgentGatewaySessionReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// FinalizerTimeout bounds how long teardown retries removing the
	// registration before giving up and releasing the finalizer.
	FinalizerTimeout time.Duration
}

// +kubebuilder:rbac:groups=agentgateway.operators.educates.dev,resources=agentgatewaysessions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentgateway.operators.educates.dev,resources=agentgatewaysessions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agentgateway.operators.educates.dev,resources=agentgatewaysessions/finalizers,verbs=update
// +kubebuilder:rbac:groups=agentgateway.operators.educates.dev,resources=agentgatewaycatalogs,verbs=get;list;watch
// +kubebuilder:rbac:groups=agentgateway.operators.educates.dev,resources=agentgatewayplatforms,verbs=get;list;watch

func (r *AgentGatewaySessionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	session := &agentgatewayv1alpha1.AgentGatewaySession{}
	if err := r.Get(ctx, req.NamespacedName, session); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !session.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, session)
	}

	// Placement is checked before anything is created. A grant in a session
	// namespace produces a Secret the attendee's pod cannot resolve, and the pod
	// then wedges in CreateContainerConfigError with no useful diagnostic, so
	// it is rejected with an explanatory condition rather than reconciled
	// (ADR-0002).
	if ok, reason := r.placementValid(ctx, session); !ok {
		session.Status.ObservedGeneration = session.Generation
		return r.reject(ctx, session, reason)
	}

	// The finalizer is added before any condition is built, because a spec
	// Update returns the server's copy of the object and would discard
	// conditions accumulated in memory beforehand.
	if !controllerutil.ContainsFinalizer(session, agentgatewayv1alpha1.SessionFinalizer) {
		controllerutil.AddFinalizer(session, agentgatewayv1alpha1.SessionFinalizer)
		if err := r.Update(ctx, session); err != nil {
			return ctrl.Result{}, err
		}
	}

	session.Status.ObservedGeneration = session.Generation
	setCondition(&session.Status.Conditions, session.Generation,
		agentgatewayv1alpha1.ConditionPlacementValid, metav1.ConditionTrue,
		agentgatewayv1alpha1.ReasonReady,
		"the grant is in a workshop namespace, so the Secret is reachable by the attendee's pod")

	// Resolve the catalog, which is also how the gateway address is learned,
	// read from the platform's status, never reconstructed.
	catalog, ready, err := r.resolveCatalog(ctx, session)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ready {
		setCondition(&session.Status.Conditions, session.Generation,
			agentgatewayv1alpha1.ConditionCatalogAvailable, metav1.ConditionFalse,
			agentgatewayv1alpha1.ReasonWaiting,
			fmt.Sprintf("catalog %q is not ready", session.CatalogName()))
		setCondition(&session.Status.Conditions, session.Generation,
			agentgatewayv1alpha1.ConditionReady, metav1.ConditionFalse,
			agentgatewayv1alpha1.ReasonWaiting, "waiting for the catalog to become ready")
		session.Status.Phase = agentgatewayv1alpha1.SessionPending
		return ctrl.Result{RequeueAfter: requeueShort}, r.updateSessionStatus(ctx, session)
	}
	setCondition(&session.Status.Conditions, session.Generation,
		agentgatewayv1alpha1.ConditionCatalogAvailable, metav1.ConditionTrue,
		agentgatewayv1alpha1.ReasonReady,
		fmt.Sprintf("catalog %q is ready", session.CatalogName()))

	// The key is read from the Secret when it exists, and generated only when
	// it does not. Generating on every pass would rotate a live attendee out of
	// their session mid-workshop: the create-only defect inherited from the
	// prior art, in reverse.
	key, generated, err := r.ensureSecret(ctx, session, catalog.gatewayURL)
	if errors.Is(err, errSessionEnding) {
		// Not a failure. The attendee's namespace is going away, so the key is
		// meant to stop existing; minting a replacement would resurrect a live
		// credential the garbage collector has just removed, and leave it
		// unowned because there is no longer a namespace to own it.
		//
		// Reported as Terminating rather than Failed, and not requeued: the
		// grant's own deletion follows, and the finalizer removes the
		// registration then.
		log.Info("session namespace is terminating, so no key was minted",
			"session", session.Name, "namespace", session.Namespace)
		setCondition(&session.Status.Conditions, session.Generation,
			agentgatewayv1alpha1.ConditionSecretWritten, metav1.ConditionFalse,
			agentgatewayv1alpha1.ReasonUninstalling, err.Error())
		session.Status.Phase = agentgatewayv1alpha1.SessionTerminating
		return ctrl.Result{}, r.updateSessionStatus(ctx, session)
	}
	if err != nil {
		setCondition(&session.Status.Conditions, session.Generation,
			agentgatewayv1alpha1.ConditionSecretWritten, metav1.ConditionFalse,
			agentgatewayv1alpha1.ReasonFailed, err.Error())
		session.Status.Phase = agentgatewayv1alpha1.SessionFailed
		if statusErr := r.updateSessionStatus(ctx, session); statusErr != nil {
			log.Error(statusErr, "recording secret failure")
		}
		return ctrl.Result{}, err
	}
	if generated {
		log.Info("generated a participant key",
			"session", session.Name, "namespace", session.Namespace)
	}

	setCondition(&session.Status.Conditions, session.Generation,
		agentgatewayv1alpha1.ConditionSecretWritten, metav1.ConditionTrue,
		agentgatewayv1alpha1.ReasonReady,
		fmt.Sprintf("participant key written to Secret %s/%s", session.Namespace, session.ResourceName()))

	// Computed once and used for both the registration and status, so the
	// expiry an operator reads is the one the gateway actually enforces.
	expiresAt := r.expiryFor(session)

	// Resolved once, so the ceiling the gateway enforces and the one status
	// reports cannot disagree.
	budget := agentgatewayv1alpha1.ResolveTokenBudget(
		session.Spec.TokenBudget,
		catalog.defaultTokenBudget,
		catalog.maxTokenBudget)

	cost := agentgatewayv1alpha1.ResolveCostBudget(
		session.Spec.CostBudget,
		catalog.defaultCostBudget,
		catalog.maxCostBudget)

	// Clamping is reported rather than rejected: the grant still becomes Ready
	// and the workshop still runs, at a budget the cluster operator is willing
	// to pay for.
	if budget.Clamped || cost.Clamped {
		setCondition(&session.Status.Conditions, session.Generation,
			agentgatewayv1alpha1.ConditionBudgetWithinLimits, metav1.ConditionFalse,
			agentgatewayv1alpha1.ReasonBudgetClamped, clampMessage(budget, cost))
	} else {
		setCondition(&session.Status.Conditions, session.Generation,
			agentgatewayv1alpha1.ConditionBudgetWithinLimits, metav1.ConditionTrue,
			agentgatewayv1alpha1.ReasonReady, enforcedMessage(budget, cost))
	}

	// The registration carries the hash, the budget and the expiry, never key
	// material. A lost Secret is repaired by generating a new key and updating
	// the registration, which makes rotation the recovery path (ADR-0004).
	if err := r.ensureRegistration(ctx, session, catalog.gatewayNamespace, key, expiresAt, budget, cost); err != nil {
		setCondition(&session.Status.Conditions, session.Generation,
			agentgatewayv1alpha1.ConditionKeyRegistered, metav1.ConditionFalse,
			agentgatewayv1alpha1.ReasonFailed, err.Error())
		session.Status.Phase = agentgatewayv1alpha1.SessionFailed
		if statusErr := r.updateSessionStatus(ctx, session); statusErr != nil {
			log.Error(statusErr, "recording registration failure")
		}
		return ctrl.Result{}, err
	}

	setCondition(&session.Status.Conditions, session.Generation,
		agentgatewayv1alpha1.ConditionKeyRegistered, metav1.ConditionTrue,
		agentgatewayv1alpha1.ReasonReady,
		fmt.Sprintf("key registered in %s", catalog.gatewayNamespace))

	// Status carries the Secret name and the gateway URL so the wiring can be
	// checked by hand, and never the key or its hash, so it is safe to paste
	// into a support conversation.
	session.Status.SecretRef = &agentgatewayv1alpha1.SecretReference{Name: session.ResourceName()}
	session.Status.GatewayURL = catalog.gatewayURL
	session.Status.ExpiresAt = &metav1.Time{Time: expiresAt}
	session.Status.EffectiveTokenBudget = budget.Value
	if cost.Configured {
		session.Status.EffectiveCostBudget = formatMicroDollars(cost.MicroDollars)
	} else {
		session.Status.EffectiveCostBudget = ""
	}
	session.Status.Phase = agentgatewayv1alpha1.SessionReady

	setCondition(&session.Status.Conditions, session.Generation,
		agentgatewayv1alpha1.ConditionReady, metav1.ConditionTrue,
		agentgatewayv1alpha1.ReasonReady, "the attendee's key is live")

	return ctrl.Result{}, r.updateSessionStatus(ctx, session)
}

// placementValid reports whether the grant is in a namespace where the
// resulting Secret is reachable by the attendee's pod.
//
// Educates creates each attendee's dashboard Deployment in the *workshop*
// namespace, while session.objects default to the *session* namespace. The
// session namespace is identifiable because Educates owns it with a
// WorkshopSession, so a grant sitting in one was almost certainly missing
// `namespace: $(workshop_namespace)`.
func (r *AgentGatewaySessionReconciler) placementValid(ctx context.Context, session *agentgatewayv1alpha1.AgentGatewaySession) (bool, string) {
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: session.Namespace}, ns); err != nil {
		// A namespace that cannot be read is not grounds for rejection: the
		// grant would then fail for a reason the author cannot act on.
		return true, ""
	}

	for _, owner := range ns.OwnerReferences {
		if owner.Kind == "WorkshopSession" {
			return false, fmt.Sprintf(
				"this grant is in the session namespace %q, where the Secret it creates would be "+
					"unreachable by the attendee's pod: the pod runs in the workshop namespace and "+
					"can only resolve a secretKeyRef there. Set `namespace: $(workshop_namespace)` on "+
					"the AgentGatewaySession in session.objects.",
				session.Namespace)
		}
	}
	return true, ""
}

// reject records an explanatory condition and creates nothing.
func (r *AgentGatewaySessionReconciler) reject(ctx context.Context, session *agentgatewayv1alpha1.AgentGatewaySession, reason string) (ctrl.Result, error) {
	setCondition(&session.Status.Conditions, session.Generation,
		agentgatewayv1alpha1.ConditionPlacementValid, metav1.ConditionFalse,
		agentgatewayv1alpha1.ReasonWrongNamespace, reason)
	setCondition(&session.Status.Conditions, session.Generation,
		agentgatewayv1alpha1.ConditionReady, metav1.ConditionFalse,
		agentgatewayv1alpha1.ReasonWrongNamespace, reason)
	session.Status.Phase = agentgatewayv1alpha1.SessionRejected

	// Deliberately not requeued: the resource has to be recreated in the right
	// namespace, and retrying would only rewrite the same condition.
	return ctrl.Result{}, r.updateSessionStatus(ctx, session)
}

// resolvedCatalog is what one reconcile needs from the catalog and platform.
//
// Carried as a struct rather than as a growing tuple: the budget policy is
// read off the same object as the gateway address, and fetching it twice would
// let one reconcile resolve a budget against a catalog it did not use to
// resolve the address.
type resolvedCatalog struct {
	gatewayURL       string
	gatewayNamespace string

	// The cluster-wide budget policy grants inherit from, read through the
	// catalog's own accessors so the nil-handling lives in one place rather
	// than being repeated here. Absent values keep their unset spelling: nil
	// for a token budget, empty for a cost budget, which is what the
	// resolution functions distinguish on.
	defaultTokenBudget *int64
	maxTokenBudget     *int64
	defaultCostBudget  string
	maxCostBudget      string
}

// resolveCatalog finds the catalog and, through it, the gateway address and the
// budget policy.
func (r *AgentGatewaySessionReconciler) resolveCatalog(ctx context.Context, session *agentgatewayv1alpha1.AgentGatewaySession) (resolved resolvedCatalog, ready bool, err error) {
	catalog := &agentgatewayv1alpha1.AgentGatewayCatalog{}
	if err := r.Get(ctx, types.NamespacedName{Name: session.CatalogName()}, catalog); err != nil {
		if apierrors.IsNotFound(err) {
			return resolvedCatalog{}, false, nil
		}
		return resolvedCatalog{}, false, err
	}
	if !conditionTrue(catalog.Status.Conditions, agentgatewayv1alpha1.ConditionReady) {
		return resolvedCatalog{}, false, nil
	}

	// The gateway namespace comes from the platform, which is where the
	// registration has to land.
	platform := &agentgatewayv1alpha1.AgentGatewayPlatform{}
	if err := r.Get(ctx, types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName}, platform); err != nil {
		if apierrors.IsNotFound(err) {
			return resolvedCatalog{}, false, nil
		}
		return resolvedCatalog{}, false, err
	}
	if platform.Status.GatewayNamespace == "" {
		return resolvedCatalog{}, false, nil
	}

	return resolvedCatalog{
		gatewayURL:         catalog.Status.GatewayURL,
		gatewayNamespace:   platform.Status.GatewayNamespace,
		defaultTokenBudget: catalog.CatalogDefaultTokenBudget(),
		maxTokenBudget:     catalog.MaxTokenBudget(),
		defaultCostBudget:  catalog.CatalogDefaultCostBudget(),
		maxCostBudget:      catalog.MaxCostBudget(),
	}, true, nil
}

// clampMessage names what the author asked for and what they got, for whichever
// of the two budgets was brought down.
//
// Both numbers, because the point of surfacing the clamp is that an author can
// see their requested value did not survive rather than debugging a limit they
// did not set.
func clampMessage(budget agentgatewayv1alpha1.ResolvedTokenBudget, cost agentgatewayv1alpha1.ResolvedCostBudget) string {
	parts := []string{}
	if budget.Clamped {
		parts = append(parts, fmt.Sprintf(
			"requested a token budget of %d, clamped to the catalog maximum of %d",
			budget.Requested, budget.Value))
	}
	if cost.Clamped {
		parts = append(parts, fmt.Sprintf(
			"requested a cost budget of %s, clamped to the catalog maximum of %s",
			formatMicroDollars(cost.Requested), formatMicroDollars(cost.MicroDollars)))
	}
	return strings.Join(parts, "; ")
}

// enforcedMessage reports what the grant is actually enforced at.
func enforcedMessage(budget agentgatewayv1alpha1.ResolvedTokenBudget, cost agentgatewayv1alpha1.ResolvedCostBudget) string {
	msg := fmt.Sprintf("enforced at a token budget of %d", budget.Value)
	if cost.Configured {
		msg += fmt.Sprintf(" and a cost budget of %s", formatMicroDollars(cost.MicroDollars))
	}
	return msg
}

// formatMicroDollars renders a micro-dollar amount back as dollars, for a human
// reading a condition.
//
// Rendered with integer arithmetic rather than a float, for the same reason the
// parsing avoids one: the value is exact and should stay that way.
func formatMicroDollars(micros int64) string {
	whole := micros / agentgatewayv1alpha1.MicroDollarsPerDollar
	frac := micros % agentgatewayv1alpha1.MicroDollarsPerDollar
	if frac == 0 {
		return fmt.Sprintf("$%d.00", whole)
	}
	// Six digits, then trailing zeros trimmed, so "$0.50" does not read as
	// "$0.500000".
	s := strings.TrimRight(fmt.Sprintf("%06d", frac), "0")
	if len(s) < 2 {
		s += "0"
	}
	return fmt.Sprintf("$%d.%s", whole, s)
}

// errSessionEnding says the session namespace is gone or terminating, so no
// key should be minted.
//
// A sentinel rather than a bare error because the caller treats it as an
// ordinary end state rather than a failure: the grant is on its way out, and
// reporting Failed on a session that is simply over would be noise in exactly
// the place an operator looks when something has actually broken.
var errSessionEnding = errors.New("the session namespace is terminating, so no key is minted")

// sessionNamespaceEnding reports whether this grant's session has ended, so no
// key should be minted for it.
//
// True only when the namespace exists and is terminating. A namespace that is
// simply absent is NOT teardown: a grant created outside Educates never has
// one, and setSessionNamespaceOwner already handles that by leaving the Secret
// to be collected with the workshop namespace instead. Treating absence as
// teardown stops those grants ever being repaired, which is a worse failure
// than the one being fixed.
//
// That leaves a window this cannot close on its own. Once the namespace object
// is fully gone, a repair can still mint an unowned Secret. What bounds it is
// the grant's own deletion, which follows a session ending: the reconcile
// returns early on DeletionTimestamp, the finalizer removes the registration,
// and the expiry sweep revokes anything that outlives both. The window is the
// gap between the namespace disappearing and the grant being deleted, and a
// Secret minted inside it is removed when the workshop namespace goes.
func (r *AgentGatewaySessionReconciler) sessionNamespaceEnding(ctx context.Context, session *agentgatewayv1alpha1.AgentGatewaySession) (bool, error) {
	ns := &corev1.Namespace{}
	err := r.Get(ctx, types.NamespacedName{Name: r.sessionNamespaceName(session)}, ns)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !ns.DeletionTimestamp.IsZero(), nil
}

// ensureSecret returns the attendee's key, generating one only when the Secret
// is absent.
//
// The returned bool says whether a key was generated, which is the difference
// between a first pass and a repair.
func (r *AgentGatewaySessionReconciler) ensureSecret(ctx context.Context, session *agentgatewayv1alpha1.AgentGatewaySession, gatewayURL string) (key string, generated bool, err error) {
	name := session.ResourceName()

	live := &corev1.Secret{}
	getErr := r.Get(ctx, types.NamespacedName{Namespace: session.Namespace, Name: name}, live)
	if getErr == nil {
		existing := string(live.Data[agentgatewayv1alpha1.SecretKeyAPIKey])
		if existing != "" {
			// The key already exists, so it is reused verbatim. This is the
			// whole point: a reconcile must not rotate a live attendee out of
			// their session.
			if string(live.Data[agentgatewayv1alpha1.SecretKeyBaseURL]) != gatewayURL {
				live.Data[agentgatewayv1alpha1.SecretKeyBaseURL] = []byte(gatewayURL)
				if err := r.Update(ctx, live); err != nil {
					return "", false, fmt.Errorf("update gateway URL in Secret: %w", err)
				}
			}
			return existing, false, nil
		}
		// The Secret exists but holds no key. Treated as absent: a new key is
		// generated into it.
	} else if !apierrors.IsNotFound(getErr) {
		return "", false, getErr
	}

	// Nothing is minted once the session namespace is on its way out.
	//
	// The Secret is owned by that namespace, so when a session ends the garbage
	// collector deletes it, and the Secret watch that exists to repair an
	// accidentally deleted Secret cannot tell the two apart. Left unguarded it
	// mints a replacement moments after the collection, and because
	// setSessionNamespaceOwner finds no namespace left to own it, the
	// replacement carries no owner reference at all: an unowned Secret holding
	// a live credential that nothing will ever collect. Teardown is exactly
	// when a key should stop existing, so absence here is the desired state,
	// not damage to repair.
	//
	// Checked only on the mint path. A Secret that still exists is read and
	// reused above, so an attendee mid-session is unaffected by a namespace
	// that has only just begun terminating.
	if ending, err := r.sessionNamespaceEnding(ctx, session); err != nil {
		return "", false, err
	} else if ending {
		return "", false, errSessionEnding
	}

	key, err = participantkey.Generate()
	if err != nil {
		return "", false, err
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: session.Namespace,
			Labels: map[string]string{
				ManagedByLabel:                      ManagedByValue,
				agentgatewayv1alpha1.SessionLabel:   session.Name,
				agentgatewayv1alpha1.SessionNSLabel: session.Namespace,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			agentgatewayv1alpha1.SecretKeyAPIKey:  []byte(key),
			agentgatewayv1alpha1.SecretKeyBaseURL: []byte(gatewayURL),
		},
	}

	// The Secret is owned by the session *namespace*, not by this resource.
	//
	// That is what makes it both reachable and collectable: it sits in the
	// workshop namespace where the attendee's pod can read it, while being
	// inside the blast radius of session teardown. It is the pattern Educates
	// uses for its own session Secrets, confirmed on a live v4 cluster
	// (ADR-0002). It also means the Secret needs no finalizer.
	if err := r.setSessionNamespaceOwner(ctx, session, secret); err != nil {
		return "", false, err
	}

	if getErr == nil {
		// Repairing a Secret that existed but was empty.
		live.Data = secret.Data
		live.Labels = secret.Labels
		if err := r.Update(ctx, live); err != nil {
			return "", false, fmt.Errorf("repair participant key Secret: %w", err)
		}
		return key, true, nil
	}

	if err := r.Create(ctx, secret); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Lost a race with another pass; re-read rather than overwrite.
			if err := r.Get(ctx, types.NamespacedName{Namespace: session.Namespace, Name: name}, live); err == nil {
				if existing := string(live.Data[agentgatewayv1alpha1.SecretKeyAPIKey]); existing != "" {
					return existing, false, nil
				}
			}
		}
		return "", false, fmt.Errorf("create participant key Secret: %w", err)
	}
	return key, true, nil
}

// setSessionNamespaceOwner points the object's owner reference at the session
// namespace.
//
// Educates owns session objects by the namespace rather than by the
// WorkshopSession custom resource. The published documentation says otherwise
// and is stale; the code is authoritative and this was confirmed live
// (ADR-0002).
func (r *AgentGatewaySessionReconciler) setSessionNamespaceOwner(ctx context.Context, session *agentgatewayv1alpha1.AgentGatewaySession, obj client.Object) error {
	sessionNS := r.sessionNamespaceName(session)

	ns := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: sessionNS}, ns); err != nil {
		if apierrors.IsNotFound(err) {
			// No session namespace to own it, which is the case in a manual
			// test, or when a grant is created outside Educates. The Secret is
			// then collected with the workshop namespace instead, which is
			// still bounded.
			return nil
		}
		return err
	}

	obj.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: "v1",
		Kind:       "Namespace",
		Name:       ns.Name,
		UID:        ns.UID,
	}})
	return nil
}

// sessionNamespaceName is the session namespace for a grant.
//
// Educates names the session namespace after the session, and the grant is
// named `$(session_name)`, so they coincide.
func (r *AgentGatewaySessionReconciler) sessionNamespaceName(session *agentgatewayv1alpha1.AgentGatewaySession) string {
	return session.Name
}

// ensureRegistration writes the hash-only registration to the gateway
// namespace.
//
// One ConfigMap per session rather than one shared map with an entry per
// attendee: thirty concurrent workshop starts would otherwise contend on a
// single hot object, and duplicate keys across ConfigMaps are documented
// upstream as undefined.
func (r *AgentGatewaySessionReconciler) ensureRegistration(ctx context.Context, session *agentgatewayv1alpha1.AgentGatewaySession, gatewayNamespace, key string, expiresAt time.Time, budget agentgatewayv1alpha1.ResolvedTokenBudget, cost agentgatewayv1alpha1.ResolvedCostBudget) error {
	name := session.ResourceName()
	hash := participantkey.Hash(key)

	// An inherited budget is written as no budget at all, so the gateway falls
	// through to the shared descriptor row and a later catalog edit reaches
	// this session without it being reconciled. Only a budget the grant asked
	// for itself is pinned onto the registration.
	var registered *int64
	if !budget.Inherited {
		registered = &budget.Value
	}

	// The cost ceiling is always pinned when one applies: unlike the token
	// budget it has no shared descriptor row to fall through to, because the
	// cost descriptor keys on this field's presence.
	var registeredCost *int64
	if cost.Configured {
		registeredCost = &cost.MicroDollars
	}

	desiredEntry := buildRegistration(registrationInputs{
		keyHash:          hash,
		sessionName:      session.Name,
		tokenBudget:      registered,
		costMicroDollars: registeredCost,
		window:           session.BudgetWindow(),
		expiresAt:        expiresAt,
	})
	payload, err := marshalRegistration(desiredEntry)
	if err != nil {
		return err
	}

	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: gatewayNamespace,
			Labels: map[string]string{
				// What the single API-key policy selects on.
				agentgatewayv1alpha1.RegistrationLabel: "true",
				ManagedByLabel:                         ManagedByValue,
				// So a cluster operator can find and remove a leaked
				// registration left by a cleanup that gave up.
				agentgatewayv1alpha1.SessionLabel:   session.Name,
				agentgatewayv1alpha1.SessionNSLabel: session.Namespace,
			},
		},
		Data: map[string]string{
			// The data key name is an arbitrary identifier: agentgateway
			// iterates the map and uses the name only in error messages.
			session.Name: payload,
		},
	}

	// Deliberately no owner reference. The registration lives in a namespace
	// that outlives every session, so it is outside the blast radius of session
	// teardown, which is why it needs a finalizer (ADR-0002).

	live := &corev1.ConfigMap{}
	getErr := r.Get(ctx, types.NamespacedName{Namespace: gatewayNamespace, Name: name}, live)
	if apierrors.IsNotFound(getErr) {
		if err := r.Create(ctx, desired); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create key registration: %w", err)
		}
		return nil
	}
	if getErr != nil {
		return getErr
	}

	// Compared on the whole rendered entry, not just the hash: the budget and
	// the expiry ride in the metadata, and comparing the hash alone would
	// render a corrected payload and then discard it, leaving the gateway
	// enforcing a ceiling the grant no longer asks for.
	//
	// Still a comparison rather than an unconditional write, so a reconcile
	// that changes nothing does no write at all and the gateway is not made to
	// reload a ConfigMap it is watching.
	//
	// Note this never rotates a key on its own: the hash is derived from the
	// key the reconcile already holds, so a metadata-only change rewrites the
	// entry around an unchanged hash.
	if existing, ok := live.Data[session.Name]; ok {
		if entry, err := parseRegistration(existing); err == nil && entry.equals(desiredEntry) {
			return nil
		}
	}

	live.Data = desired.Data
	live.Labels = desired.Labels
	if err := r.Update(ctx, live); err != nil {
		return fmt.Errorf("update key registration: %w", err)
	}
	return nil
}

// expiryFor computes when the key stops working regardless of cleanup.
//
// Every key carries a TTL because force-deleting a namespace strips finalizers
// and orphans the registration outright; this is the only protection in that
// case (ADR-0002).
func (r *AgentGatewaySessionReconciler) expiryFor(session *agentgatewayv1alpha1.AgentGatewaySession) time.Time {
	ttl := session.Spec.TTL
	if ttl == "" {
		ttl = agentgatewayv1alpha1.DefaultTTL
	}
	d, err := time.ParseDuration(ttl)
	if err != nil {
		// The CRD pattern constrains this, so a parse failure here means the
		// pattern and this code disagree. Fall back rather than fail the grant.
		d, _ = time.ParseDuration(agentgatewayv1alpha1.DefaultTTL)
	}

	// Measured from creation rather than from now, so a re-reconcile does not
	// keep pushing the expiry out and defeat the backstop.
	return session.CreationTimestamp.Add(d)
}

// updateSessionStatus writes status, re-reading under conflict.
func (r *AgentGatewaySessionReconciler) updateSessionStatus(ctx context.Context, session *agentgatewayv1alpha1.AgentGatewaySession) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		live := &agentgatewayv1alpha1.AgentGatewaySession{}
		if err := r.Get(ctx, types.NamespacedName{
			Namespace: session.Namespace, Name: session.Name,
		}, live); err != nil {
			return err
		}
		live.Status = session.Status
		return r.Status().Update(ctx, live)
	})
}

func (r *AgentGatewaySessionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentgatewayv1alpha1.AgentGatewaySession{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		// Secrets are watched by label rather than with Owns(), because the
		// Secret is deliberately owned by the session *namespace* and not by
		// this resource, Owns() would match nothing, and a deleted Secret
		// would never be repaired.
		Watches(&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(mapSecretToSession)).
		// A lowered maximum has to reach grants that already pinned a budget
		// above it. Those grants bypass the shared descriptor row entirely,
		// carrying their own ceiling on their own registration, so unlike an
		// edited *default* nothing about them changes until they are
		// reconciled. Without this watch the trust boundary would hold only for
		// grants created after the edit, which is not a trust boundary.
		//
		// Deliberately no GenerationChangedPredicate: the budget block lives in
		// spec, so a generation change is exactly what this needs to see, and
		// the mapping already narrows it to the one catalog that matters.
		Watches(&agentgatewayv1alpha1.AgentGatewayCatalog{},
			handler.EnqueueRequestsFromMapFunc(r.mapCatalogToSessions)).
		Named("agentgatewaysession").
		Complete(r)
}

// mapCatalogToSessions wakes the grants an edited catalog can still reach.
//
// Deliberately not every grant. The two halves of the budget policy reach a
// running session by different routes, and only one of them needs a reconcile:
//
//   - A grant that pins nothing carries no budget on its registration, so the
//     gateway falls through to the shared descriptor row. Editing the catalog
//     rewrites that one ConfigMap and the change reaches the session with no
//     grant reconciled and no registration rewritten, which is the property
//     the design rests on.
//   - A grant that pins its own value bypasses that row entirely. A lowered
//     maximum cannot reach it any other way, so it has to be reconciled, or a
//     ceiling would bind only the grants created after it was set, which is
//     not a ceiling.
//
// So the mapping selects exactly the second group. Waking the first as well
// would be harmless in effect, since their resolved budget is unchanged, but
// it would quietly cost a reconcile per attendee on every catalog edit and
// erase the distinction the fall-through mechanism exists to create.
func (r *AgentGatewaySessionReconciler) mapCatalogToSessions(ctx context.Context, obj client.Object) []reconcile.Request {
	sessions := &agentgatewayv1alpha1.AgentGatewaySessionList{}
	if err := r.List(ctx, sessions); err != nil {
		return nil
	}

	// A catalog offering a cost budget puts one on every grant that draws on
	// it, inherited or not, so those grants need the reconcile too.
	catalog, _ := obj.(*agentgatewayv1alpha1.AgentGatewayCatalog)
	offersCost := catalog != nil && catalog.CatalogDefaultCostBudget() != ""

	requests := make([]reconcile.Request, 0, len(sessions.Items))
	for i := range sessions.Items {
		session := &sessions.Items[i]
		// Only the grants that actually draw on this catalog.
		if session.CatalogName() != obj.GetName() {
			continue
		}
		// Only those a catalog edit cannot reach any other way.
		if !session.NeedsReconcileOnCatalogChange(offersCost) {
			continue
		}
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: session.Namespace,
				Name:      session.Name,
			},
		})
	}
	return requests
}

// mapSecretToSession routes a participant key Secret back to its grant.
//
// The labels this operator writes carry both halves of the grant's identity,
// which is what makes the mapping possible without an owner reference.
func mapSecretToSession(_ context.Context, obj client.Object) []reconcile.Request {
	labels := obj.GetLabels()
	if labels[ManagedByLabel] != ManagedByValue {
		return nil
	}

	name := labels[agentgatewayv1alpha1.SessionLabel]
	namespace := labels[agentgatewayv1alpha1.SessionNSLabel]
	if name == "" || namespace == "" {
		return nil
	}

	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Namespace: namespace, Name: name},
	}}
}
