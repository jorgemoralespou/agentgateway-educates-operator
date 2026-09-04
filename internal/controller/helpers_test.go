package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
	"github.com/educates/agentgateway-educates-operator/internal/helm"
)

// Fixtures for driving the reconciler under envtest.
//
// envtest is an API server with no controllers: no Deployment controller, no
// garbage collector, and nothing that would make a Gateway report Programmed.
// Anything a real cluster's controllers would do has to be simulated here,
// which is also what makes the resulting assertions honest — the test says
// exactly which upstream signal it is standing in for.

// createPlatform creates the default bundled platform declaration.
func createPlatform() *agentgatewayv1alpha1.AgentGatewayPlatform {
	GinkgoHelper()

	platform := &agentgatewayv1alpha1.AgentGatewayPlatform{
		ObjectMeta: metav1.ObjectMeta{Name: agentgatewayv1alpha1.SingletonName},
		Spec: agentgatewayv1alpha1.AgentGatewayPlatformSpec{
			Provider:   agentgatewayv1alpha1.ProviderBundled,
			GatewayAPI: agentgatewayv1alpha1.GatewayAPIManaged,
			BundledAgentgateway: &agentgatewayv1alpha1.BundledAgentgatewaySpec{
				Namespace: testGatewayNamespace,
			},
		},
	}
	Expect(k8sClient.Create(ctx, platform)).To(Succeed())
	return platform
}

// getPlatform re-reads the singleton.
func getPlatform() *agentgatewayv1alpha1.AgentGatewayPlatform {
	GinkgoHelper()

	platform := &agentgatewayv1alpha1.AgentGatewayPlatform{}
	Expect(k8sClient.Get(ctx,
		types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName}, platform)).To(Succeed())
	return platform
}

// conditionStatus reads one condition's status, returning Unknown when it is
// absent so Eventually keeps polling rather than failing on a nil deref.
func conditionStatus(condType string) metav1.ConditionStatus {
	platform := &agentgatewayv1alpha1.AgentGatewayPlatform{}
	if err := k8sClient.Get(ctx,
		types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName}, platform); err != nil {
		return metav1.ConditionUnknown
	}
	cond := findCondition(platform.Status.Conditions, condType)
	if cond == nil {
		return metav1.ConditionUnknown
	}
	return cond.Status
}

func conditionReason(condType string) string {
	platform := &agentgatewayv1alpha1.AgentGatewayPlatform{}
	if err := k8sClient.Get(ctx,
		types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName}, platform); err != nil {
		return ""
	}
	cond := findCondition(platform.Status.Conditions, condType)
	if cond == nil {
		return ""
	}
	return cond.Reason
}

func conditionMessage(condType string) string {
	platform := &agentgatewayv1alpha1.AgentGatewayPlatform{}
	if err := k8sClient.Get(ctx,
		types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName}, platform); err != nil {
		return ""
	}
	cond := findCondition(platform.Status.Conditions, condType)
	if cond == nil {
		return ""
	}
	return cond.Message
}

// releaseExists reports whether a Helm release is present in the in-memory
// store.
func releaseExists(fac *helm.MemoryHelmFactory, namespace, releaseName string) error {
	hc, err := fac.For(namespace)
	if err != nil {
		return err
	}
	_, err = hc.Status(releaseName)
	return err
}

// markDeploymentAvailable stands in for the Deployment controller, which
// envtest does not run.
//
// The Deployment is created if absent: in production the chart creates it, but
// nothing in envtest applies chart manifests.
func markDeploymentAvailable(namespace, name string) {
	GinkgoHelper()

	ensureNamespaceExists(namespace)

	deploy := &appsv1.Deployment{}
	err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, deploy)
	if apierrors.IsNotFound(err) {
		deploy = &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "c", Image: "example.invalid/x:v1"}},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, deploy)).To(Succeed())
	} else {
		Expect(err).NotTo(HaveOccurred())
	}

	deploy.Status.Conditions = []appsv1.DeploymentCondition{{
		Type:               appsv1.DeploymentAvailable,
		Status:             corev1.ConditionTrue,
		Reason:             "MinimumReplicasAvailable",
		LastTransitionTime: metav1.Now(),
	}}
	Expect(k8sClient.Status().Update(ctx, deploy)).To(Succeed())
}

// createAcceptedGatewayClass stands in for agentgateway's own controller, which
// creates the GatewayClass after leader election.
//
// The operator under test must never create this itself, which is why it is a
// fixture rather than something the reconciler produces.
func createAcceptedGatewayClass() {
	GinkgoHelper()

	gwc := newGatewayClass()
	gwc.SetName(GatewayClassName)
	Expect(unstructured.SetNestedField(gwc.Object,
		"agentgateway.dev/agentgateway", "spec", "controllerName")).To(Succeed())

	if err := k8sClient.Create(ctx, gwc); err != nil {
		Expect(apierrors.IsAlreadyExists(err)).To(BeTrue())
	}

	live := newGatewayClass()
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: GatewayClassName}, live)).To(Succeed())
	Expect(unstructured.SetNestedSlice(live.Object, []any{
		map[string]any{
			"type":               "Accepted",
			"status":             string(metav1.ConditionTrue),
			"reason":             "Accepted",
			"message":            "accepted by the test fixture",
			"lastTransitionTime": metav1.Now().Format("2006-01-02T15:04:05Z"),
			"observedGeneration": int64(1),
		},
	}, "status", "conditions")).To(Succeed())
	Expect(k8sClient.Status().Update(ctx, live)).To(Succeed())
}

// markGatewayProgrammed stands in for agentgateway's controller programming the
// Gateway.
//
// Deliberately sets no addresses: readiness must not depend on them, which is
// the LoadBalancer-without-a-provider case (ADR-0005).
func markGatewayProgrammed(namespace, name string) {
	GinkgoHelper()

	gw := newGateway()
	Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, gw)).To(Succeed())
	Expect(unstructured.SetNestedSlice(gw.Object, []any{
		map[string]any{
			"type":               "Programmed",
			"status":             string(metav1.ConditionTrue),
			"reason":             "Programmed",
			"message":            "programmed by the test fixture",
			"lastTransitionTime": metav1.Now().Format("2006-01-02T15:04:05Z"),
			"observedGeneration": int64(1),
		},
		map[string]any{
			"type":               "Accepted",
			"status":             string(metav1.ConditionTrue),
			"reason":             "Accepted",
			"message":            "accepted by the test fixture",
			"lastTransitionTime": metav1.Now().Format("2006-01-02T15:04:05Z"),
			"observedGeneration": int64(1),
		},
	}, "status", "conditions")).To(Succeed())
	Expect(k8sClient.Status().Update(ctx, gw)).To(Succeed())
}

// ensureNamespaceExists creates a namespace if it is not already there.
func ensureNamespaceExists(name string) {
	GinkgoHelper()

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := k8sClient.Create(ctx, ns); err != nil {
		Expect(apierrors.IsAlreadyExists(err)).To(BeTrue())
	}
}

// cleanupGatewayObjects removes what a spec created, so the next spec starts
// clean.
//
// Namespaces are deliberately not deleted: envtest never actually removes a
// namespace (there is no namespace controller to finish it), so it would sit in
// Terminating and block re-creation for the rest of the suite.
func cleanupGatewayObjects() {
	gw := newGateway()
	gw.SetName(GatewayName)
	gw.SetNamespace(testGatewayNamespace)
	_ = k8sClient.Delete(ctx, gw)

	params := newParameters()
	params.SetName(ParametersName)
	params.SetNamespace(testGatewayNamespace)
	_ = k8sClient.Delete(ctx, params)

	policy := newPolicy()
	policy.SetName(PolicyName)
	policy.SetNamespace(testGatewayNamespace)
	_ = k8sClient.Delete(ctx, policy)

	gwc := newGatewayClass()
	gwc.SetName(GatewayClassName)
	_ = k8sClient.Delete(ctx, gwc)

	_ = k8sClient.DeleteAllOf(ctx, &appsv1.Deployment{}, client.InNamespace(testGatewayNamespace))
}

// driveToReady walks a platform declaration all the way to Ready, standing in
// for every upstream controller envtest does not run.
//
// Each call names the signal it is simulating, so a spec that fails here says
// which upstream behaviour stopped happening.
func driveToReady() {
	GinkgoHelper()

	// The agentgateway chart's control-plane Deployment, rolled out.
	markDeploymentAvailable(testGatewayNamespace, AgentgatewayReleaseName)

	// agentgateway's controller creating and accepting the GatewayClass, which
	// it does only after leader election.
	createAcceptedGatewayClass()

	// The operator creates the Gateway once the GatewayClass is Accepted.
	Eventually(func() error {
		gw := newGateway()
		return k8sClient.Get(ctx,
			types.NamespacedName{Namespace: testGatewayNamespace, Name: GatewayName}, gw)
	}, pollTimeout, pollInterval).Should(Succeed())

	// agentgateway programming the Gateway and provisioning its data plane.
	// Deliberately no addresses: readiness must not depend on them.
	markGatewayProgrammed(testGatewayNamespace, GatewayName)
	markDeploymentAvailable(testGatewayNamespace, GatewayName)

	// The rate-limit stack the operator renders, rolled out. Both Deployments:
	// readiness gates on the counter store as well as the service, since the
	// service comes up Available with no store reachable.
	for _, name := range []string{redisName, RateLimitServiceName} {
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Namespace: testGatewayNamespace, Name: name,
			}, &appsv1.Deployment{})
		}, pollTimeout, pollInterval).Should(Succeed())
		markDeploymentAvailable(testGatewayNamespace, name)
	}
}
