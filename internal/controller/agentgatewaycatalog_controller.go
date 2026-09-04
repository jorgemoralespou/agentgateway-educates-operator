package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
)

// AgentGatewayCatalogReconciler renders the models the Gateway offers.
//
// Configures a Gateway; never installs one: it waits for the platform
// declaration to be ready and reads the gateway address from its status rather
// than reconstructing it.
type AgentGatewayCatalogReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Discovery is used to probe for agentgateway's CRDs.
	//
	// The operator installs those CRDs, so a reconcile can run before they
	// exist. A fresh discovery probe distinguishes "the CRDs are genuinely
	// absent" from "my REST mapper is stale", which otherwise look identical:
	// both surface as a no-kind-match.
	Discovery discovery.DiscoveryInterface
}

// +kubebuilder:rbac:groups=agentgateway.operators.educates.dev,resources=agentgatewaycatalogs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentgateway.operators.educates.dev,resources=agentgatewaycatalogs/status,verbs=get;update;patch

func (r *AgentGatewayCatalogReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	catalog := &agentgatewayv1alpha1.AgentGatewayCatalog{}
	if err := r.Get(ctx, req.NamespacedName, catalog); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	catalog.Status.ObservedGeneration = catalog.Generation

	// The catalog gates on the platform being ready: there is no point
	// rendering models against a gateway that is not serving.
	platform := &agentgatewayv1alpha1.AgentGatewayPlatform{}
	if err := r.Get(ctx, types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName}, platform); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		return r.notReady(ctx, catalog, agentgatewayv1alpha1.ConditionPlatformReady,
			agentgatewayv1alpha1.ReasonNotFound,
			"no AgentGatewayPlatform named 'cluster' exists yet")
	}

	if !conditionTrue(platform.Status.Conditions, agentgatewayv1alpha1.ConditionReady) {
		return r.notReady(ctx, catalog, agentgatewayv1alpha1.ConditionPlatformReady,
			agentgatewayv1alpha1.ReasonWaiting,
			"waiting for the AgentGatewayPlatform to become ready")
	}

	gatewayNamespace := platform.Status.GatewayNamespace
	gatewayURL := platform.Status.GatewayURL
	if gatewayNamespace == "" || gatewayURL == "" {
		return r.notReady(ctx, catalog, agentgatewayv1alpha1.ConditionPlatformReady,
			agentgatewayv1alpha1.ReasonWaiting,
			"the platform has not published its gateway address yet")
	}

	setCondition(&catalog.Status.Conditions, catalog.Generation,
		agentgatewayv1alpha1.ConditionPlatformReady, metav1.ConditionTrue,
		agentgatewayv1alpha1.ReasonReady, "the platform is ready")

	// agentgateway's CRDs are installed by the platform, so they may still be
	// missing on a reconcile that races ahead. A fresh discovery probe tells
	// "absent" apart from "stale mapper", and without resetting the mapper,
	// every typed call to those kinds would fail until the pod restarted.
	present, err := r.customResourceKindsPresent(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !present {
		log.V(1).Info("agentgateway CRDs are not established yet; deferring model rendering")
		return r.notReady(ctx, catalog, agentgatewayv1alpha1.ConditionModelsRendered,
			agentgatewayv1alpha1.ReasonCustomResourceKinds,
			"agentgateway's CRDs are not established yet")
	}

	if err := r.renderModels(ctx, catalog, gatewayNamespace); err != nil {
		setCondition(&catalog.Status.Conditions, catalog.Generation,
			agentgatewayv1alpha1.ConditionModelsRendered, metav1.ConditionFalse,
			agentgatewayv1alpha1.ReasonFailed, err.Error())
		catalog.Status.Phase = agentgatewayv1alpha1.CatalogFailed
		if statusErr := r.updateCatalogStatus(ctx, catalog); statusErr != nil {
			log.Error(statusErr, "recording model rendering failure")
		}
		return ctrl.Result{}, err
	}

	setCondition(&catalog.Status.Conditions, catalog.Generation,
		agentgatewayv1alpha1.ConditionModelsRendered, metav1.ConditionTrue,
		agentgatewayv1alpha1.ReasonReady,
		fmt.Sprintf("%d catalog models rendered", len(catalog.Spec.Models)))

	// The catalog names are published so an author can find out which models
	// exist without reading a resource they may not have access to.
	names := make([]string, 0, len(catalog.Spec.Models))
	for _, m := range catalog.Spec.Models {
		names = append(names, m.Name)
	}
	catalog.Status.AvailableModels = names
	catalog.Status.GatewayURL = gatewayURL
	catalog.Status.Phase = agentgatewayv1alpha1.CatalogReady

	setCondition(&catalog.Status.Conditions, catalog.Generation,
		agentgatewayv1alpha1.ConditionReady, metav1.ConditionTrue,
		agentgatewayv1alpha1.ReasonReady, "the catalog is rendered and serving")

	return ctrl.Result{}, r.updateCatalogStatus(ctx, catalog)
}

// renderModels writes one internal model plus one alias per catalog entry.
//
// The pair is what keeps attendees from learning the provider or upstream model
// behind a name: the concrete model is Internal, so it can only be selected by
// a virtual model, and the alias is what an attendee addresses.
func (r *AgentGatewayCatalogReconciler) renderModels(ctx context.Context, catalog *agentgatewayv1alpha1.AgentGatewayCatalog, namespace string) error {
	for _, model := range catalog.Spec.Models {
		if err := r.ensureInternalModel(ctx, catalog, namespace, model); err != nil {
			return fmt.Errorf("render internal model for %q: %w", model.Name, err)
		}
		if err := r.ensureAliasModel(ctx, catalog, namespace, model); err != nil {
			return fmt.Errorf("render alias for %q: %w", model.Name, err)
		}
	}
	return r.pruneModels(ctx, catalog, namespace)
}

// pruneModels deletes rendered models whose catalog entry has gone.
//
// Rendering alone is not convergence. Dropping a model from the catalog left
// its pair behind, still parented to the Gateway and still Public, so an
// attendee could go on addressing a model the catalog no longer lists, and go
// on spending against the credential behind it. Editing an entry's provider or
// upstream model is safe without this, since both objects keep the catalog
// name; it is removal and rename that strand them.
//
// Scoped by this operator's managed-by label, so a model a cluster operator
// wrote by hand in the gateway namespace is left alone. The catalog is a
// CRD-enforced singleton, so there is no sibling catalog whose models could be
// caught by the same label.
func (r *AgentGatewayCatalogReconciler) pruneModels(ctx context.Context, catalog *agentgatewayv1alpha1.AgentGatewayCatalog, namespace string) error {
	log := logf.FromContext(ctx)

	expected := make(map[string]struct{}, len(catalog.Spec.Models)*2)
	for _, model := range catalog.Spec.Models {
		expected[model.Name] = struct{}{}
		expected[internalModelName(model.Name)] = struct{}{}
	}

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
		live := &models.Items[i]
		if _, keep := expected[live.GetName()]; keep {
			continue
		}

		if err := r.Delete(ctx, live); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("prune model %q: %w", live.GetName(), err)
		}
		log.V(1).Info("pruned a model no longer in the catalog", "model", live.GetName())
	}
	return nil
}

// internalModelName is the concrete model's name. Suffixed so it cannot collide
// with the alias, which must carry the catalog name exactly.
func internalModelName(catalogName string) string {
	return catalogName + "-upstream"
}

// ensureInternalModel writes the concrete model: the provider, the upstream
// model name, and the credential.
//
// The credential lives here rather than on the alias because agentgateway's CEL
// forbids a `policies` block on a virtual model.
func (r *AgentGatewayCatalogReconciler) ensureInternalModel(ctx context.Context, catalog *agentgatewayv1alpha1.AgentGatewayCatalog, namespace string, model agentgatewayv1alpha1.CatalogModel) error {
	name := internalModelName(model.Name)

	spec := map[string]any{
		"parentRefs": []any{
			map[string]any{
				"group": GatewayAPIGroup,
				"kind":  KindGateway,
				"name":  GatewayName,
			},
		},
		"provider": string(model.Provider),
		// Internal means only a virtual model can select it, so an attendee
		// cannot address the upstream model name directly.
		"visibility": "Internal",
		"match": map[string]any{
			"model": model.Model,
		},
		"policies": map[string]any{
			"auth": map[string]any{
				"secretRef": map[string]any{
					"name": model.CredentialRef.Name,
					"key":  model.CredentialKey(),
				},
			},
		},
	}
	if model.BaseURL != "" {
		spec["baseURL"] = model.BaseURL
	}

	return r.applyModel(ctx, catalog, namespace, name, spec)
}

// ensureAliasModel writes the virtual model an attendee addresses.
//
// A single-target weighted virtual model: the degenerate case of agentgateway's
// weighted routing, which is the simplest way to express a pure alias. Failover
// and real weighting are deliberately out of scope.
func (r *AgentGatewayCatalogReconciler) ensureAliasModel(ctx context.Context, catalog *agentgatewayv1alpha1.AgentGatewayCatalog, namespace string, model agentgatewayv1alpha1.CatalogModel) error {
	spec := map[string]any{
		"parentRefs": []any{
			map[string]any{
				"group": GatewayAPIGroup,
				"kind":  KindGateway,
				"name":  GatewayName,
			},
		},
		// A virtual model must be Public; agentgateway's CEL rejects Internal.
		"visibility": "Public",
		"match": map[string]any{
			// The catalog name, exactly: a virtual model's match must not
			// contain a wildcard.
			"model": model.Name,
		},
		"virtualModel": map[string]any{
			"weighted": map[string]any{
				"targets": []any{
					map[string]any{
						"modelRef": map[string]any{
							"name": internalModelName(model.Name),
						},
						"weight": int64(1),
					},
				},
			},
		},
		// Deliberately no `policies`: agentgateway's CEL forbids it alongside
		// virtualModel, which is why the credential sits on the internal model.
	}

	return r.applyModel(ctx, catalog, namespace, model.Name, spec)
}

// applyModel creates or updates one AgentgatewayModel.
func (r *AgentGatewayCatalogReconciler) applyModel(ctx context.Context, catalog *agentgatewayv1alpha1.AgentGatewayCatalog, namespace, name string, spec map[string]any) error {
	live := newModel()
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, live)
	if apierrors.IsNotFound(err) {
		desired := newModel()
		desired.SetName(name)
		desired.SetNamespace(namespace)
		desired.SetLabels(map[string]string{ManagedByLabel: ManagedByValue})
		if err := unstructured.SetNestedMap(desired.Object, spec, "spec"); err != nil {
			return err
		}
		if err := r.Create(ctx, desired); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
		return nil
	}
	if err != nil {
		return err
	}

	if err := unstructured.SetNestedMap(live.Object, spec, "spec"); err != nil {
		return err
	}
	labels := live.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[ManagedByLabel] = ManagedByValue
	live.SetLabels(labels)

	return r.Update(ctx, live)
}

// customResourceKindsPresent probes discovery for agentgateway's kinds.
//
// A *fresh* probe, not the cached REST mapper: the mapper is what goes stale,
// and asking it would report absent for kinds that now exist. When the probe
// says the kinds are there, the mapper is reset so subsequent calls resolve.
func (r *AgentGatewayCatalogReconciler) customResourceKindsPresent(ctx context.Context) (bool, error) {
	if r.Discovery == nil {
		// No discovery client injected (some tests). Fall back to the mapper,
		// which is correct once the CRDs have been seen.
		_, err := r.RESTMapper().RESTMapping(schema.GroupKind{
			Group: AgentgatewayGroup,
			Kind:  KindAgentgatewayModel,
		}, AgentgatewayVersion)
		if err != nil {
			if meta.IsNoMatchError(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}

	groupVersion := AgentgatewayGroup + "/" + AgentgatewayVersion
	resources, err := r.Discovery.ServerResourcesForGroupVersion(groupVersion)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	for _, res := range resources.APIResources {
		if res.Kind == KindAgentgatewayModel {
			// The kinds exist. Reset the mapper so a typed or unstructured call
			// made right now resolves rather than failing on a cached miss.
			if resetter, ok := r.RESTMapper().(meta.ResettableRESTMapper); ok {
				resetter.Reset()
			}
			return true, nil
		}
	}
	return false, nil
}

// notReady records a gating condition and requeues.
func (r *AgentGatewayCatalogReconciler) notReady(ctx context.Context, catalog *agentgatewayv1alpha1.AgentGatewayCatalog, condType, reason, message string) (ctrl.Result, error) {
	setCondition(&catalog.Status.Conditions, catalog.Generation,
		condType, metav1.ConditionFalse, reason, message)
	setCondition(&catalog.Status.Conditions, catalog.Generation,
		agentgatewayv1alpha1.ConditionReady, metav1.ConditionFalse, reason, message)
	catalog.Status.Phase = agentgatewayv1alpha1.CatalogPending

	return ctrl.Result{RequeueAfter: requeueShort}, r.updateCatalogStatus(ctx, catalog)
}

func (r *AgentGatewayCatalogReconciler) updateCatalogStatus(ctx context.Context, catalog *agentgatewayv1alpha1.AgentGatewayCatalog) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		live := &agentgatewayv1alpha1.AgentGatewayCatalog{}
		if err := r.Get(ctx, types.NamespacedName{Name: catalog.Name}, live); err != nil {
			return err
		}
		live.Status = catalog.Status
		return r.Status().Update(ctx, live)
	})
}

func (r *AgentGatewayCatalogReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentgatewayv1alpha1.AgentGatewayCatalog{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		// The catalog gates on the platform, so a platform that becomes ready
		// has to wake it, otherwise the catalog waits for its requeue.
		Watches(&agentgatewayv1alpha1.AgentGatewayPlatform{},
			handler.EnqueueRequestsFromMapFunc(mapPlatformToCatalog)).
		Named("agentgatewaycatalog").
		Complete(r)
}

// mapCatalogToPlatform wakes the platform when the catalog changes.
//
// The API-key policy is rendered by the platform controller: it must be
// exactly one object, in the namespace the platform owns, but its failureMode
// is a catalog setting. Without this, changing the failure mode would sit unread
// until the next platform reconcile.
func mapCatalogToPlatform(_ context.Context, _ client.Object) []reconcile.Request {
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName},
	}}
}

// mapPlatformToCatalog wakes the catalog singleton when the platform changes.
func mapPlatformToCatalog(_ context.Context, _ client.Object) []reconcile.Request {
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName},
	}}
}
