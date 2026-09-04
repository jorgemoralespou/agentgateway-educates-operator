package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
)

// Fixtures for the session and catalog specs.

const (
	// workshopNamespace stands in for $(workshop_namespace) — where the
	// attendee's pod actually runs, and so where the Secret has to be.
	workshopNamespace = "educates-test-w01"

	testGatewayURL = "http://agentgateway-educates.agentgateway-system.svc.cluster.local:4000"
)

// createReadyPlatform fakes a platform that has finished installing.
//
// The session reconciler only reads the platform's status, so driving a real
// install here would test the platform controller a second time.
func createReadyPlatform() {
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
	if err := k8sClient.Create(ctx, platform); err != nil {
		Expect(apierrors.IsAlreadyExists(err)).To(BeTrue())
	}

	live := &agentgatewayv1alpha1.AgentGatewayPlatform{}
	Expect(k8sClient.Get(ctx,
		types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName}, live)).To(Succeed())
	live.Status.Phase = agentgatewayv1alpha1.PlatformReady
	live.Status.GatewayURL = testGatewayURL
	live.Status.GatewayNamespace = testGatewayNamespace
	setCondition(&live.Status.Conditions, live.Generation,
		agentgatewayv1alpha1.ConditionReady, metav1.ConditionTrue,
		agentgatewayv1alpha1.ReasonReady, "ready in the test fixture")
	Expect(k8sClient.Status().Update(ctx, live)).To(Succeed())
}

// createReadyCatalog fakes a catalog that has finished rendering.
func createReadyCatalog() {
	GinkgoHelper()

	catalog := &agentgatewayv1alpha1.AgentGatewayCatalog{
		ObjectMeta: metav1.ObjectMeta{Name: agentgatewayv1alpha1.SingletonName},
		Spec: agentgatewayv1alpha1.AgentGatewayCatalogSpec{
			Models: []agentgatewayv1alpha1.CatalogModel{{
				Name:     "fast",
				Provider: agentgatewayv1alpha1.ProviderOpenAI,
				Model:    "gpt-4o-mini",
				CredentialRef: agentgatewayv1alpha1.CredentialReference{
					Name: "openai-credentials",
					Key:  "api-key",
				},
			}},
		},
	}
	if err := k8sClient.Create(ctx, catalog); err != nil {
		Expect(apierrors.IsAlreadyExists(err)).To(BeTrue())
	}

	live := &agentgatewayv1alpha1.AgentGatewayCatalog{}
	Expect(k8sClient.Get(ctx,
		types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName}, live)).To(Succeed())
	live.Status.Phase = agentgatewayv1alpha1.CatalogReady
	live.Status.GatewayURL = testGatewayURL
	live.Status.AvailableModels = []string{"fast"}
	setCondition(&live.Status.Conditions, live.Generation,
		agentgatewayv1alpha1.ConditionReady, metav1.ConditionTrue,
		agentgatewayv1alpha1.ReasonReady, "ready in the test fixture")
	Expect(k8sClient.Status().Update(ctx, live)).To(Succeed())
}

// createSession creates a grant in the workshop namespace, the way a correctly
// written workshop definition does.
func createSession(name string) *agentgatewayv1alpha1.AgentGatewaySession {
	GinkgoHelper()

	session := &agentgatewayv1alpha1.AgentGatewaySession{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: workshopNamespace},
		Spec: agentgatewayv1alpha1.AgentGatewaySessionSpec{
			CatalogRef:  agentgatewayv1alpha1.CatalogReference{Name: agentgatewayv1alpha1.SingletonName},
			TokenBudget: 100000,
			TTL:         "4h",
		},
	}
	Expect(k8sClient.Create(ctx, session)).To(Succeed())
	return session
}

// ensureSessionNamespace creates a session namespace owned by a WorkshopSession,
// which is how Educates marks it and how this operator recognises one.
func ensureSessionNamespace(name string) *corev1.Namespace {
	GinkgoHelper()

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "training.educates.dev/v1beta1",
				Kind:       "WorkshopSession",
				Name:       name,
				// A real cluster supplies a real UID; only the Kind is read.
				UID: "00000000-0000-0000-0000-000000000001",
			}},
		},
	}
	if err := k8sClient.Create(ctx, ns); err != nil {
		Expect(apierrors.IsAlreadyExists(err)).To(BeTrue())
	}

	live := &corev1.Namespace{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, live)).To(Succeed())
	return live
}

func sessionCondition(session *agentgatewayv1alpha1.AgentGatewaySession, condType string) metav1.ConditionStatus {
	live := &agentgatewayv1alpha1.AgentGatewaySession{}
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: session.Namespace, Name: session.Name,
	}, live); err != nil {
		return metav1.ConditionUnknown
	}
	cond := findCondition(live.Status.Conditions, condType)
	if cond == nil {
		return metav1.ConditionUnknown
	}
	return cond.Status
}

func sessionConditionReason(session *agentgatewayv1alpha1.AgentGatewaySession, condType string) string {
	live := &agentgatewayv1alpha1.AgentGatewaySession{}
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: session.Namespace, Name: session.Name,
	}, live); err != nil {
		return ""
	}
	cond := findCondition(live.Status.Conditions, condType)
	if cond == nil {
		return ""
	}
	return cond.Reason
}

func sessionConditionMessage(session *agentgatewayv1alpha1.AgentGatewaySession, condType string) string {
	live := &agentgatewayv1alpha1.AgentGatewaySession{}
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: session.Namespace, Name: session.Name,
	}, live); err != nil {
		return ""
	}
	cond := findCondition(live.Status.Conditions, condType)
	if cond == nil {
		return ""
	}
	return cond.Message
}

// readKey returns the participant key from a session's Secret.
func readKey(sessionName string) string {
	GinkgoHelper()

	secret := &corev1.Secret{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{
		Namespace: workshopNamespace, Name: sessionName + agentgatewayv1alpha1.SecretSuffix,
	}, secret)).To(Succeed())
	return string(secret.Data[agentgatewayv1alpha1.SecretKeyAPIKey])
}

// readKeyOrEmpty is readKey for use inside Eventually, where a missing Secret
// is a reason to keep polling rather than to fail.
func readKeyOrEmpty(sessionName string) string {
	secret := &corev1.Secret{}
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: workshopNamespace, Name: sessionName + agentgatewayv1alpha1.SecretSuffix,
	}, secret); err != nil {
		return ""
	}
	return string(secret.Data[agentgatewayv1alpha1.SecretKeyAPIKey])
}

// cleanupSessions clears session state between specs.
//
// Finalizers are stripped first: the manager that would drain them is already
// stopped, so a plain delete would hang.
func cleanupSessions() {
	sessions := &agentgatewayv1alpha1.AgentGatewaySessionList{}
	if err := k8sClient.List(ctx, sessions); err == nil {
		for i := range sessions.Items {
			s := &sessions.Items[i]
			if len(s.Finalizers) > 0 {
				s.Finalizers = nil
				_ = k8sClient.Update(ctx, s)
			}
			_ = k8sClient.Delete(ctx, s)
		}
	}

	_ = k8sClient.DeleteAllOf(ctx, &corev1.Secret{}, client.InNamespace(workshopNamespace))
	_ = k8sClient.DeleteAllOf(ctx, &corev1.ConfigMap{},
		client.InNamespace(testGatewayNamespace),
		client.MatchingLabels{agentgatewayv1alpha1.RegistrationLabel: "true"})

	_ = k8sClient.DeleteAllOf(ctx, &agentgatewayv1alpha1.AgentGatewayCatalog{})

	platform := &agentgatewayv1alpha1.AgentGatewayPlatform{}
	if err := k8sClient.Get(ctx,
		types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName}, platform); err == nil {
		platform.Finalizers = nil
		_ = k8sClient.Update(ctx, platform)
		_ = k8sClient.Delete(ctx, platform)
	}
}
