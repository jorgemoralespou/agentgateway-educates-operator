package controller

import (
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
		return err
	}

	return nil
}
