package controller

import (
	"fmt"

	"k8s.io/client-go/discovery"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/educates/agentgateway-educates-operator/internal/helm"
)

// HelmClientFactory returns a Helm client scoped to a namespace.
//
// A function type rather than a concrete client so tests can inject an
// in-memory Helm store: the real client needs a cluster Helm can apply
// manifests to, which envtest is not.
type HelmClientFactory func(namespace string) (*helm.Client, error)

// SetupWithManager registers every controller in this operator.
//
// One entrypoint so cmd/main.go does not have to know which controllers exist
// or what each of them needs.
func SetupWithManager(mgr ctrl.Manager, helmClientFor HelmClientFactory, operatorNamespace string) error {
	if err := (&AgentGatewayPlatformReconciler{
		Client:        mgr.GetClient(),
		Scheme:        mgr.GetScheme(),
		HelmClientFor: helmClientFor,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("set up the platform controller: %w", err)
	}

	// A discovery client, not the manager's REST mapper: the catalog has to
	// tell "agentgateway's CRDs are genuinely absent" apart from "my mapper is
	// stale", and only a fresh probe can.
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(mgr.GetConfig())
	if err != nil {
		return fmt.Errorf("build discovery client: %w", err)
	}

	if err := (&AgentGatewayCatalogReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Discovery: discoveryClient,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("set up the catalog controller: %w", err)
	}

	if err := (&AgentGatewaySessionReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("set up the session controller: %w", err)
	}

	// The TTL backstop. Enforced here rather than at the gateway because
	// agentgateway 1.5.0's CEL has no current-time function to compare an
	// expiry against — see agentgatewaysession_expiry.go.
	if err := (&ExpirySweeper{
		Client:              mgr.GetClient(),
		GatewayNamespaceFor: gatewayNamespaceFromPlatform(mgr.GetClient()),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("set up the key expiry sweep: %w", err)
	}

	return nil
}
