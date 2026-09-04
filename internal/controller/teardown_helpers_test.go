package controller

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
)

// Namespace-parameterised fixtures, for the teardown specs.
//
// Teardown deletes the gateway namespace, and envtest runs no namespace
// controller to finish the job, so a namespace a teardown spec has touched
// stays in Terminating for the rest of the suite and nothing can be created in
// it again. Those specs therefore each get their own.

var gatewayNamespaceCounter int

// uniqueGatewayNamespace returns a namespace name no other spec has used.
func uniqueGatewayNamespace() string {
	gatewayNamespaceCounter++
	return fmt.Sprintf("agentgateway-teardown-%d", gatewayNamespaceCounter)
}

// createPlatformIn creates a bundled platform declaration in a given namespace.
func createPlatformIn(namespace string) *agentgatewayv1alpha1.AgentGatewayPlatform {
	GinkgoHelper()

	platform := &agentgatewayv1alpha1.AgentGatewayPlatform{
		ObjectMeta: metav1.ObjectMeta{Name: agentgatewayv1alpha1.SingletonName},
		Spec: agentgatewayv1alpha1.AgentGatewayPlatformSpec{
			Provider:   agentgatewayv1alpha1.ProviderBundled,
			GatewayAPI: agentgatewayv1alpha1.GatewayAPIManaged,
			BundledAgentgateway: &agentgatewayv1alpha1.BundledAgentgatewaySpec{
				Namespace: namespace,
			},
		},
	}
	Expect(k8sClient.Create(ctx, platform)).To(Succeed())
	return platform
}

// driveToReadyIn is driveToReady for a given namespace.
func driveToReadyIn(namespace string) {
	GinkgoHelper()

	markDeploymentAvailable(namespace, AgentgatewayReleaseName)
	createAcceptedGatewayClass()

	Eventually(func() error {
		gw := newGateway()
		return k8sClient.Get(ctx,
			types.NamespacedName{Namespace: namespace, Name: GatewayName}, gw)
	}, pollTimeout, pollInterval).Should(Succeed())

	markGatewayProgrammed(namespace, GatewayName)
	markDeploymentAvailable(namespace, GatewayName)

	// Both rate-limit Deployments: readiness gates on the counter store as well
	// as the service, since the service comes up Available with no store.
	for _, name := range []string{redisName, RateLimitServiceName} {
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace, Name: name,
			}, &appsv1.Deployment{})
		}, pollTimeout, pollInterval).Should(Succeed())
		markDeploymentAvailable(namespace, name)
	}
}

// cleanupGatewayObjectsIn is cleanupGatewayObjects for a given namespace.
func cleanupGatewayObjectsIn(namespace string) {
	gw := newGateway()
	gw.SetName(GatewayName)
	gw.SetNamespace(namespace)
	_ = k8sClient.Delete(ctx, gw)

	params := newParameters()
	params.SetName(ParametersName)
	params.SetNamespace(namespace)
	_ = k8sClient.Delete(ctx, params)

	policy := newPolicy()
	policy.SetName(PolicyName)
	policy.SetNamespace(namespace)
	_ = k8sClient.Delete(ctx, policy)

	gwc := newGatewayClass()
	gwc.SetName(GatewayClassName)
	_ = k8sClient.Delete(ctx, gwc)

	_ = k8sClient.DeleteAllOf(ctx, &appsv1.Deployment{}, client.InNamespace(namespace))
}
