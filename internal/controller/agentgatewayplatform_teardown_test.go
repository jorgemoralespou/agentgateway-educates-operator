package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	crconfig "sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
	"github.com/educates/agentgateway-educates-operator/internal/helm"
	vendoredcharts "github.com/educates/agentgateway-educates-operator/vendored-charts"
	releasecommon "helm.sh/helm/v4/pkg/release/common"
)

// Ticket 04: teardown and provider switching.
//
// Both are things a cluster operator does deliberately and rarely, which is
// exactly when a silent failure goes unnoticed until it matters.
var _ = Describe("AgentGatewayPlatform teardown and provider switching", func() {
	var (
		mgrCancel context.CancelFunc
		mgrDone   chan error
		helmFac   *helm.MemoryHelmFactory
		namespace string
	)

	// Each spec gets its own gateway namespace.
	//
	// Teardown deletes the namespace, and envtest runs no namespace controller
	// to finish the job, so a shared one would sit in Terminating and every
	// later spec would fail to create anything in it.
	BeforeEach(func() {
		helmFac = helm.NewMemoryHelmFactory()
		namespace = uniqueGatewayNamespace()

		var mgrCtx context.Context
		mgrCtx, mgrCancel = context.WithCancel(ctx)

		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:     k8sClient.Scheme(),
			Metrics:    metricsserver.Options{BindAddress: "0"},
			Controller: crconfig.Controller{SkipNameValidation: ptrTo(true)},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect((&AgentGatewayPlatformReconciler{
			Client:        mgr.GetClient(),
			Scheme:        mgr.GetScheme(),
			HelmClientFor: helmFac.For,
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

		platform := &agentgatewayv1alpha1.AgentGatewayPlatform{}
		if err := k8sClient.Get(ctx,
			types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName}, platform); err == nil {
			platform.Finalizers = nil
			_ = k8sClient.Update(ctx, platform)
			_ = k8sClient.Delete(ctx, platform)
		}
		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName},
				&agentgatewayv1alpha1.AgentGatewayPlatform{})
			return apierrors.IsNotFound(err)
		}, pollTimeout, pollInterval).Should(BeTrue())

		cleanupGatewayObjectsIn(namespace)
	})

	Describe("deleting the platform declaration", func() {
		It("uninstalls the releases and releases the finalizer", func() {
			createPlatformIn(namespace)
			driveToReadyIn(namespace)

			Eventually(func() metav1.ConditionStatus {
				return conditionStatus(agentgatewayv1alpha1.ConditionReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			hc, err := helmFac.For(namespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(hc.Status(AgentgatewayReleaseName)).Error().NotTo(HaveOccurred())

			platform := getPlatform()
			Expect(k8sClient.Delete(ctx, platform)).To(Succeed())

			// Both releases go, not just the one.
			Eventually(func() error {
				_, err := hc.Status(AgentgatewayReleaseName)
				return err
			}, pollTimeout, pollInterval).Should(MatchError(helm.ErrReleaseNotFound))

			Eventually(func() error {
				_, err := hc.Status(AgentgatewayCRDsReleaseName)
				return err
			}, pollTimeout, pollInterval).Should(MatchError(helm.ErrReleaseNotFound))

			// And the declaration itself is gone rather than stuck behind its
			// own finalizer.
			Eventually(func() bool {
				err := k8sClient.Get(ctx,
					types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName},
					&agentgatewayv1alpha1.AgentGatewayPlatform{})
				return apierrors.IsNotFound(err)
			}, pollTimeout, pollInterval).Should(BeTrue())
		})

		It("removes the Gateway and the policy, so nothing is left configuring a gateway that is going away", func() {
			createPlatformIn(namespace)
			driveToReadyIn(namespace)

			Eventually(func() metav1.ConditionStatus {
				return conditionStatus(agentgatewayv1alpha1.ConditionReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			// The policy is rendered as the last install step.
			Eventually(func() error {
				policy := newPolicy()
				return k8sClient.Get(ctx,
					types.NamespacedName{Namespace: namespace, Name: PolicyName}, policy)
			}, pollTimeout, pollInterval).Should(Succeed())

			platform := getPlatform()
			Expect(k8sClient.Delete(ctx, platform)).To(Succeed())

			Eventually(func() bool {
				policy := newPolicy()
				err := k8sClient.Get(ctx,
					types.NamespacedName{Namespace: namespace, Name: PolicyName}, policy)
				return apierrors.IsNotFound(err)
			}, pollTimeout, pollInterval).Should(BeTrue())

			Eventually(func() bool {
				gw := newGateway()
				err := k8sClient.Get(ctx,
					types.NamespacedName{Namespace: namespace, Name: GatewayName}, gw)
				return apierrors.IsNotFound(err)
			}, pollTimeout, pollInterval).Should(BeTrue())
		})

		// Models are the one kind here with no fixed name, so they are the one
		// teardown could quietly skip. They also carry no ownerReferences, so
		// nothing collects them: left behind, they outlive the Gateway they
		// were parented to and a later platform in the same namespace inherits
		// models from a catalog that may since have changed.
		It("deletes the models the catalog rendered", func() {
			createPlatformIn(namespace)
			driveToReadyIn(namespace)

			Eventually(func() metav1.ConditionStatus {
				return conditionStatus(agentgatewayv1alpha1.ConditionReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			// A rendered pair, as the catalog controller would have written it.
			for _, name := range []string{"fast", "fast-upstream"} {
				model := newModel()
				model.SetName(name)
				model.SetNamespace(namespace)
				model.SetLabels(map[string]string{ManagedByLabel: ManagedByValue})
				Expect(unstructured.SetNestedMap(model.Object, map[string]any{
					"parentRefs": []any{
						map[string]any{
							"group": GatewayAPIGroup,
							"kind":  KindGateway,
							"name":  GatewayName,
						},
					},
					"provider":   "OpenAI",
					"visibility": "Public",
					"match":      map[string]any{"model": name},
				}, "spec")).To(Succeed())
				Expect(k8sClient.Create(ctx, model)).To(Succeed())
			}

			platform := getPlatform()
			Expect(k8sClient.Delete(ctx, platform)).To(Succeed())

			for _, name := range []string{"fast", "fast-upstream"} {
				Eventually(func() bool {
					err := k8sClient.Get(ctx,
						types.NamespacedName{Namespace: namespace, Name: name}, newModel())
					return apierrors.IsNotFound(err)
				}, pollTimeout, pollInterval).Should(BeTrue(),
					"%s must not outlive the platform", name)
			}
		})

		It("leaves a model it did not render", func() {
			createPlatformIn(namespace)
			driveToReadyIn(namespace)

			Eventually(func() metav1.ConditionStatus {
				return conditionStatus(agentgatewayv1alpha1.ConditionReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			// No managed-by label: a cluster operator's own.
			foreign := newModel()
			foreign.SetName("hand-written")
			foreign.SetNamespace(namespace)
			Expect(unstructured.SetNestedMap(foreign.Object, map[string]any{
				"parentRefs": []any{
					map[string]any{
						"group": GatewayAPIGroup,
						"kind":  KindGateway,
						"name":  GatewayName,
					},
				},
				"provider":   "OpenAI",
				"visibility": "Public",
				"match":      map[string]any{"model": "hand-written"},
			}, "spec")).To(Succeed())
			Expect(k8sClient.Create(ctx, foreign)).To(Succeed())

			platform := getPlatform()
			Expect(k8sClient.Delete(ctx, platform)).To(Succeed())

			Eventually(func() bool {
				err := k8sClient.Get(ctx,
					types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName},
					&agentgatewayv1alpha1.AgentGatewayPlatform{})
				return apierrors.IsNotFound(err)
			}, pollTimeout, pollInterval).Should(BeTrue())

			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Namespace: namespace, Name: "hand-written"},
				newModel())).To(Succeed(),
				"a model without this operator's managed-by label must survive teardown")
		})

		// Teardown is retried, so it must be safe to run against a cluster where
		// some of it has already happened.
		It("is idempotent: a drain with nothing left to remove still completes", func() {
			createPlatformIn(namespace)
			driveToReadyIn(namespace)

			Eventually(func() metav1.ConditionStatus {
				return conditionStatus(agentgatewayv1alpha1.ConditionReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			// Remove the releases out from under the operator, as a
			// partially-completed earlier drain would have.
			hc, err := helmFac.For(namespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(hc.Uninstall(AgentgatewayReleaseName)).To(Succeed())
			Expect(hc.Uninstall(AgentgatewayCRDsReleaseName)).To(Succeed())

			platform := getPlatform()
			Expect(k8sClient.Delete(ctx, platform)).To(Succeed())

			// The drain must still finish rather than erroring on what is
			// already gone.
			Eventually(func() bool {
				err := k8sClient.Get(ctx,
					types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName},
					&agentgatewayv1alpha1.AgentGatewayPlatform{})
				return apierrors.IsNotFound(err)
			}, pollTimeout, pollInterval).Should(BeTrue())
		})
	})

	Describe("switching the provider to external", func() {
		// v4's cluster-services path orphans a release when its provider is
		// switched away; its platform-extras path drains. This operator drains,
		// which is what "disabling something actually removes it" means.
		It("uninstalls the bundled release rather than orphaning it", func() {
			createPlatformIn(namespace)
			driveToReadyIn(namespace)

			Eventually(func() metav1.ConditionStatus {
				return conditionStatus(agentgatewayv1alpha1.ConditionReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			hc, err := helmFac.For(namespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(hc.Status(AgentgatewayReleaseName)).Error().NotTo(HaveOccurred())

			// The cluster operator points the platform at a gateway they run.
			platform := getPlatform()
			platform.Spec.Provider = agentgatewayv1alpha1.ProviderExternal
			platform.Spec.ExternalAgentgateway = &agentgatewayv1alpha1.ExternalAgentgatewaySpec{
				Namespace:   namespace,
				GatewayName: "someone-elses-gateway",
			}
			Expect(k8sClient.Update(ctx, platform)).To(Succeed())

			Eventually(func() error {
				_, err := hc.Status(AgentgatewayReleaseName)
				return err
			}, pollTimeout, pollInterval).Should(MatchError(helm.ErrReleaseNotFound),
				"switching to external must drain the bundled release, not orphan it")

			Eventually(func() error {
				_, err := hc.Status(AgentgatewayCRDsReleaseName)
				return err
			}, pollTimeout, pollInterval).Should(MatchError(helm.ErrReleaseNotFound))
		})

		It("installs nothing at all when the provider is external from the start", func() {
			platform := &agentgatewayv1alpha1.AgentGatewayPlatform{
				ObjectMeta: metav1.ObjectMeta{Name: agentgatewayv1alpha1.SingletonName},
				Spec: agentgatewayv1alpha1.AgentGatewayPlatformSpec{
					Provider: agentgatewayv1alpha1.ProviderExternal,
					ExternalAgentgateway: &agentgatewayv1alpha1.ExternalAgentgatewaySpec{
						Namespace:   namespace,
						GatewayName: "someone-elses-gateway",
					},
				},
			}
			Expect(k8sClient.Create(ctx, platform)).To(Succeed())

			// It should be waiting on the referenced Gateway, which does not
			// exist, and must never have installed anything of its own.
			Eventually(func() metav1.ConditionStatus {
				return conditionStatus(agentgatewayv1alpha1.ConditionControlPlaneReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			hc, err := helmFac.For(namespace)
			Expect(err).NotTo(HaveOccurred())

			Consistently(func() error {
				_, err := hc.Status(AgentgatewayReleaseName)
				return err
			}, 2*time.Second, pollInterval).Should(MatchError(helm.ErrReleaseNotFound),
				"an external provider must install nothing")
		})

		It("reports a clear failure when the external provider has no reference", func() {
			platform := &agentgatewayv1alpha1.AgentGatewayPlatform{
				ObjectMeta: metav1.ObjectMeta{Name: agentgatewayv1alpha1.SingletonName},
				Spec: agentgatewayv1alpha1.AgentGatewayPlatformSpec{
					Provider: agentgatewayv1alpha1.ProviderExternal,
				},
			}
			Expect(k8sClient.Create(ctx, platform)).To(Succeed())

			Eventually(func() string {
				return conditionMessage(agentgatewayv1alpha1.ConditionReady)
			}, pollTimeout, pollInterval).Should(ContainSubstring("externalAgentgateway"))

			Expect(getPlatform().Status.Phase).To(Equal(agentgatewayv1alpha1.PlatformFailed))
		})
	})

	Describe("tolerating a partially dismantled cluster", func() {
		// The Educates installer's teardown guard hardcodes the three platform
		// component kinds and will not wait for this operator's resources, so
		// cluster services, and the CRDs behind these kinds, can be removed
		// while this operator is still draining.
		It("completes teardown when the objects it would delete are already gone", func() {
			createPlatformIn(namespace)
			driveToReadyIn(namespace)

			Eventually(func() metav1.ConditionStatus {
				return conditionStatus(agentgatewayv1alpha1.ConditionReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			// Delete the Gateway, policy and parameters out from under the
			// operator, as a teardown that ran ahead of it would have.
			cleanupGatewayObjectsIn(namespace)

			platform := getPlatform()
			Expect(k8sClient.Delete(ctx, platform)).To(Succeed())

			Eventually(func() bool {
				err := k8sClient.Get(ctx,
					types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName},
					&agentgatewayv1alpha1.AgentGatewayPlatform{})
				return apierrors.IsNotFound(err)
			}, pollTimeout, pollInterval).Should(BeTrue(),
				"a delete of something already gone must not block teardown")
		})

		// A release left in a failed state must not be treated as ours to
		// converge, nor block a drain.
		It("drains a release that is in a failed state", func() {
			hc, err := helmFac.For(namespace)
			Expect(err).NotTo(HaveOccurred())

			chrt, err := vendoredcharts.AgentgatewayCRDs()
			Expect(err).NotTo(HaveOccurred())
			Expect(hc.SeedRelease(AgentgatewayCRDsReleaseName, 1,
				releasecommon.StatusFailed, chrt, map[string]any{},
				map[string]string{helm.OwnerLabel: helm.OwnerValue})).To(Succeed())

			createPlatformIn(namespace)

			// Held rather than churned, because the inputs have not changed.
			Eventually(func() string {
				return conditionReason(agentgatewayv1alpha1.ConditionControlPlaneReady)
			}, pollTimeout, pollInterval).Should(Equal(agentgatewayv1alpha1.ReasonFailed))

			platform := getPlatform()
			Expect(k8sClient.Delete(ctx, platform)).To(Succeed())

			Eventually(func() bool {
				err := k8sClient.Get(ctx,
					types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName},
					&agentgatewayv1alpha1.AgentGatewayPlatform{})
				return apierrors.IsNotFound(err)
			}, pollTimeout, pollInterval).Should(BeTrue())
		})
	})
})
