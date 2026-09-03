package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
	"github.com/educates/agentgateway-educates-operator/internal/helm"
	vendoredcharts "github.com/educates/agentgateway-educates-operator/vendored-charts"
)

// PlatformFinalizer drains the installed releases before the platform
// declaration goes away.
//
// Unlike the session finalizer this one cannot wedge a workshop: it holds a
// cluster-scoped resource, not a namespace, so a stuck drain blocks only the
// platform resource itself.
const PlatformFinalizer = "agentgatewayplatform.agentgateway.operators.educates.dev/finalizer"

// requeueShort is how long to wait before looking again at something that is
// merely not ready yet — a chart still rolling out, a GatewayClass not yet
// created by agentgateway's controller.
const requeueShort = 15 * time.Second

// AgentGatewayPlatformReconciler installs and owns the running gateway.
type AgentGatewayPlatformReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// HelmClientFor returns a Helm client scoped to a namespace. A function
	// field rather than a concrete client so envtest can inject an in-memory
	// Helm store: the real one needs a cluster Helm can apply manifests to,
	// which envtest is not.
	HelmClientFor func(namespace string) (*helm.Client, error)
}

// +kubebuilder:rbac:groups=agentgateway.operators.educates.dev,resources=agentgatewayplatforms,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentgateway.operators.educates.dev,resources=agentgatewayplatforms/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agentgateway.operators.educates.dev,resources=agentgatewayplatforms/finalizers,verbs=update

// The operator installs agentgateway's charts, which ship their own RBAC.
// Kubernetes forbids creating a role granting permissions the creator lacks, so
// this operator needs bind and escalate on ClusterRoles — approximately
// cluster-admin equivalent, and stated plainly in the chart's README (ADR-0005).
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings;roles;rolebindings,verbs=get;list;watch;create;update;patch;delete;bind;escalate
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts;services;configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments;daemonsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch;create;update;patch;delete

// The Educates cluster config is read as an input contract and never written.
// Read-only, and only its status (the v4 contract), so a missing CRD is
// tolerated rather than fatal.
// +kubebuilder:rbac:groups=config.educates.dev,resources=educatesclusterconfigs,verbs=get;list;watch

// Gateway API and agentgateway's own resources. The operator creates the
// Gateway and the parameters overlay, and waits for the GatewayClass.
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways;gatewayclasses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways/status;gatewayclasses/status,verbs=get;list;watch
// +kubebuilder:rbac:groups=agentgateway.dev,resources=agentgatewaypolicies;agentgatewaymodels;agentgatewayparameters;agentgatewaybackends,verbs=get;list;watch;create;update;patch;delete

func (r *AgentGatewayPlatformReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	platform := &agentgatewayv1alpha1.AgentGatewayPlatform{}
	if err := r.Get(ctx, req.NamespacedName, platform); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !platform.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, platform)
	}

	if !controllerutil.ContainsFinalizer(platform, PlatformFinalizer) {
		controllerutil.AddFinalizer(platform, PlatformFinalizer)
		if err := r.Update(ctx, platform); err != nil {
			return ctrl.Result{}, err
		}
	}

	platform.Status.ObservedGeneration = platform.Generation
	platform.Status.GatewayNamespace = platform.GatewayNamespace()

	log.V(1).Info("reconciling platform", "provider", platform.Spec.Provider)

	// An external gateway installs nothing. The operator configures what is
	// already there and, critically, drains anything it installed previously —
	// switching away from bundled must not orphan the release.
	if platform.Spec.Provider == agentgatewayv1alpha1.ProviderExternal {
		return r.reconcileExternal(ctx, platform)
	}

	return r.reconcileBundled(ctx, platform)
}

// reconcileBundled runs the ordered install, each step gating the next.
func (r *AgentGatewayPlatformReconciler) reconcileBundled(ctx context.Context, platform *agentgatewayv1alpha1.AgentGatewayPlatform) (ctrl.Result, error) {
	namespace := platform.GatewayNamespace()

	// The Educates cluster config is surfaced as its own condition but does not
	// gate the install: a missing or not-yet-ready cluster config is "not ready
	// yet", never an error, and the gateway is perfectly usable on a cluster
	// that has no Educates at all. Reporting it separately is what lets a
	// cluster operator tell "my cluster config is wrong" apart from "this
	// operator is broken".
	if err := r.reconcileClusterConfig(ctx, platform); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.ensureNamespace(ctx, namespace, platform); err != nil {
		return ctrl.Result{}, err
	}

	// Step 1: Gateway API CRDs. Skipped when the cluster operator declares them
	// already present, so this operator does not fight an ingress controller
	// that owns them.
	if done, res, err := r.reconcileGatewayAPI(ctx, platform); !done {
		return res, err
	}

	// Step 2: the CRDs chart. Before the control plane: agentgateway comes up
	// but never becomes ready without its CRDs, and that failure is silent.
	if done, res, err := r.reconcileCRDsChart(ctx, platform, namespace); !done {
		return res, err
	}

	// Step 3: the control plane itself.
	if done, res, err := r.reconcileControlPlane(ctx, platform, namespace); !done {
		return res, err
	}

	// Step 4: the parameters overlay, before the Gateway, so the data-plane
	// Service is ClusterIP from the start. The overlay is retroactive, so the
	// order is belt-and-braces rather than load-bearing.
	if err := r.ensureParameters(ctx, platform, namespace); err != nil {
		return ctrl.Result{}, err
	}

	// Step 5: wait for the GatewayClass. agentgateway's own controller creates
	// it after leader election, so a ready Deployment is necessary but not
	// sufficient — and this operator must never create it itself.
	if done, res, err := r.waitForGatewayClass(ctx, platform); !done {
		return res, err
	}

	// Step 6: the Gateway. Creating it is what causes data-plane pods to appear.
	if err := r.ensureGateway(ctx, platform, namespace); err != nil {
		return ctrl.Result{}, err
	}

	// Step 7: readiness. Gates on Programmed plus the data-plane Deployment,
	// never on the Gateway's addresses (ADR-0005).
	if done, res, err := r.reconcileGatewayReady(ctx, platform, namespace); !done {
		return res, err
	}

	// Step 8: the rate-limit service and its counter store, so budgets can be
	// enforced before any key exists to enforce them against.
	if done, res, err := r.reconcileRateLimit(ctx, platform, namespace); !done {
		return res, err
	}

	// Step 9: the single API-key policy. Last, because it targets the Gateway
	// and references the rate-limit Service, both of which must exist first.
	if err := r.ensurePolicy(ctx, namespace); err != nil {
		setCondition(&platform.Status.Conditions, platform.Generation,
			agentgatewayv1alpha1.ConditionPolicyReady, metav1.ConditionFalse,
			agentgatewayv1alpha1.ReasonFailed, err.Error())
		platform.Status.Phase = agentgatewayv1alpha1.PlatformFailed
		if statusErr := r.updateStatus(ctx, platform); statusErr != nil {
			logf.FromContext(ctx).Error(statusErr, "recording policy failure")
		}
		return ctrl.Result{}, err
	}
	setCondition(&platform.Status.Conditions, platform.Generation,
		agentgatewayv1alpha1.ConditionPolicyReady, metav1.ConditionTrue,
		agentgatewayv1alpha1.ReasonReady, "the API-key policy is applied")

	r.markReady(platform, namespace)
	return ctrl.Result{}, r.updateStatus(ctx, platform)
}

// reconcileExternal configures a gateway somebody else installed, and drains
// any bundled release this operator left behind.
func (r *AgentGatewayPlatformReconciler) reconcileExternal(ctx context.Context, platform *agentgatewayv1alpha1.AgentGatewayPlatform) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	ext := platform.Spec.ExternalAgentgateway
	if ext == nil {
		setCondition(&platform.Status.Conditions, platform.Generation,
			agentgatewayv1alpha1.ConditionReady, metav1.ConditionFalse,
			agentgatewayv1alpha1.ReasonFailed,
			"provider is ExternalAgentgateway but spec.externalAgentgateway is not set")
		platform.Status.Phase = agentgatewayv1alpha1.PlatformFailed
		return ctrl.Result{}, r.updateStatus(ctx, platform)
	}

	// Switching to external actively drains the bundled release rather than
	// orphaning it. This is v4's platform-extras behaviour; its cluster-services
	// path has the orphaning bug (ADR-0005).
	if err := r.drainBundled(ctx, platform.GatewayNamespace()); err != nil {
		log.Error(err, "draining bundled release after switch to external provider")
		return ctrl.Result{}, err
	}

	setCondition(&platform.Status.Conditions, platform.Generation,
		agentgatewayv1alpha1.ConditionGatewayAPIReady, metav1.ConditionTrue,
		agentgatewayv1alpha1.ReasonReady, "Gateway API is the external gateway's concern")
	setCondition(&platform.Status.Conditions, platform.Generation,
		agentgatewayv1alpha1.ConditionControlPlaneReady, metav1.ConditionTrue,
		agentgatewayv1alpha1.ReasonReady, "using an externally installed agentgateway; nothing installed here")

	// Readiness still gates on the referenced Gateway reporting Programmed:
	// this operator did not install it, but a session cannot work until it
	// serves traffic.
	if done, res, err := r.reconcileGatewayReady(ctx, platform, ext.Namespace); !done {
		return res, err
	}

	r.markReady(platform, ext.Namespace)
	return ctrl.Result{}, r.updateStatus(ctx, platform)
}

// reconcileClusterConfig surfaces the Educates cluster config's readiness as a
// condition.
//
// Deliberately non-gating. The v4 installer's own components block on this, but
// they are useless without it; this operator's gateway is not, so a cluster
// without Educates gets a working gateway and an honest condition rather than a
// permanently pending install.
func (r *AgentGatewayPlatformReconciler) reconcileClusterConfig(ctx context.Context, platform *agentgatewayv1alpha1.AgentGatewayPlatform) error {
	state, err := clusterConfigStatus(ctx, r.Client)
	if err != nil {
		return err
	}

	status := metav1.ConditionFalse
	reason := agentgatewayv1alpha1.ReasonWaiting
	switch {
	case state.Ready:
		status = metav1.ConditionTrue
		reason = agentgatewayv1alpha1.ReasonReady
	case !state.Present:
		reason = agentgatewayv1alpha1.ReasonNotFound
	}

	setCondition(&platform.Status.Conditions, platform.Generation,
		agentgatewayv1alpha1.ConditionClusterConfigAvailable, status, reason, state.Message)
	return nil
}

// reconcileGatewayAPI applies the Gateway API CRDs unless they are declared
// already present.
func (r *AgentGatewayPlatformReconciler) reconcileGatewayAPI(ctx context.Context, platform *agentgatewayv1alpha1.AgentGatewayPlatform) (bool, ctrl.Result, error) {
	if platform.Spec.GatewayAPI == agentgatewayv1alpha1.GatewayAPIExisting {
		setCondition(&platform.Status.Conditions, platform.Generation,
			agentgatewayv1alpha1.ConditionGatewayAPIReady, metav1.ConditionTrue,
			agentgatewayv1alpha1.ReasonReady,
			"Gateway API declared as already present; installation skipped")
		return true, ctrl.Result{}, nil
	}

	present, err := r.gatewayAPIPresent(ctx)
	if err != nil {
		return false, ctrl.Result{}, err
	}
	if !present {
		// The CRDs are applied by the operator's own chart rather than here:
		// they are a static, versioned artefact with no per-cluster variation,
		// and applying them from a reconcile would need the operator to carry a
		// second copy of Gateway API.
		setCondition(&platform.Status.Conditions, platform.Generation,
			agentgatewayv1alpha1.ConditionGatewayAPIReady, metav1.ConditionFalse,
			agentgatewayv1alpha1.ReasonWaiting,
			"Gateway API CRDs are not present; they are installed by this operator's Helm chart")
		platform.Status.Phase = agentgatewayv1alpha1.PlatformInstalling
		return false, ctrl.Result{RequeueAfter: requeueShort}, r.updateStatus(ctx, platform)
	}

	setCondition(&platform.Status.Conditions, platform.Generation,
		agentgatewayv1alpha1.ConditionGatewayAPIReady, metav1.ConditionTrue,
		agentgatewayv1alpha1.ReasonReady, "Gateway API CRDs are present")
	return true, ctrl.Result{}, nil
}

// reconcileCRDsChart converges the agentgateway-crds release.
func (r *AgentGatewayPlatformReconciler) reconcileCRDsChart(ctx context.Context, platform *agentgatewayv1alpha1.AgentGatewayPlatform, namespace string) (bool, ctrl.Result, error) {
	chrt, err := vendoredcharts.AgentgatewayCRDs()
	if err != nil {
		return false, ctrl.Result{}, fmt.Errorf("load agentgateway-crds chart: %w", err)
	}

	res, err := r.converge(ctx, namespace, AgentgatewayCRDsReleaseName, chrt,
		renderAgentgatewayCRDsValues(platform))
	if err != nil {
		return false, ctrl.Result{}, err
	}

	if proceed, result, err := r.handleReleaseResult(ctx, platform,
		"agentgateway-crds", agentgatewayv1alpha1.ConditionControlPlaneReady, res); !proceed {
		return false, result, err
	}
	return true, ctrl.Result{}, nil
}

// reconcileControlPlane converges the agentgateway release and waits for its
// Deployment.
func (r *AgentGatewayPlatformReconciler) reconcileControlPlane(ctx context.Context, platform *agentgatewayv1alpha1.AgentGatewayPlatform, namespace string) (bool, ctrl.Result, error) {
	chrt, err := vendoredcharts.Agentgateway()
	if err != nil {
		return false, ctrl.Result{}, fmt.Errorf("load agentgateway chart: %w", err)
	}

	res, err := r.converge(ctx, namespace, AgentgatewayReleaseName, chrt,
		renderAgentgatewayValues(platform))
	if err != nil {
		return false, ctrl.Result{}, err
	}

	if proceed, result, err := r.handleReleaseResult(ctx, platform,
		"agentgateway", agentgatewayv1alpha1.ConditionControlPlaneReady, res); !proceed {
		return false, result, err
	}

	platform.Status.BundledChartVersions = &agentgatewayv1alpha1.ChartVersions{
		Agentgateway:     vendoredcharts.AgentgatewayChartVersion,
		AgentgatewayCRDs: vendoredcharts.AgentgatewayCRDsChartVersion,
	}

	available, err := r.deploymentAvailable(ctx, namespace, AgentgatewayReleaseName)
	if err != nil {
		return false, ctrl.Result{}, err
	}
	if !available {
		setCondition(&platform.Status.Conditions, platform.Generation,
			agentgatewayv1alpha1.ConditionControlPlaneReady, metav1.ConditionFalse,
			agentgatewayv1alpha1.ReasonWaiting,
			"waiting for the agentgateway control-plane Deployment to become available")
		platform.Status.Phase = agentgatewayv1alpha1.PlatformInstalling
		return false, ctrl.Result{RequeueAfter: requeueShort}, r.updateStatus(ctx, platform)
	}

	setCondition(&platform.Status.Conditions, platform.Generation,
		agentgatewayv1alpha1.ConditionControlPlaneReady, metav1.ConditionTrue,
		agentgatewayv1alpha1.ReasonReady, "agentgateway control plane is available")
	return true, ctrl.Result{}, nil
}

// waitForGatewayClass waits for agentgateway's controller to create and accept
// the GatewayClass.
//
// This operator never creates it. agentgateway's controller does, after leader
// election, which is why a ready Deployment is not enough to proceed on
// (ADR-0005).
func (r *AgentGatewayPlatformReconciler) waitForGatewayClass(ctx context.Context, platform *agentgatewayv1alpha1.AgentGatewayPlatform) (bool, ctrl.Result, error) {
	gwc := newGatewayClass()
	err := r.Get(ctx, types.NamespacedName{Name: GatewayClassName}, gwc)
	if err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			setCondition(&platform.Status.Conditions, platform.Generation,
				agentgatewayv1alpha1.ConditionGatewayProgrammed, metav1.ConditionFalse,
				agentgatewayv1alpha1.ReasonWaiting,
				fmt.Sprintf("waiting for agentgateway's controller to create GatewayClass %q", GatewayClassName))
			platform.Status.Phase = agentgatewayv1alpha1.PlatformInstalling
			return false, ctrl.Result{RequeueAfter: requeueShort}, r.updateStatus(ctx, platform)
		}
		return false, ctrl.Result{}, err
	}

	if !unstructuredConditionTrue(gwc, "Accepted") {
		setCondition(&platform.Status.Conditions, platform.Generation,
			agentgatewayv1alpha1.ConditionGatewayProgrammed, metav1.ConditionFalse,
			agentgatewayv1alpha1.ReasonWaiting,
			fmt.Sprintf("GatewayClass %q has not reported Accepted", GatewayClassName))
		platform.Status.Phase = agentgatewayv1alpha1.PlatformInstalling
		return false, ctrl.Result{RequeueAfter: requeueShort}, r.updateStatus(ctx, platform)
	}

	return true, ctrl.Result{}, nil
}

// reconcileGatewayReady gates on the Gateway reporting Programmed plus its
// data-plane Deployment.
//
// Deliberately not on status.addresses: a ClusterIP Gateway does populate them,
// but a LoadBalancer one on a cluster with no load-balancer provider never
// would, and Programmed plus Deployment is correct for every service type.
func (r *AgentGatewayPlatformReconciler) reconcileGatewayReady(ctx context.Context, platform *agentgatewayv1alpha1.AgentGatewayPlatform, namespace string) (bool, ctrl.Result, error) {
	gatewayName := GatewayName
	if platform.Spec.Provider == agentgatewayv1alpha1.ProviderExternal &&
		platform.Spec.ExternalAgentgateway != nil {
		gatewayName = platform.Spec.ExternalAgentgateway.GatewayName
	}

	gw := newGateway()
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: gatewayName}, gw)
	if err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			setCondition(&platform.Status.Conditions, platform.Generation,
				agentgatewayv1alpha1.ConditionGatewayProgrammed, metav1.ConditionFalse,
				agentgatewayv1alpha1.ReasonWaiting,
				fmt.Sprintf("Gateway %s/%s does not exist yet", namespace, gatewayName))
			platform.Status.Phase = agentgatewayv1alpha1.PlatformInstalling
			return false, ctrl.Result{RequeueAfter: requeueShort}, r.updateStatus(ctx, platform)
		}
		return false, ctrl.Result{}, err
	}

	if !unstructuredConditionTrue(gw, "Programmed") {
		setCondition(&platform.Status.Conditions, platform.Generation,
			agentgatewayv1alpha1.ConditionGatewayProgrammed, metav1.ConditionFalse,
			agentgatewayv1alpha1.ReasonWaiting,
			fmt.Sprintf("Gateway %s/%s has not reported Programmed", namespace, gatewayName))
		platform.Status.Phase = agentgatewayv1alpha1.PlatformInstalling
		return false, ctrl.Result{RequeueAfter: requeueShort}, r.updateStatus(ctx, platform)
	}

	// agentgateway names the data-plane Deployment after the Gateway.
	available, err := r.deploymentAvailable(ctx, namespace, gatewayName)
	if err != nil {
		return false, ctrl.Result{}, err
	}
	if !available {
		setCondition(&platform.Status.Conditions, platform.Generation,
			agentgatewayv1alpha1.ConditionGatewayProgrammed, metav1.ConditionFalse,
			agentgatewayv1alpha1.ReasonWaiting,
			fmt.Sprintf("waiting for the data-plane Deployment %s/%s to become available", namespace, gatewayName))
		platform.Status.Phase = agentgatewayv1alpha1.PlatformInstalling
		return false, ctrl.Result{RequeueAfter: requeueShort}, r.updateStatus(ctx, platform)
	}

	setCondition(&platform.Status.Conditions, platform.Generation,
		agentgatewayv1alpha1.ConditionGatewayProgrammed, metav1.ConditionTrue,
		agentgatewayv1alpha1.ReasonReady,
		fmt.Sprintf("Gateway %s/%s is programmed and its data plane is available", namespace, gatewayName))
	return true, ctrl.Result{}, nil
}

// markReady sets the summary condition and publishes the gateway URL other
// resources read.
func (r *AgentGatewayPlatformReconciler) markReady(platform *agentgatewayv1alpha1.AgentGatewayPlatform, namespace string) {
	platform.Status.GatewayURL = r.gatewayURL(platform, namespace)
	platform.Status.GatewayNamespace = namespace
	platform.Status.Phase = agentgatewayv1alpha1.PlatformReady

	setCondition(&platform.Status.Conditions, platform.Generation,
		agentgatewayv1alpha1.ConditionReady, metav1.ConditionTrue,
		agentgatewayv1alpha1.ReasonReady, "the gateway is installed and serving")
}

// gatewayURL is the in-cluster address sessions use. Nothing outside the cluster
// reaches the data plane, so this is always a cluster-local DNS name.
func (r *AgentGatewayPlatformReconciler) gatewayURL(platform *agentgatewayv1alpha1.AgentGatewayPlatform, namespace string) string {
	if platform.Spec.Provider == agentgatewayv1alpha1.ProviderExternal &&
		platform.Spec.ExternalAgentgateway != nil {
		if platform.Spec.ExternalAgentgateway.GatewayURL != "" {
			return platform.Spec.ExternalAgentgateway.GatewayURL
		}
		return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d",
			platform.Spec.ExternalAgentgateway.GatewayName, namespace, GatewayPort)
	}
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", GatewayName, namespace, GatewayPort)
}

// converge builds a namespace-scoped Helm client and converges one release,
// refusing a release this operator does not own.
func (r *AgentGatewayPlatformReconciler) converge(ctx context.Context, namespace, releaseName string, chrt *helm.Chart, vals map[string]any) (helm.Result, error) {
	hc, err := r.HelmClientFor(namespace)
	if err != nil {
		return helm.Result{}, fmt.Errorf("build helm client for %s: %w", namespace, err)
	}
	return hc.EnsureRelease(ctx, releaseName, chrt, vals)
}

// handleReleaseResult turns a converge outcome into conditions, and says whether
// to carry on with the next install step.
func (r *AgentGatewayPlatformReconciler) handleReleaseResult(
	ctx context.Context,
	platform *agentgatewayv1alpha1.AgentGatewayPlatform,
	service string,
	condType string,
	res helm.Result,
) (bool, ctrl.Result, error) {
	switch res.Action {
	case helm.ActionRefusedNotOwned:
		// The whole point of the ownership marker: report a conflict rather than
		// taking over a release another operator owns and fighting it forever.
		setCondition(&platform.Status.Conditions, platform.Generation,
			condType, metav1.ConditionFalse,
			agentgatewayv1alpha1.ReasonReleaseConflict,
			fmt.Sprintf("Helm release %q exists but is not owned by this operator; refusing to converge it", service))
		setCondition(&platform.Status.Conditions, platform.Generation,
			agentgatewayv1alpha1.ConditionReady, metav1.ConditionFalse,
			agentgatewayv1alpha1.ReasonReleaseConflict,
			fmt.Sprintf("Helm release %q is owned by someone else", service))
		platform.Status.Phase = agentgatewayv1alpha1.PlatformFailed
		return false, ctrl.Result{}, r.updateStatus(ctx, platform)

	case helm.ActionHeldFailed:
		setCondition(&platform.Status.Conditions, platform.Generation,
			condType, metav1.ConditionFalse,
			agentgatewayv1alpha1.ReasonFailed,
			helm.FailureMessage(res.Release, fmt.Sprintf("%s Helm release is in a failed state", service)))
		platform.Status.Phase = agentgatewayv1alpha1.PlatformFailed
		return false, ctrl.Result{}, r.updateStatus(ctx, platform)

	case helm.ActionRepairedRollback:
		setCondition(&platform.Status.Conditions, platform.Generation,
			condType, metav1.ConditionFalse,
			agentgatewayv1alpha1.ReasonInstalling,
			fmt.Sprintf("rolled %s back to its last deployed revision; re-applying the desired configuration", service))
		platform.Status.Phase = agentgatewayv1alpha1.PlatformInstalling
		return false, ctrl.Result{RequeueAfter: requeueShort}, r.updateStatus(ctx, platform)

	case helm.ActionWaitingUninstall:
		setCondition(&platform.Status.Conditions, platform.Generation,
			condType, metav1.ConditionFalse,
			agentgatewayv1alpha1.ReasonWaiting,
			fmt.Sprintf("%s is being uninstalled; waiting for teardown before re-converging", service))
		platform.Status.Phase = agentgatewayv1alpha1.PlatformInstalling
		return false, ctrl.Result{RequeueAfter: requeueShort}, r.updateStatus(ctx, platform)

	default:
		return true, ctrl.Result{}, nil
	}
}

// updateStatus writes status, re-reading under conflict so a concurrent spec
// update does not lose it.
func (r *AgentGatewayPlatformReconciler) updateStatus(ctx context.Context, platform *agentgatewayv1alpha1.AgentGatewayPlatform) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		live := &agentgatewayv1alpha1.AgentGatewayPlatform{}
		if err := r.Get(ctx, types.NamespacedName{Name: platform.Name}, live); err != nil {
			return err
		}
		live.Status = platform.Status
		return r.Status().Update(ctx, live)
	})
}

// ensureNamespace creates the gateway namespace, labelled and owned by the
// platform declaration so it cascades on delete.
func (r *AgentGatewayPlatformReconciler) ensureNamespace(ctx context.Context, name string, owner *agentgatewayv1alpha1.AgentGatewayPlatform) error {
	ns := &corev1.Namespace{}
	err := r.Get(ctx, types.NamespacedName{Name: name}, ns)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}

	ns = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{ManagedByLabel: ManagedByValue},
		},
	}
	if err := controllerutil.SetControllerReference(owner, ns, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// deploymentAvailable reports whether a Deployment exists and has the Available
// condition. A missing Deployment is "not yet", not an error.
func (r *AgentGatewayPlatformReconciler) deploymentAvailable(ctx context.Context, namespace, name string) (bool, error) {
	deploy := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, deploy); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	for _, cond := range deploy.Status.Conditions {
		if cond.Type == appsv1.DeploymentAvailable {
			return cond.Status == corev1.ConditionTrue, nil
		}
	}
	return false, nil
}

func (r *AgentGatewayPlatformReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentgatewayv1alpha1.AgentGatewayPlatform{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&corev1.Namespace{}).
		// The policy this controller renders takes its failureMode from the
		// catalog, so a catalog change has to wake it.
		Watches(&agentgatewayv1alpha1.AgentGatewayCatalog{},
			handler.EnqueueRequestsFromMapFunc(mapCatalogToPlatform)).
		Named("agentgatewayplatform").
		Complete(r)
}
