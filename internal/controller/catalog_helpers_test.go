package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
)

// Fixtures for the catalog specs.

// defaultCatalogModel is the single-entry catalog most specs use.
func defaultCatalogModel() agentgatewayv1alpha1.CatalogModel {
	return agentgatewayv1alpha1.CatalogModel{
		Name:     "fast",
		Provider: agentgatewayv1alpha1.ProviderOpenAI,
		Model:    "gpt-4o-mini",
		CredentialRef: agentgatewayv1alpha1.CredentialReference{
			Name: "openai-credentials",
			Key:  "api-key",
		},
	}
}

func createCatalog() *agentgatewayv1alpha1.AgentGatewayCatalog {
	GinkgoHelper()
	return createCatalogWithModels(defaultCatalogModel())
}

func createCatalogWithModels(models ...agentgatewayv1alpha1.CatalogModel) *agentgatewayv1alpha1.AgentGatewayCatalog {
	GinkgoHelper()

	catalog := &agentgatewayv1alpha1.AgentGatewayCatalog{
		ObjectMeta: metav1.ObjectMeta{Name: agentgatewayv1alpha1.SingletonName},
		Spec:       agentgatewayv1alpha1.AgentGatewayCatalogSpec{Models: models},
	}
	Expect(k8sClient.Create(ctx, catalog)).To(Succeed())
	return catalog
}

func getCatalog() *agentgatewayv1alpha1.AgentGatewayCatalog {
	GinkgoHelper()

	catalog := &agentgatewayv1alpha1.AgentGatewayCatalog{}
	Expect(k8sClient.Get(ctx,
		types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName}, catalog)).To(Succeed())
	return catalog
}

func catalogCondition(condType string) metav1.ConditionStatus {
	catalog := &agentgatewayv1alpha1.AgentGatewayCatalog{}
	if err := k8sClient.Get(ctx,
		types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName}, catalog); err != nil {
		return metav1.ConditionUnknown
	}
	cond := findCondition(catalog.Status.Conditions, condType)
	if cond == nil {
		return metav1.ConditionUnknown
	}
	return cond.Status
}

func catalogConditionReason(condType string) string {
	catalog := &agentgatewayv1alpha1.AgentGatewayCatalog{}
	if err := k8sClient.Get(ctx,
		types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName}, catalog); err != nil {
		return ""
	}
	cond := findCondition(catalog.Status.Conditions, condType)
	if cond == nil {
		return ""
	}
	return cond.Reason
}

// createNotReadyPlatform creates a platform that exists but has not finished
// installing.
func createNotReadyPlatform() {
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
	live.Status.Phase = agentgatewayv1alpha1.PlatformInstalling
	setCondition(&live.Status.Conditions, live.Generation,
		agentgatewayv1alpha1.ConditionReady, metav1.ConditionFalse,
		agentgatewayv1alpha1.ReasonInstalling, "still installing in the test fixture")
	Expect(k8sClient.Status().Update(ctx, live)).To(Succeed())
}

// createReadyPlatformWithURL is createReadyPlatform with a chosen address, so a
// spec can prove the catalog reads it rather than deriving one.
func createReadyPlatformWithURL(gatewayURL string) {
	GinkgoHelper()

	createReadyPlatform()

	live := &agentgatewayv1alpha1.AgentGatewayPlatform{}
	Expect(k8sClient.Get(ctx,
		types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName}, live)).To(Succeed())
	live.Status.GatewayURL = gatewayURL
	Expect(k8sClient.Status().Update(ctx, live)).To(Succeed())
}

// cleanupCatalog clears catalog state between specs.
func cleanupCatalog() {
	_ = k8sClient.DeleteAllOf(ctx, &agentgatewayv1alpha1.AgentGatewayCatalog{})

	for _, name := range []string{"fast", "fast-upstream", "smart", "smart-upstream"} {
		model := newModel()
		model.SetName(name)
		model.SetNamespace(testGatewayNamespace)
		_ = k8sClient.Delete(ctx, model)
	}

	platform := &agentgatewayv1alpha1.AgentGatewayPlatform{}
	if err := k8sClient.Get(ctx,
		types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName}, platform); err == nil {
		platform.Finalizers = nil
		_ = k8sClient.Update(ctx, platform)
		_ = k8sClient.Delete(ctx, platform)
	}

}
