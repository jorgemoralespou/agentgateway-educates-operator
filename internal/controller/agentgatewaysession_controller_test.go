package controller

import (
	"context"
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	crconfig "sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
	"github.com/educates/agentgateway-educates-operator/internal/participantkey"
)

var _ = Describe("AgentGatewaySession reconciler", func() {
	var (
		mgrCancel context.CancelFunc
		mgrDone   chan error
	)

	// A short finalizer window so the give-up path can be exercised without the
	// suite waiting two minutes for it.
	const testFinalizerTimeout = 3 * time.Second

	BeforeEach(func() {
		ensureNamespaceExists(testGatewayNamespace)
		ensureNamespaceExists(workshopNamespace)

		// The session reconciler gates on a ready platform and catalog, so both
		// are faked into a ready state rather than driven through a real
		// install — that path is covered by the platform specs.
		createReadyPlatform()
		createReadyCatalog()

		var mgrCtx context.Context
		mgrCtx, mgrCancel = context.WithCancel(ctx)

		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:     k8sClient.Scheme(),
			Metrics:    metricsserver.Options{BindAddress: "0"},
			Controller: crconfig.Controller{SkipNameValidation: ptrTo(true)},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect((&AgentGatewaySessionReconciler{
			Client:           mgr.GetClient(),
			Scheme:           mgr.GetScheme(),
			FinalizerTimeout: testFinalizerTimeout,
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
		cleanupSessions()
	})

	Describe("minting a key", func() {
		It("writes the plaintext to the workshop namespace and only the hash to the gateway namespace", func() {
			session := createSession("ws-001")

			Eventually(func() metav1.ConditionStatus {
				return sessionCondition(session, agentgatewayv1alpha1.ConditionReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			// The Secret must be in the workshop namespace: the attendee's pod
			// runs there and can only resolve a secretKeyRef in its own
			// namespace.
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: workshopNamespace, Name: "ws-001-agentgateway",
			}, secret)).To(Succeed())

			key := string(secret.Data[agentgatewayv1alpha1.SecretKeyAPIKey])
			Expect(key).NotTo(BeEmpty())
			Expect(participantkey.IsParticipantKey(key)).To(BeTrue(),
				"the Secret must hold a key this operator generated")
			Expect(string(secret.Data[agentgatewayv1alpha1.SecretKeyBaseURL])).
				To(Equal(testGatewayURL))

			// The registration must be in the gateway namespace: agentgateway
			// resolves API-key credentials only within its own namespace.
			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: testGatewayNamespace, Name: "ws-001-agentgateway",
			}, cm)).To(Succeed())

			// It must carry the hash of the exact key in the Secret, and no key
			// material at all.
			entry := parseRegistrationEntry(cm, "ws-001")
			Expect(entry.KeyHash).To(Equal(participantkey.Hash(key)))
			Expect(entry.KeyHash).To(HavePrefix("sha256:"))
			Expect(entry.Metadata).To(HaveKeyWithValue("session", "ws-001"))

			raw := cm.Data["ws-001"]
			Expect(raw).NotTo(ContainSubstring(key),
				"the registration must never contain key material")
		})

		It("labels the registration so the policy selects it", func() {
			session := createSession("ws-002")

			Eventually(func() metav1.ConditionStatus {
				return sessionCondition(session, agentgatewayv1alpha1.ConditionReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: testGatewayNamespace, Name: "ws-002-agentgateway",
			}, cm)).To(Succeed())

			// The single API-key policy selects on exactly this label.
			Expect(cm.Labels).To(HaveKeyWithValue(agentgatewayv1alpha1.RegistrationLabel, "true"))
			// And these make a leaked registration findable.
			Expect(cm.Labels).To(HaveKeyWithValue(agentgatewayv1alpha1.SessionLabel, "ws-002"))
			Expect(cm.Labels).To(HaveKeyWithValue(agentgatewayv1alpha1.SessionNSLabel, workshopNamespace))
		})

		It("gives each session its own registration, so concurrent starts do not contend", func() {
			// Thirty attendees starting at once would contend on one shared
			// object, and duplicate keys across ConfigMaps are documented
			// upstream as undefined.
			for _, name := range []string{"ws-c1", "ws-c2", "ws-c3"} {
				createSession(name)
			}

			for _, name := range []string{"ws-c1", "ws-c2", "ws-c3"} {
				Eventually(func() error {
					return k8sClient.Get(ctx, types.NamespacedName{
						Namespace: testGatewayNamespace, Name: name + "-agentgateway",
					}, &corev1.ConfigMap{})
				}, pollTimeout, pollInterval).Should(Succeed())
			}

			// Distinct keys, so one attendee cannot spend another's budget.
			keys := map[string]bool{}
			for _, name := range []string{"ws-c1", "ws-c2", "ws-c3"} {
				secret := &corev1.Secret{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{
					Namespace: workshopNamespace, Name: name + "-agentgateway",
				}, secret)).To(Succeed())
				key := string(secret.Data[agentgatewayv1alpha1.SecretKeyAPIKey])
				Expect(keys[key]).To(BeFalse(), "two sessions were given the same key")
				keys[key] = true
			}
		})
	})

	Describe("ownership", func() {
		// An object in the workshop namespace but owned by the session
		// namespace is both reachable by the attendee's pod and collected on
		// teardown. Educates relies on this for its own session Secrets
		// (ADR-0002), which is what makes it platform-idiomatic here.
		It("owns the Secret by the session namespace, and the registration by nothing", func() {
			sessionNS := ensureSessionNamespace("ws-003")
			session := createSession("ws-003")

			Eventually(func() metav1.ConditionStatus {
				return sessionCondition(session, agentgatewayv1alpha1.ConditionReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: workshopNamespace, Name: "ws-003-agentgateway",
			}, secret)).To(Succeed())

			Expect(secret.OwnerReferences).To(HaveLen(1))
			Expect(secret.OwnerReferences[0].Kind).To(Equal("Namespace"))
			Expect(secret.OwnerReferences[0].Name).To(Equal("ws-003"))
			Expect(secret.OwnerReferences[0].UID).To(Equal(sessionNS.UID))

			// The registration is in a namespace that outlives every session,
			// so nothing owns it — which is precisely why it needs a finalizer.
			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: testGatewayNamespace, Name: "ws-003-agentgateway",
			}, cm)).To(Succeed())
			Expect(cm.OwnerReferences).To(BeEmpty(),
				"the registration must not be owned, or it would be collected before cleanup could run")
		})
	})

	Describe("idempotency", func() {
		// The regression test for the create-only defect inherited from the
		// prior art, in reverse: generating a key on every pass would rotate a
		// live attendee out of their session mid-workshop.
		It("does not rotate the key on a second reconcile", func() {
			session := createSession("ws-004")

			Eventually(func() metav1.ConditionStatus {
				return sessionCondition(session, agentgatewayv1alpha1.ConditionReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			firstKey := readKey("ws-004")
			Expect(firstKey).NotTo(BeEmpty())

			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: testGatewayNamespace, Name: "ws-004-agentgateway",
			}, cm)).To(Succeed())
			firstResourceVersion := cm.ResourceVersion

			// Force another reconcile by bumping the spec.
			live := &agentgatewayv1alpha1.AgentGatewaySession{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: workshopNamespace, Name: "ws-004",
			}, live)).To(Succeed())
			live.Spec.TokenBudget = 200000
			Expect(k8sClient.Update(ctx, live)).To(Succeed())

			Eventually(func() int64 {
				got := &agentgatewayv1alpha1.AgentGatewaySession{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Namespace: workshopNamespace, Name: "ws-004",
				}, got); err != nil {
					return 0
				}
				return got.Status.ObservedGeneration
			}, pollTimeout, pollInterval).Should(Equal(live.Generation))

			Expect(readKey("ws-004")).To(Equal(firstKey),
				"a reconcile must never rotate a live attendee's key")

			// And the registration was not rewritten, since the hash did not
			// change.
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: testGatewayNamespace, Name: "ws-004-agentgateway",
			}, cm)).To(Succeed())
			Expect(cm.ResourceVersion).To(Equal(firstResourceVersion),
				"an unchanged registration must not be rewritten")
		})
	})

	Describe("self-healing", func() {
		// The plaintext is unrecoverable once written, so a lost Secret cannot
		// be restored — a new key is generated and the registration updated,
		// which makes rotation the recovery path (ADR-0004).
		It("repairs a deleted Secret by generating a new key and updating the registration", func() {
			session := createSession("ws-005")

			Eventually(func() metav1.ConditionStatus {
				return sessionCondition(session, agentgatewayv1alpha1.ConditionReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			originalKey := readKey("ws-005")
			originalHash := participantkey.Hash(originalKey)

			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: workshopNamespace, Name: "ws-005-agentgateway",
			}, secret)).To(Succeed())
			Expect(k8sClient.Delete(ctx, secret)).To(Succeed())

			// A new key appears...
			Eventually(func() string {
				return readKeyOrEmpty("ws-005")
			}, pollTimeout, pollInterval).ShouldNot(Or(BeEmpty(), Equal(originalKey)))

			newKey := readKey("ws-005")

			// ...and the registration follows it, or the attendee would hold a
			// key the gateway does not know.
			Eventually(func() string {
				cm := &corev1.ConfigMap{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Namespace: testGatewayNamespace, Name: "ws-005-agentgateway",
				}, cm); err != nil {
					return ""
				}
				return parseRegistrationEntry(cm, "ws-005").KeyHash
			}, pollTimeout, pollInterval).Should(Equal(participantkey.Hash(newKey)))

			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: testGatewayNamespace, Name: "ws-005-agentgateway",
			}, cm)).To(Succeed())
			Expect(parseRegistrationEntry(cm, "ws-005").KeyHash).NotTo(Equal(originalHash))
		})
	})

	Describe("placement", func() {
		// Without `namespace: $(workshop_namespace)` the grant lands in the
		// session namespace, the Secret follows it there, and the attendee's
		// pod wedges in CreateContainerConfigError with no useful diagnostic.
		It("rejects a grant in a session namespace, with an explanatory condition and no objects", func() {
			sessionNS := ensureSessionNamespace("ws-006")

			session := &agentgatewayv1alpha1.AgentGatewaySession{
				ObjectMeta: metav1.ObjectMeta{Name: "ws-006", Namespace: sessionNS.Name},
				Spec: agentgatewayv1alpha1.AgentGatewaySessionSpec{
					CatalogRef:  agentgatewayv1alpha1.CatalogReference{Name: agentgatewayv1alpha1.SingletonName},
					TokenBudget: 100000,
				},
			}
			Expect(k8sClient.Create(ctx, session)).To(Succeed())

			Eventually(func() string {
				return sessionConditionReason(session, agentgatewayv1alpha1.ConditionPlacementValid)
			}, pollTimeout, pollInterval).Should(Equal(agentgatewayv1alpha1.ReasonWrongNamespace))

			got := &agentgatewayv1alpha1.AgentGatewaySession{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: sessionNS.Name, Name: "ws-006",
			}, got)).To(Succeed())

			Expect(got.Status.Phase).To(Equal(agentgatewayv1alpha1.SessionRejected))

			// The message has to tell the author what to change, since this is
			// the error they will actually hit.
			message := sessionConditionMessage(session, agentgatewayv1alpha1.ConditionPlacementValid)
			Expect(message).To(ContainSubstring("$(workshop_namespace)"))
			Expect(message).To(ContainSubstring("session namespace"))

			// Nothing may be created: a Secret here would be unreachable, and a
			// registration would be a live credential for a session that never
			// starts.
			Consistently(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Namespace: sessionNS.Name, Name: "ws-006-agentgateway",
				}, &corev1.Secret{})
				return apierrors.IsNotFound(err)
			}, 2*time.Second, pollInterval).Should(BeTrue())

			err := k8sClient.Get(ctx, types.NamespacedName{
				Namespace: testGatewayNamespace, Name: "ws-006-agentgateway",
			}, &corev1.ConfigMap{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue(),
				"a rejected grant must not register a key")
		})
	})

	Describe("status", func() {
		// Status is what someone debugging a session reads, and what they paste
		// when asking for help, so it must be useful and must not be a
		// credential leak.
		It("reports the Secret name and gateway URL, and never the key or its hash", func() {
			session := createSession("ws-007")

			Eventually(func() metav1.ConditionStatus {
				return sessionCondition(session, agentgatewayv1alpha1.ConditionReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			got := &agentgatewayv1alpha1.AgentGatewaySession{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: workshopNamespace, Name: "ws-007",
			}, got)).To(Succeed())

			Expect(got.Status.SecretRef).NotTo(BeNil())
			Expect(got.Status.SecretRef.Name).To(Equal("ws-007-agentgateway"))
			Expect(got.Status.GatewayURL).To(Equal(testGatewayURL))
			Expect(got.Status.Phase).To(Equal(agentgatewayv1alpha1.SessionReady))

			// A TTL backstop, so the key expires even if cleanup never runs.
			Expect(got.Status.ExpiresAt).NotTo(BeNil())
			Expect(got.Status.ExpiresAt.Time).To(BeTemporally(">", got.CreationTimestamp.Time))

			// Nothing in the serialised status may contain the key or its hash.
			key := readKey("ws-007")
			raw, err := json.Marshal(got.Status)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(raw)).NotTo(ContainSubstring(key))
			Expect(string(raw)).NotTo(ContainSubstring(participantkey.Hash(key)))
			Expect(string(raw)).NotTo(ContainSubstring("sha256:"))
		})

		It("progresses its conditions as each step completes", func() {
			session := createSession("ws-008")

			Eventually(func() metav1.ConditionStatus {
				return sessionCondition(session, agentgatewayv1alpha1.ConditionReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			for _, condType := range []string{
				agentgatewayv1alpha1.ConditionPlacementValid,
				agentgatewayv1alpha1.ConditionCatalogAvailable,
				agentgatewayv1alpha1.ConditionSecretWritten,
				agentgatewayv1alpha1.ConditionKeyRegistered,
				agentgatewayv1alpha1.ConditionReady,
			} {
				Expect(sessionCondition(session, condType)).To(Equal(metav1.ConditionTrue),
					"condition %s should be True once the session is ready", condType)
			}
		})
	})

	Describe("teardown", func() {
		It("removes the registration and releases the finalizer", func() {
			session := createSession("ws-009")

			Eventually(func() metav1.ConditionStatus {
				return sessionCondition(session, agentgatewayv1alpha1.ConditionReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: testGatewayNamespace, Name: "ws-009-agentgateway",
			}, &corev1.ConfigMap{})).To(Succeed())

			live := &agentgatewayv1alpha1.AgentGatewaySession{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: workshopNamespace, Name: "ws-009",
			}, live)).To(Succeed())
			Expect(k8sClient.Delete(ctx, live)).To(Succeed())

			// The registration goes...
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Namespace: testGatewayNamespace, Name: "ws-009-agentgateway",
				}, &corev1.ConfigMap{})
				return apierrors.IsNotFound(err)
			}, pollTimeout, pollInterval).Should(BeTrue())

			// ...and the finalizer is released, so the object actually goes
			// away rather than sitting in Terminating.
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Namespace: workshopNamespace, Name: "ws-009",
				}, &agentgatewayv1alpha1.AgentGatewaySession{})
				return apierrors.IsNotFound(err)
			}, pollTimeout, pollInterval).Should(BeTrue())
		})

		// A gateway namespace that is gone means the registration is gone with
		// it. Counted as success rather than retried, so platform teardown does
		// not strand every session behind a finalizer.
		It("treats a missing gateway namespace as cleanup success", func() {
			session := createSession("ws-010")

			Eventually(func() metav1.ConditionStatus {
				return sessionCondition(session, agentgatewayv1alpha1.ConditionReady)
			}, pollTimeout, pollInterval).Should(Equal(metav1.ConditionTrue))

			// Point the platform at a namespace that does not exist, which is
			// what a torn-down platform looks like to this reconciler.
			platform := &agentgatewayv1alpha1.AgentGatewayPlatform{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: agentgatewayv1alpha1.SingletonName,
			}, platform)).To(Succeed())
			platform.Status.GatewayNamespace = "agentgateway-system-gone"
			Expect(k8sClient.Status().Update(ctx, platform)).To(Succeed())

			live := &agentgatewayv1alpha1.AgentGatewaySession{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: workshopNamespace, Name: "ws-010",
			}, live)).To(Succeed())
			Expect(k8sClient.Delete(ctx, live)).To(Succeed())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Namespace: workshopNamespace, Name: "ws-010",
				}, &agentgatewayv1alpha1.AgentGatewaySession{})
				return apierrors.IsNotFound(err)
			}, pollTimeout, pollInterval).Should(BeTrue(),
				"a missing gateway namespace must not strand the session behind its finalizer")
		})
	})
})

// parseRegistrationEntry reads one entry out of a registration ConfigMap.
func parseRegistrationEntry(cm *corev1.ConfigMap, sessionName string) registrationEntry {
	GinkgoHelper()

	raw, ok := cm.Data[sessionName]
	Expect(ok).To(BeTrue(), "registration has no entry named %q", sessionName)

	entry, err := parseRegistration(raw)
	Expect(err).NotTo(HaveOccurred())
	return entry
}
