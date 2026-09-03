package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
)

// The ADR-0002 teardown path, which is the one thing envtest cannot exercise.
//
// Two halves of a session's credential live in two namespaces with different
// lifecycles. The Secret is collected by the garbage collector because it is
// owned by the session namespace; the registration is not owned by anything, so
// only the finalizer removes it. Both must actually happen when a session ends,
// or a finished workshop leaves a live credential or a wedged namespace behind.
var _ = Describe("session teardown on a real cluster", Ordered, func() {
	const (
		workshopNS  = "e2e-workshop"
		sessionNS   = "e2e-session"
		sessionName = "e2e-session"
	)

	var gatewayNamespace string

	BeforeAll(func() {
		// The gateway namespace comes from the running platform, so this test
		// exercises whatever the operator actually installed.
		platform := &agentgatewayv1alpha1.AgentGatewayPlatform{}
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName}, platform)).To(Succeed(),
			"this test needs a ready AgentGatewayPlatform; apply one before running it")

		gatewayNamespace = platform.Status.GatewayNamespace
		Expect(gatewayNamespace).NotTo(BeEmpty(),
			"the platform has not published its gateway namespace yet")

		createNamespace(workshopNS, nil)

		// A stand-in for the session namespace Educates creates. Nothing owns
		// it here, but the grant's Secret will be owned by it, which is the
		// relationship under test.
		createNamespace(sessionNS, nil)
	})

	AfterAll(func() {
		_ = k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: workshopNS}})
		_ = k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: sessionNS}})
	})

	It("collects the Secret and removes the registration when the session namespace goes", func() {
		By("creating a session grant in the workshop namespace")
		session := &agentgatewayv1alpha1.AgentGatewaySession{
			ObjectMeta: metav1.ObjectMeta{Name: sessionName, Namespace: workshopNS},
			Spec: agentgatewayv1alpha1.AgentGatewaySessionSpec{
				CatalogRef:  agentgatewayv1alpha1.CatalogReference{Name: agentgatewayv1alpha1.SingletonName},
				TokenBudget: 1000,
				TTL:         "1h",
			},
		}
		Expect(k8sClient.Create(ctx, session)).To(Succeed())

		By("waiting for the key to be minted")
		Eventually(func() metav1.ConditionStatus {
			live := &agentgatewayv1alpha1.AgentGatewaySession{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Namespace: workshopNS, Name: sessionName,
			}, live); err != nil {
				return metav1.ConditionUnknown
			}
			for _, c := range live.Status.Conditions {
				if c.Type == agentgatewayv1alpha1.ConditionReady {
					return c.Status
				}
			}
			return metav1.ConditionUnknown
		}, e2eTimeout, e2eInterval).Should(Equal(metav1.ConditionTrue))

		secretName := sessionName + agentgatewayv1alpha1.SecretSuffix

		By("confirming both halves exist, in their two namespaces")
		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Namespace: workshopNS, Name: secretName,
		}, secret)).To(Succeed())

		// The owner reference is what the garbage collector will act on. If it
		// is missing, the rest of this test proves nothing.
		Expect(secret.OwnerReferences).To(HaveLen(1))
		Expect(secret.OwnerReferences[0].Kind).To(Equal("Namespace"))
		Expect(secret.OwnerReferences[0].Name).To(Equal(sessionNS))

		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Namespace: gatewayNamespace, Name: secretName,
		}, &corev1.ConfigMap{})).To(Succeed())

		By("deleting the session namespace, as Educates does when a session ends")
		Expect(k8sClient.Delete(ctx,
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: sessionNS}})).To(Succeed())

		// This is the assertion envtest cannot make: a real garbage collector
		// following an owner reference across namespaces.
		By("waiting for the garbage collector to collect the Secret")
		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{
				Namespace: workshopNS, Name: secretName,
			}, &corev1.Secret{})
			return apierrors.IsNotFound(err)
		}, e2eTimeout, e2eInterval).Should(BeTrue(),
			"the participant key Secret must be collected with the session namespace")

		By("deleting the grant, so the finalizer runs")
		live := &agentgatewayv1alpha1.AgentGatewaySession{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Namespace: workshopNS, Name: sessionName,
		}, live)).To(Succeed())
		Expect(k8sClient.Delete(ctx, live)).To(Succeed())

		By("waiting for the finalizer to remove the registration")
		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{
				Namespace: gatewayNamespace, Name: secretName,
			}, &corev1.ConfigMap{})
			return apierrors.IsNotFound(err)
		}, e2eTimeout, e2eInterval).Should(BeTrue(),
			"the registration lives in a namespace nothing collects, so only the finalizer removes it")

		By("confirming the grant itself is gone rather than stuck in Terminating")
		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{
				Namespace: workshopNS, Name: sessionName,
			}, &agentgatewayv1alpha1.AgentGatewaySession{})
			return apierrors.IsNotFound(err)
		}, e2eTimeout, e2eInterval).Should(BeTrue(),
			"a finalizer that never releases would hold the namespace in Terminating forever")
	})
})

func createNamespace(name string, labels map[string]string) {
	GinkgoHelper()

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
	}
	if err := k8sClient.Create(ctx, ns); err != nil {
		Expect(apierrors.IsAlreadyExists(err)).To(BeTrue())
	}
}
