package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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

const (
	testGatewayNamespace = "agentgateway-system"
	pollTimeout          = 30 * time.Second
	pollInterval         = 200 * time.Millisecond
)

var _ = Describe("AgentGatewayPlatform reconciler", func() {
	var (
		mgrCancel context.CancelFunc
		mgrDone   chan error
		helmFac   *helm.MemoryHelmFactory
	)

	BeforeEach(func() {
		helmFac = helm.NewMemoryHelmFactory()

		var mgrCtx context.Context
		mgrCtx, mgrCancel = context.WithCancel(ctx)

		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:  k8sClient.Scheme(),
			Metrics: metricsserver.Options{BindAddress: "0"},
			// Each spec starts a fresh manager, and controller-runtime
			// otherwise rejects a second controller with the same name.
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

		// Drop the finalizer by hand: the manager that would drain it is gone,
		// so a plain Delete would block forever.
		platform := &agentgatewayv1alpha1.AgentGatewayPlatform{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName}, platform); err == nil {
			platform.Finalizers = nil
			_ = k8sClient.Update(ctx, platform)
			_ = k8sClient.Delete(ctx, platform)
		}
		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName},
				&agentgatewayv1alpha1.AgentGatewayPlatform{})
			return apierrors.IsNotFound(err)
		}, pollTimeout, pollInterval).Should(BeTrue())

		cleanupGatewayObjects()
	})

	Describe("the ordered install", func() {
		It("installs the charts in order, each condition flipping as its step completes", func() {
			createPlatform()

			// The CRDs chart must land before the control plane: agentgateway
			// comes up but never becomes ready without its CRDs, and that
			// failure is silent.
			Eventually(func() error {
				return releaseExists(helmFac, testGatewayNamespace, AgentgatewayCRDsReleaseName)
			}, pollTimeout, pollInterval).Should(Succeed())

			Eventually(func() error {
				return releaseExists(helmFac, testGatewayNamespace, AgentgatewayReleaseName)
			}, pollTimeout, pollInterval).Should(Succeed())

			// Gateway API is present in this suite, so its condition is True
			// from the first pass.
			Eventually(func() metav1.ConditionStatus {
				return conditionStatus(agentgatewayv1alpha1.ConditionGatewayAPIReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			// The control plane is not ready until its Deployment is, and
			// envtest runs no Deployment controller.
			Eventually(func() metav1.ConditionStatus {
				return conditionStatus(agentgatewayv1alpha1.ConditionControlPlaneReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionFalse))

			markDeploymentAvailable(testGatewayNamespace, AgentgatewayReleaseName)

			Eventually(func() metav1.ConditionStatus {
				return conditionStatus(agentgatewayv1alpha1.ConditionControlPlaneReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))
		})

		It("reports the installed chart versions, so they can be seen without cracking open the image", func() {
			createPlatform()
			markDeploymentAvailable(testGatewayNamespace, AgentgatewayReleaseName)

			Eventually(func() string {
				platform := getPlatform()
				if platform.Status.BundledChartVersions == nil {
					return ""
				}
				return platform.Status.BundledChartVersions.Agentgateway
			}, pollTimeout, pollInterval).Should(Equal(vendoredcharts.AgentgatewayChartVersion))

			platform := getPlatform()
			Expect(platform.Status.BundledChartVersions.AgentgatewayCRDs).
				To(Equal(vendoredcharts.AgentgatewayCRDsChartVersion))
		})

		It("creates the gateway namespace, owned by the platform so it cascades", func() {
			// A namespace of its own, because envtest never actually removes a
			// namespace, there is no namespace controller to finish the job,
			// so one another spec created would already exist here, unowned,
			// and the operator would rightly leave it alone.
			namespace := "agentgateway-ownership-test"

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

			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, &corev1.Namespace{})
			}, pollTimeout, pollInterval).Should(Succeed())

			ns := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, ns)).To(Succeed())
			Expect(ns.Labels).To(HaveKeyWithValue(ManagedByLabel, ManagedByValue))
			Expect(ns.OwnerReferences).To(HaveLen(1))
			Expect(ns.OwnerReferences[0].Kind).To(Equal("AgentGatewayPlatform"))
		})
	})

	Describe("waiting for the GatewayClass", func() {
		// agentgateway's own controller creates the GatewayClass, after leader
		// election. A ready Deployment is therefore necessary but not
		// sufficient, and this operator must never create it itself.
		It("waits for the GatewayClass rather than assuming Deployment-ready is enough", func() {
			createPlatform()
			markDeploymentAvailable(testGatewayNamespace, AgentgatewayReleaseName)

			Eventually(func() metav1.ConditionStatus {
				return conditionStatus(agentgatewayv1alpha1.ConditionControlPlaneReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			// The control plane is ready, but with no GatewayClass the platform
			// must not claim the Gateway is programmed.
			Consistently(func() metav1.ConditionStatus {
				return conditionStatus(agentgatewayv1alpha1.ConditionGatewayProgrammed)
			}, 2*time.Second, pollInterval).ShouldNot(Equal(metav1.ConditionTrue))

			msg := conditionMessage(agentgatewayv1alpha1.ConditionGatewayProgrammed)
			Expect(msg).To(ContainSubstring("GatewayClass"))
		})

		It("never creates the GatewayClass itself", func() {
			createPlatform()
			markDeploymentAvailable(testGatewayNamespace, AgentgatewayReleaseName)

			// Give the reconciler room to run through its steps.
			Eventually(func() metav1.ConditionStatus {
				return conditionStatus(agentgatewayv1alpha1.ConditionControlPlaneReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			Consistently(func() bool {
				gwc := newGatewayClass()
				err := k8sClient.Get(ctx, types.NamespacedName{Name: GatewayClassName}, gwc)
				return apierrors.IsNotFound(err)
			}, 2*time.Second, pollInterval).Should(BeTrue(),
				"the operator must wait for agentgateway's controller to create the GatewayClass, never create it")
		})

		It("proceeds to create the Gateway once the GatewayClass is Accepted", func() {
			createPlatform()
			markDeploymentAvailable(testGatewayNamespace, AgentgatewayReleaseName)
			createAcceptedGatewayClass()

			Eventually(func() error {
				gw := newGateway()
				return k8sClient.Get(ctx,
					types.NamespacedName{Namespace: testGatewayNamespace, Name: GatewayName}, gw)
			}, pollTimeout, pollInterval).Should(Succeed())

			gw := newGateway()
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Namespace: testGatewayNamespace, Name: GatewayName}, gw)).To(Succeed())

			className, _, _ := unstructured.NestedString(gw.Object, "spec", "gatewayClassName")
			Expect(className).To(Equal(GatewayClassName))
		})
	})

	Describe("the data-plane Service type", func() {
		// agentgateway's default is LoadBalancer, which never resolves on a
		// cluster with no load-balancer provider. Sessions reach the gateway
		// over in-cluster DNS, so ClusterIP is correct everywhere and is not a
		// user-facing choice.
		It("forces ClusterIP through a parameters overlay, so a cluster with no load-balancer provider works unconfigured", func() {
			createPlatform()
			markDeploymentAvailable(testGatewayNamespace, AgentgatewayReleaseName)

			Eventually(func() error {
				params := newParameters()
				return k8sClient.Get(ctx,
					types.NamespacedName{Namespace: testGatewayNamespace, Name: ParametersName}, params)
			}, pollTimeout, pollInterval).Should(Succeed())

			params := newParameters()
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Namespace: testGatewayNamespace, Name: ParametersName}, params)).To(Succeed())

			svcType, found, err := unstructured.NestedString(params.Object, "spec", "service", "spec", "type")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue(), "the overlay must set the data-plane Service type")
			Expect(svcType).To(Equal("ClusterIP"))
		})

		// The overlay above is inert unless something points at it. An earlier
		// build created the AgentgatewayParameters and stopped there, so this
		// suite went green while every real cluster still provisioned a
		// LoadBalancer Service that sat at <pending> forever.
		It("references the overlay from the Gateway, because an overlay nothing points at is ignored", func() {
			createPlatform()
			markDeploymentAvailable(testGatewayNamespace, AgentgatewayReleaseName)
			createAcceptedGatewayClass()

			var ref map[string]any
			Eventually(func() bool {
				gw := newGateway()
				if err := k8sClient.Get(ctx,
					types.NamespacedName{Namespace: testGatewayNamespace, Name: GatewayName}, gw); err != nil {
					return false
				}
				var found bool
				var err error
				ref, found, err = unstructured.NestedMap(gw.Object,
					"spec", "infrastructure", "parametersRef")
				return err == nil && found
			}, pollTimeout, pollInterval).Should(BeTrue(),
				"the Gateway must carry spec.infrastructure.parametersRef")

			Expect(ref["group"]).To(Equal(AgentgatewayGroup))
			Expect(ref["kind"]).To(Equal(KindAgentgatewayParameters))
			Expect(ref["name"]).To(Equal(ParametersName),
				"the ref must name the overlay this operator created")
		})
	})

	Describe("the counter store's security context", func() {
		// RunAsNonRoot on its own is not enough. The kubelet resolves it
		// against the image's declared user, and redis:7-alpine declares none,
		// so it assumes root, refuses the container outright, and the pod sits
		// at CreateContainerConfigError with "container has runAsNonRoot and
		// image will run as root". That is a permanent config error, not a
		// crash loop: no amount of retrying clears it, and the rate-limit
		// service has no counter store until someone notices.
		//
		// envoyproxy/ratelimit declares USER 65532, which is why the identical
		// pod-level context works there and this one has to name a UID.
		It("names a non-root UID, because the redis image declares no default user", func() {
			createPlatform()
			markDeploymentAvailable(testGatewayNamespace, AgentgatewayReleaseName)

			// The rate-limit stack is rendered only after the Gateway is
			// programmed, so the earlier steps have to run for redis to exist.
			createAcceptedGatewayClass()
			Eventually(func() error {
				gw := newGateway()
				return k8sClient.Get(ctx,
					types.NamespacedName{Namespace: testGatewayNamespace, Name: GatewayName}, gw)
			}, pollTimeout, pollInterval).Should(Succeed())
			markGatewayProgrammed(testGatewayNamespace, GatewayName)
			markDeploymentAvailable(testGatewayNamespace, GatewayName)

			deploy := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Namespace: testGatewayNamespace, Name: redisName,
				}, deploy)
			}, pollTimeout, pollInterval).Should(Succeed())

			sc := deploy.Spec.Template.Spec.SecurityContext
			Expect(sc).NotTo(BeNil())
			Expect(sc.RunAsNonRoot).NotTo(BeNil())
			Expect(*sc.RunAsNonRoot).To(BeTrue())

			Expect(sc.RunAsUser).NotTo(BeNil(),
				"RunAsNonRoot without RunAsUser leaves the kubelet unable to "+
					"prove redis:7-alpine is non-root, and it refuses the container")
			Expect(*sc.RunAsUser).NotTo(BeZero(), "must not be root")
			Expect(*sc.RunAsUser).To(Equal(int64(redisUID)))
		})
	})

	Describe("readiness of the counter store", func() {
		// The rate-limit service reports Available with no counter store
		// reachable: it retries redis in the background and answers checks
		// meanwhile. Gating only on the service therefore let the platform
		// report Ready while the store was in CreateContainerConfigError, the
		// exact state that made this bug survive to a live cluster. Token
		// budgets are unenforceable without the store, so waiting is correct.
		It("does not report ready while the counter store is unavailable", func() {
			createPlatform()
			markDeploymentAvailable(testGatewayNamespace, AgentgatewayReleaseName)
			createAcceptedGatewayClass()

			Eventually(func() error {
				gw := newGateway()
				return k8sClient.Get(ctx,
					types.NamespacedName{Namespace: testGatewayNamespace, Name: GatewayName}, gw)
			}, pollTimeout, pollInterval).Should(Succeed())
			markGatewayProgrammed(testGatewayNamespace, GatewayName)
			markDeploymentAvailable(testGatewayNamespace, GatewayName)

			// Everything except the counter store is healthy.
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Namespace: testGatewayNamespace, Name: RateLimitServiceName,
				}, &appsv1.Deployment{})
			}, pollTimeout, pollInterval).Should(Succeed())
			markDeploymentAvailable(testGatewayNamespace, RateLimitServiceName)

			Consistently(func() metav1.ConditionStatus {
				return conditionStatus(agentgatewayv1alpha1.ConditionRateLimitReady)
			}, "2s", pollInterval).ShouldNot(Equal(metav1.ConditionTrue),
				"RateLimitReady must not be True while redis is unavailable")

			Expect(conditionStatus(agentgatewayv1alpha1.ConditionReady)).
				NotTo(Equal(metav1.ConditionTrue))

			// Once the store rolls out, readiness follows.
			markDeploymentAvailable(testGatewayNamespace, redisName)
			Eventually(func() metav1.ConditionStatus {
				return conditionStatus(agentgatewayv1alpha1.ConditionRateLimitReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))
		})
	})

	Describe("the LLM request path", func() {
		// Everything below is what makes /v1/chat/completions resolve at all.
		// Get either half wrong and every request 404s with "route not found"
		// while the platform, catalog, Gateway and models all report healthy,
		// agentgateway does not report a model that failed to attach, so there
		// is no failing condition anywhere to notice.

		It("enables the AgentgatewayModel API, which the chart ships disabled", func() {
			values := renderAgentgatewayValues(nil)

			models, ok := values["agentgatewayModels"].(map[string]any)
			Expect(ok).To(BeTrue(),
				"the control-plane values must set agentgatewayModels")
			Expect(models["enabled"]).To(BeTrue(),
				"this operator renders AgentgatewayModels and nothing else, so "+
					"the API it depends on cannot be left at the chart's default of off")
		})

		It("names AgentgatewayModel in the listener's allowedRoutes, since the kind is opt-in", func() {
			createPlatform()
			markDeploymentAvailable(testGatewayNamespace, AgentgatewayReleaseName)
			createAcceptedGatewayClass()

			var kinds []any
			Eventually(func() bool {
				gw := newGateway()
				if err := k8sClient.Get(ctx,
					types.NamespacedName{Namespace: testGatewayNamespace, Name: GatewayName}, gw); err != nil {
					return false
				}
				listeners, found, err := unstructured.NestedSlice(gw.Object, "spec", "listeners")
				if err != nil || !found || len(listeners) == 0 {
					return false
				}
				listener, _ := listeners[0].(map[string]any)
				allowed, _ := listener["allowedRoutes"].(map[string]any)
				if allowed == nil {
					return false
				}
				kinds, _ = allowed["kinds"].([]any)
				return len(kinds) > 0
			}, pollTimeout, pollInterval).Should(BeTrue(),
				"the listener must name the route kinds it accepts")

			named := map[string]string{}
			for _, raw := range kinds {
				kind, _ := raw.(map[string]any)
				group, _ := kind["group"].(string)
				name, _ := kind["kind"].(string)
				named[name] = group
			}

			Expect(named).To(HaveKeyWithValue(KindAgentgatewayModel, AgentgatewayGroup),
				"without this the models attach to nothing and every request 404s")

			// Setting kinds at all narrows the listener to exactly what is
			// listed, so the default this operator does not use must still be
			// carried or it is silently revoked.
			Expect(named).To(HaveKeyWithValue(KindHTTPRoute, GatewayAPIGroup),
				"listing kinds revokes the defaults, so HTTPRoute must be kept")
		})

		It("adds the model kind to a Gateway created before this operator named it", func() {
			createPlatform()
			markDeploymentAvailable(testGatewayNamespace, AgentgatewayReleaseName)
			createAcceptedGatewayClass()

			Eventually(func() error {
				gw := newGateway()
				return k8sClient.Get(ctx,
					types.NamespacedName{Namespace: testGatewayNamespace, Name: GatewayName}, gw)
			}, pollTimeout, pollInterval).Should(Succeed())

			// Rewind the listener to the pre-fix shape: namespaces only, no
			// kinds, exactly as an earlier build wrote it.
			gw := newGateway()
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Namespace: testGatewayNamespace, Name: GatewayName}, gw)).To(Succeed())
			listeners, _, err := unstructured.NestedSlice(gw.Object, "spec", "listeners")
			Expect(err).NotTo(HaveOccurred())
			listener, _ := listeners[0].(map[string]any)
			listener["allowedRoutes"] = map[string]any{
				"namespaces": map[string]any{"from": "All"},
			}
			listeners[0] = listener
			Expect(unstructured.SetNestedSlice(gw.Object, listeners, "spec", "listeners")).To(Succeed())
			Expect(k8sClient.Update(ctx, gw)).To(Succeed())

			Eventually(func() bool {
				live := newGateway()
				if err := k8sClient.Get(ctx,
					types.NamespacedName{Namespace: testGatewayNamespace, Name: GatewayName}, live); err != nil {
					return false
				}
				ls, found, err := unstructured.NestedSlice(live.Object, "spec", "listeners")
				if err != nil || !found || len(ls) == 0 {
					return false
				}
				l, _ := ls[0].(map[string]any)
				allowed, _ := l["allowedRoutes"].(map[string]any)
				if allowed == nil {
					return false
				}
				ks, _ := allowed["kinds"].([]any)
				for _, raw := range ks {
					k, _ := raw.(map[string]any)
					if k["kind"] == KindAgentgatewayModel {
						return true
					}
				}
				return false
			}, pollTimeout, pollInterval).Should(BeTrue(),
				"a Gateway from an earlier build must be converged, not left broken")
		})
	})

	Describe("the Gateway's name", func() {
		// agentgateway names the data-plane Deployment, Service and
		// ServiceAccount after the Gateway, verbatim, in the Gateway's own
		// namespace. Naming the Gateway after the control-plane Helm release
		// makes the data plane try to adopt the control plane's Deployment,
		// whose spec.selector is immutable and different: an unrecoverable
		// wedge, retried forever at Programmed=False.
		It("never matches the control-plane release name, which would collide with its Deployment", func() {
			Expect(GatewayName).NotTo(Equal(AgentgatewayReleaseName))
			Expect(GatewayName).NotTo(Equal(RateLimitReleaseName))
			Expect(GatewayName).NotTo(Equal(AgentgatewayCRDsReleaseName))
		})

		It("removes the pre-rename Gateway, so a cluster upgraded from an older build heals itself", func() {
			createPlatform()
			markDeploymentAvailable(testGatewayNamespace, AgentgatewayReleaseName)
			createAcceptedGatewayClass()

			legacy := newGateway()
			legacy.SetName(LegacyGatewayName)
			legacy.SetNamespace(testGatewayNamespace)
			legacy.SetLabels(map[string]string{ManagedByLabel: ManagedByValue})
			Expect(unstructured.SetNestedMap(legacy.Object, map[string]any{
				"gatewayClassName": GatewayClassName,
				"listeners": []any{
					map[string]any{
						"name":     GatewayListenerName,
						"port":     int64(GatewayPort),
						"protocol": "HTTP",
					},
				},
			}, "spec")).To(Succeed())
			Expect(k8sClient.Create(ctx, legacy)).To(Succeed())

			Eventually(func() bool {
				gw := newGateway()
				err := k8sClient.Get(ctx,
					types.NamespacedName{Namespace: testGatewayNamespace, Name: LegacyGatewayName}, gw)
				return apierrors.IsNotFound(err)
			}, pollTimeout, pollInterval).Should(BeTrue(),
				"the wedged pre-rename Gateway must be deleted")
		})

		It("leaves a Gateway of that name it did not create, which may be a cluster operator's own", func() {
			createPlatform()
			markDeploymentAvailable(testGatewayNamespace, AgentgatewayReleaseName)
			createAcceptedGatewayClass()

			foreign := newGateway()
			foreign.SetName(LegacyGatewayName)
			foreign.SetNamespace(testGatewayNamespace)
			// No managed-by label, not ours.
			Expect(unstructured.SetNestedMap(foreign.Object, map[string]any{
				"gatewayClassName": GatewayClassName,
				"listeners": []any{
					map[string]any{
						"name":     GatewayListenerName,
						"port":     int64(GatewayPort),
						"protocol": "HTTP",
					},
				},
			}, "spec")).To(Succeed())
			Expect(k8sClient.Create(ctx, foreign)).To(Succeed())

			// Wait for the operator to have reconciled past the prune, by
			// waiting for the Gateway it does own.
			Eventually(func() error {
				gw := newGateway()
				return k8sClient.Get(ctx,
					types.NamespacedName{Namespace: testGatewayNamespace, Name: GatewayName}, gw)
			}, pollTimeout, pollInterval).Should(Succeed())

			Consistently(func() error {
				gw := newGateway()
				return k8sClient.Get(ctx,
					types.NamespacedName{Namespace: testGatewayNamespace, Name: LegacyGatewayName}, gw)
			}, "2s", pollInterval).Should(Succeed(),
				"a Gateway without this operator's managed-by label must survive")
		})
	})

	Describe("readiness", func() {
		It("gates on Programmed plus the data-plane Deployment, never on the Gateway's addresses", func() {
			createPlatform()

			// driveToReady programs the Gateway with no addresses at all, the
			// LoadBalancer-without-a-provider case. Readiness must not depend
			// on them.
			driveToReady()

			Eventually(func() metav1.ConditionStatus {
				return conditionStatus(agentgatewayv1alpha1.ConditionReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			platform := getPlatform()
			Expect(platform.Status.Phase).To(Equal(agentgatewayv1alpha1.PlatformReady))
			Expect(platform.Status.GatewayURL).To(Equal(
				"http://agentgateway-educates.agentgateway-system.svc.cluster.local:4000"))
			Expect(platform.Status.GatewayNamespace).To(Equal(testGatewayNamespace))

			// The Gateway carries no addresses, confirming readiness did not
			// come from them.
			gw := newGateway()
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Namespace: testGatewayNamespace, Name: GatewayName}, gw)).To(Succeed())
			addresses, found, err := unstructured.NestedSlice(gw.Object, "status", "addresses")
			Expect(err).NotTo(HaveOccurred())
			Expect(found && len(addresses) > 0).To(BeFalse(),
				"the fixture must leave the Gateway address-less for this assertion to mean anything")
		})

		It("does not become Ready while the data-plane Deployment is unavailable", func() {
			createPlatform()
			markDeploymentAvailable(testGatewayNamespace, AgentgatewayReleaseName)
			createAcceptedGatewayClass()

			Eventually(func() error {
				gw := newGateway()
				return k8sClient.Get(ctx,
					types.NamespacedName{Namespace: testGatewayNamespace, Name: GatewayName}, gw)
			}, pollTimeout, pollInterval).Should(Succeed())

			// Programmed, but the data plane has not rolled out.
			markGatewayProgrammed(testGatewayNamespace, GatewayName)

			Consistently(func() metav1.ConditionStatus {
				return conditionStatus(agentgatewayv1alpha1.ConditionReady)
			}, 2*time.Second, pollInterval).ShouldNot(Equal(metav1.ConditionTrue))
		})
	})

	Describe("Gateway API installation", func() {
		It("skips installation when Gateway API is declared as already present", func() {
			platform := &agentgatewayv1alpha1.AgentGatewayPlatform{
				ObjectMeta: metav1.ObjectMeta{Name: agentgatewayv1alpha1.SingletonName},
				Spec: agentgatewayv1alpha1.AgentGatewayPlatformSpec{
					Provider:   agentgatewayv1alpha1.ProviderBundled,
					GatewayAPI: agentgatewayv1alpha1.GatewayAPIExisting,
				},
			}
			Expect(k8sClient.Create(ctx, platform)).To(Succeed())

			Eventually(func() string {
				return conditionMessage(agentgatewayv1alpha1.ConditionGatewayAPIReady)
			}, pollTimeout, pollInterval).Should(ContainSubstring("skipped"))

			Expect(conditionStatus(agentgatewayv1alpha1.ConditionGatewayAPIReady)).
				To(Equal(metav1.ConditionTrue))
		})
	})

	Describe("release ownership", func() {
		// v4 does not check who owns a release before converging it, so two
		// operators sharing a release name fight in an upgrade loop. This
		// operator refuses instead.
		It("refuses a release owned by somebody else, reporting a conflict rather than converging", func() {
			hc, err := helmFac.For(testGatewayNamespace)
			Expect(err).NotTo(HaveOccurred())

			chrt, err := vendoredcharts.AgentgatewayCRDs()
			Expect(err).NotTo(HaveOccurred())

			// A release with somebody else's ownership marker, already deployed.
			Expect(hc.SeedRelease(AgentgatewayCRDsReleaseName, 1,
				releasecommon.StatusDeployed, chrt, map[string]any{},
				map[string]string{helm.OwnerLabel: "some-other-operator"})).To(Succeed())

			createPlatform()

			Eventually(func() string {
				return conditionReason(agentgatewayv1alpha1.ConditionReady)
			}, pollTimeout, pollInterval).Should(Equal(agentgatewayv1alpha1.ReasonReleaseConflict))

			platform := getPlatform()
			Expect(platform.Status.Phase).To(Equal(agentgatewayv1alpha1.PlatformFailed))
			Expect(conditionMessage(agentgatewayv1alpha1.ConditionReady)).
				To(ContainSubstring("owned by someone else"))

			// The refusal must leave the release alone rather than upgrading it.
			rel, err := hc.Status(AgentgatewayCRDsReleaseName)
			Expect(err).NotTo(HaveOccurred())
			Expect(rel.Version).To(Equal(1), "the release must not have been converged")
			Expect(rel.Labels).To(HaveKeyWithValue(helm.OwnerLabel, "some-other-operator"))
		})

		It("converges a release it does own", func() {
			hc, err := helmFac.For(testGatewayNamespace)
			Expect(err).NotTo(HaveOccurred())

			chrt, err := vendoredcharts.AgentgatewayCRDs()
			Expect(err).NotTo(HaveOccurred())

			// Our own release, but with values that no longer match, so it must
			// be upgraded rather than left alone.
			Expect(hc.SeedRelease(AgentgatewayCRDsReleaseName, 1,
				releasecommon.StatusDeployed, chrt, map[string]any{"stale": true},
				map[string]string{helm.OwnerLabel: helm.OwnerValue})).To(Succeed())

			createPlatform()

			Eventually(func() int {
				rel, err := hc.Status(AgentgatewayCRDsReleaseName)
				if err != nil {
					return 0
				}
				return rel.Version
			}, pollTimeout, pollInterval).Should(BeNumerically(">", 1),
				"a release we own with changed values must be upgraded")
		})
	})

	Describe("idempotency", func() {
		It("a second reconcile changes nothing", func() {
			createPlatform()
			driveToReady()

			Eventually(func() metav1.ConditionStatus {
				return conditionStatus(agentgatewayv1alpha1.ConditionReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			hc, err := helmFac.For(testGatewayNamespace)
			Expect(err).NotTo(HaveOccurred())

			before, err := hc.Status(AgentgatewayReleaseName)
			Expect(err).NotTo(HaveOccurred())

			// Force another pass by bumping the spec. Restating the Gateway API
			// setting at its current value changes the generation without
			// changing anything the converge would render, which is exactly the
			// case under test.
			platform := getPlatform()
			platform.Spec.GatewayAPI = agentgatewayv1alpha1.GatewayAPIExisting
			Expect(k8sClient.Update(ctx, platform)).To(Succeed())

			Eventually(func() int64 {
				return getPlatform().Status.ObservedGeneration
			}, pollTimeout, pollInterval).Should(Equal(platform.Generation))

			// The release must not have climbed a revision: the rendered values
			// did not change, so there was nothing to do.
			after, err := hc.Status(AgentgatewayReleaseName)
			Expect(err).NotTo(HaveOccurred())
			Expect(after.Version).To(Equal(before.Version),
				"an unchanged converge must not climb release revisions")
		})
	})
})
