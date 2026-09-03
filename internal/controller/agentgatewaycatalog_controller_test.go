package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	crconfig "sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
)

var _ = Describe("AgentGatewayCatalog reconciler", func() {
	var (
		mgrCancel context.CancelFunc
		mgrDone   chan error
	)

	BeforeEach(func() {
		ensureNamespaceExists(testGatewayNamespace)

		var mgrCtx context.Context
		mgrCtx, mgrCancel = context.WithCancel(ctx)

		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:     k8sClient.Scheme(),
			Metrics:    metricsserver.Options{BindAddress: "0"},
			Controller: crconfig.Controller{SkipNameValidation: ptrTo(true)},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect((&AgentGatewayCatalogReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
			// No discovery client: the suite serves agentgateway's CRDs from
			// the start, so the reconciler falls back to the REST mapper, which
			// resolves them.
		}).SetupWithManager(mgr)).To(Succeed())

		mgrDone = make(chan error, 1)
		go func() {
			defer GinkgoRecover()
			mgrDone <- mgr.Start(mgrCtx)
		}()
	})

	AfterEach(func() {
		mgrCancel()
		Eventually(mgrDone, 10*time.Second).Should(Receive())
		cleanupCatalog()
	})

	Describe("gating on the platform", func() {
		// The catalog configures a Gateway and never installs one, so there is
		// nothing to render until the platform is serving.
		It("reports not-ready when no platform exists", func() {
			createCatalog()

			Eventually(func() string {
				return catalogConditionReason(agentgatewayv1alpha1.ConditionPlatformReady)
			}, pollTimeout, pollInterval).Should(Equal(agentgatewayv1alpha1.ReasonNotFound))

			catalog := getCatalog()
			Expect(catalog.Status.Phase).To(Equal(agentgatewayv1alpha1.CatalogPending))

			// A missing platform is "not ready yet", never an error, so Ready
			// is False rather than the catalog failing outright.
			Expect(catalogCondition(agentgatewayv1alpha1.ConditionReady)).
				To(Equal(metav1.ConditionFalse))
		})

		It("waits while the platform exists but is not ready", func() {
			createNotReadyPlatform()
			createCatalog()

			Eventually(func() string {
				return catalogConditionReason(agentgatewayv1alpha1.ConditionPlatformReady)
			}, pollTimeout, pollInterval).Should(Equal(agentgatewayv1alpha1.ReasonWaiting))

			Consistently(func() metav1.ConditionStatus {
				return catalogCondition(agentgatewayv1alpha1.ConditionReady)
			}, 2*time.Second, pollInterval).ShouldNot(Equal(metav1.ConditionTrue))
		})

		It("reads the gateway address from the platform's status rather than reconstructing it", func() {
			// A deliberately unusual address: anything the catalog derived
			// itself would not match this.
			const publishedURL = "http://somewhere-else.example.svc.cluster.local:9999"

			createReadyPlatformWithURL(publishedURL)
			createCatalog()

			Eventually(func() string {
				return getCatalog().Status.GatewayURL
			}, pollTimeout, pollInterval).Should(Equal(publishedURL))
		})
	})

	Describe("rendering models", func() {
		BeforeEach(func() {
			createReadyPlatform()
		})

		It("renders one internal model plus one alias per entry", func() {
			createCatalog()

			Eventually(func() metav1.ConditionStatus {
				return catalogCondition(agentgatewayv1alpha1.ConditionReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			// The alias carries the catalog name, which is what an attendee
			// addresses.
			alias := newModel()
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: testGatewayNamespace, Name: "fast",
			}, alias)).To(Succeed())

			// The concrete model carries the provider and the upstream name.
			internal := newModel()
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: testGatewayNamespace, Name: "fast-upstream",
			}, internal)).To(Succeed())
		})

		It("hides the provider and upstream model behind the alias", func() {
			createCatalog()

			Eventually(func() metav1.ConditionStatus {
				return catalogCondition(agentgatewayv1alpha1.ConditionReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			internal := newModel()
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: testGatewayNamespace, Name: "fast-upstream",
			}, internal)).To(Succeed())

			// Internal means only a virtual model can select it, so an attendee
			// cannot address `gpt-4o-mini` directly.
			visibility, _, _ := unstructured.NestedString(internal.Object, "spec", "visibility")
			Expect(visibility).To(Equal("Internal"))

			provider, _, _ := unstructured.NestedString(internal.Object, "spec", "provider")
			Expect(provider).To(Equal("OpenAI"),
				"the provider enum is agentgateway's casing, not ours")

			upstream, _, _ := unstructured.NestedString(internal.Object, "spec", "match", "model")
			Expect(upstream).To(Equal("gpt-4o-mini"))

			alias := newModel()
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: testGatewayNamespace, Name: "fast",
			}, alias)).To(Succeed())

			// A virtual model must be Public; agentgateway rejects Internal.
			aliasVisibility, _, _ := unstructured.NestedString(alias.Object, "spec", "visibility")
			Expect(aliasVisibility).To(Equal("Public"))

			// It matches the catalog name exactly, with no wildcard.
			aliasMatch, _, _ := unstructured.NestedString(alias.Object, "spec", "match", "model")
			Expect(aliasMatch).To(Equal("fast"))

			// And it points at the internal model.
			targets, found, err := unstructured.NestedSlice(alias.Object,
				"spec", "virtualModel", "weighted", "targets")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(targets).To(HaveLen(1))

			target := targets[0].(map[string]any)
			modelRef := target["modelRef"].(map[string]any)
			Expect(modelRef["name"]).To(Equal("fast-upstream"))
		})

		It("puts the credential on the concrete model and never on the alias", func() {
			createCatalog()

			Eventually(func() metav1.ConditionStatus {
				return catalogCondition(agentgatewayv1alpha1.ConditionReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			internal := newModel()
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: testGatewayNamespace, Name: "fast-upstream",
			}, internal)).To(Succeed())

			// Referenced by Secret name only: no credential material appears in
			// any custom resource, so a catalog stays safe to commit.
			secretName, found, err := unstructured.NestedString(internal.Object,
				"spec", "policies", "auth", "secretRef", "name")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(secretName).To(Equal("openai-credentials"))

			alias := newModel()
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: testGatewayNamespace, Name: "fast",
			}, alias)).To(Succeed())

			// agentgateway's CEL forbids policies alongside virtualModel, so an
			// alias carrying one would be rejected outright.
			_, hasPolicies, err := unstructured.NestedMap(alias.Object, "spec", "policies")
			Expect(err).NotTo(HaveOccurred())
			Expect(hasPolicies).To(BeFalse(),
				"a virtual model must not carry a policies block")
		})

		It("publishes the catalog names an author may reference", func() {
			createCatalogWithModels(
				agentgatewayv1alpha1.CatalogModel{
					Name:     "fast",
					Provider: agentgatewayv1alpha1.ProviderOpenAI,
					Model:    "gpt-4o-mini",
					CredentialRef: agentgatewayv1alpha1.CredentialReference{
						Name: "openai-credentials", Key: "api-key",
					},
				},
				agentgatewayv1alpha1.CatalogModel{
					Name:     "smart",
					Provider: agentgatewayv1alpha1.ProviderAnthropic,
					Model:    "claude-sonnet-4-0",
					CredentialRef: agentgatewayv1alpha1.CredentialReference{
						Name: "anthropic-credentials", Key: "api-key",
					},
				},
			)

			Eventually(func() []string {
				return getCatalog().Status.AvailableModels
			}, pollTimeout, pollInterval).Should(ConsistOf("fast", "smart"))

			// Both providers render, with agentgateway's own casing.
			internal := newModel()
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: testGatewayNamespace, Name: "smart-upstream",
			}, internal)).To(Succeed())
			provider, _, _ := unstructured.NestedString(internal.Object, "spec", "provider")
			Expect(provider).To(Equal("Anthropic"))
		})

		It("rebinds a name to a different upstream model without touching the alias", func() {
			createCatalog()

			Eventually(func() metav1.ConditionStatus {
				return catalogCondition(agentgatewayv1alpha1.ConditionReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			// A cluster operator changes what sits behind `fast`.
			live := getCatalog()
			live.Spec.Models[0].Model = "gpt-4o"
			Expect(k8sClient.Update(ctx, live)).To(Succeed())

			Eventually(func() string {
				internal := newModel()
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Namespace: testGatewayNamespace, Name: "fast-upstream",
				}, internal); err != nil {
					return ""
				}
				upstream, _, _ := unstructured.NestedString(internal.Object, "spec", "match", "model")
				return upstream
			}, pollTimeout, pollInterval).Should(Equal("gpt-4o"))

			// The alias is unchanged, which is what lets workshop content keep
			// working across a rebind.
			alias := newModel()
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: testGatewayNamespace, Name: "fast",
			}, alias)).To(Succeed())
			aliasMatch, _, _ := unstructured.NestedString(alias.Object, "spec", "match", "model")
			Expect(aliasMatch).To(Equal("fast"))
		})
	})
})
