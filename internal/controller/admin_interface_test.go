package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
)

var _ = Describe("the admin interface overlay", func() {
	Describe("applyAdminAddr", func() {
		// agentgateway rejects a top-level adminAddr and crash-loops rather
		// than ignoring it, so the nesting under `config` is the whole point of
		// this helper and is asserted explicitly.
		It("writes adminAddr under rawConfig.config, the only nesting agentgateway accepts", func() {
			u := newParameters()

			Expect(applyAdminAddr(u, true)).To(Succeed())

			addr, found, err := unstructured.NestedString(u.Object,
				"spec", "rawConfig", "config", "adminAddr")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue(), "the overlay must carry the admin bind address")
			Expect(addr).To(Equal("[::]:15000"),
				"both address families must be bound, as the stats and readiness listeners are")
		})

		It("writes nothing when the interface is not exposed, leaving agentgateway's loopback default", func() {
			u := newParameters()

			Expect(applyAdminAddr(u, false)).To(Succeed())

			_, found, err := unstructured.NestedMap(u.Object, "spec", "rawConfig")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse(),
				"an unexposed interface must not leave an empty rawConfig behind")
		})

		// Turning the field off has to actively remove the key. Merely
		// declining to write it would leave the previous value in place and the
		// interface exposed until somebody hand-edited the overlay.
		It("removes a previously written adminAddr, so turning the field off actually closes the port", func() {
			u := newParameters()
			Expect(applyAdminAddr(u, true)).To(Succeed())

			Expect(applyAdminAddr(u, false)).To(Succeed())

			_, found, err := unstructured.NestedString(u.Object,
				"spec", "rawConfig", "config", "adminAddr")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse(), "the bind address must be gone, not merely stale")
		})

		It("prunes the containers it created, so disabling leaves no empty rawConfig block", func() {
			u := newParameters()
			Expect(applyAdminAddr(u, true)).To(Succeed())

			Expect(applyAdminAddr(u, false)).To(Succeed())

			_, found, err := unstructured.NestedMap(u.Object, "spec", "rawConfig")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
		})

		// The overlay is a shared object: this operator owns one key in it and
		// must not clear a cluster operator's own rawConfig while pruning.
		It("keeps a cluster operator's own rawConfig keys when disabling", func() {
			u := newParameters()
			Expect(unstructured.SetNestedField(u.Object, "json",
				"spec", "rawConfig", "config", "logging", "format")).To(Succeed())
			Expect(applyAdminAddr(u, true)).To(Succeed())

			Expect(applyAdminAddr(u, false)).To(Succeed())

			format, found, err := unstructured.NestedString(u.Object,
				"spec", "rawConfig", "config", "logging", "format")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue(), "a sibling key must survive the prune")
			Expect(format).To(Equal("json"))
		})

		It("is idempotent, so a steady-state reconcile writes no drift", func() {
			first := newParameters()
			Expect(applyAdminAddr(first, true)).To(Succeed())
			second := newParameters()
			Expect(applyAdminAddr(second, true)).To(Succeed())
			Expect(applyAdminAddr(second, true)).To(Succeed())

			Expect(second.Object).To(Equal(first.Object))
		})
	})

	// agentgateway derives the generated Service's ports from the Gateway's
	// listeners, and the admin interface is not a listener. An earlier build
	// bound the listener and stopped there, which was only ever verified on a
	// cluster where the port had been added by hand: on a fresh install the
	// Service has one port and the UI is unreachable however the listener is
	// bound.
	Describe("applyAdminServicePort", func() {
		It("publishes port 15000 when exposed, because the listener set never yields it", func() {
			u := newParameters()

			Expect(applyAdminServicePort(u, true)).To(Succeed())

			ports, found, err := unstructured.NestedSlice(u.Object, "spec", "service", "spec", "ports")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue(), "the overlay must publish the admin port")
			Expect(ports).To(HaveLen(1))

			port, ok := ports[0].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(port["port"]).To(Equal(int64(15000)))
			Expect(port["targetPort"]).To(Equal(int64(15000)))
			Expect(port["protocol"]).To(Equal("TCP"))
		})

		It("publishes no port when not exposed", func() {
			u := newParameters()

			Expect(applyAdminServicePort(u, false)).To(Succeed())

			_, found, err := unstructured.NestedSlice(u.Object, "spec", "service", "spec", "ports")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
		})

		// A Service port left pointing at an unbound listener accepts
		// connections and then refuses them, which reads as a broken gateway
		// rather than a disabled feature.
		It("withdraws the port when the interface is turned off", func() {
			u := newParameters()
			Expect(applyAdminServicePort(u, true)).To(Succeed())

			Expect(applyAdminServicePort(u, false)).To(Succeed())

			_, found, err := unstructured.NestedSlice(u.Object, "spec", "service", "spec", "ports")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse(), "the port must not outlive the listener binding")
		})

		It("is idempotent, so a steady-state reconcile does not duplicate the port", func() {
			u := newParameters()

			Expect(applyAdminServicePort(u, true)).To(Succeed())
			Expect(applyAdminServicePort(u, true)).To(Succeed())

			ports, _, err := unstructured.NestedSlice(u.Object, "spec", "service", "spec", "ports")
			Expect(err).NotTo(HaveOccurred())
			Expect(ports).To(HaveLen(1), "a repeated reconcile must not append a second entry")
		})

		// The overlay merges by port number, so a cluster operator who has
		// published their own port keeps it through both transitions.
		It("leaves a cluster operator's own Service ports alone", func() {
			u := newParameters()
			Expect(unstructured.SetNestedSlice(u.Object, []any{
				map[string]any{"name": "extra", "port": int64(9000), "protocol": "TCP"},
			}, "spec", "service", "spec", "ports")).To(Succeed())

			Expect(applyAdminServicePort(u, true)).To(Succeed())
			Expect(applyAdminServicePort(u, false)).To(Succeed())

			ports, found, err := unstructured.NestedSlice(u.Object, "spec", "service", "spec", "ports")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue(), "a foreign port must survive the withdrawal")
			Expect(ports).To(HaveLen(1))
			port, ok := ports[0].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(port["port"]).To(Equal(int64(9000)))
		})
	})

	// Exposing the interface is only complete when both halves are written:
	// either one alone yields a gateway that looks configured and is not.
	Describe("applyAdminInterface", func() {
		It("writes both the bind address and the Service port", func() {
			u := newParameters()

			Expect(applyAdminInterface(u, true)).To(Succeed())

			addr, found, err := unstructured.NestedString(u.Object,
				"spec", "rawConfig", "config", "adminAddr")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue(), "the listener must be bound")
			Expect(addr).To(Equal("[::]:15000"))

			ports, found, err := unstructured.NestedSlice(u.Object, "spec", "service", "spec", "ports")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue(), "the port must be published alongside the binding")
			Expect(ports).To(HaveLen(1))
		})

		It("withdraws both when turned off", func() {
			u := newParameters()
			Expect(applyAdminInterface(u, true)).To(Succeed())

			Expect(applyAdminInterface(u, false)).To(Succeed())

			_, found, err := unstructured.NestedMap(u.Object, "spec", "rawConfig")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
			_, found, err = unstructured.NestedSlice(u.Object, "spec", "service", "spec", "ports")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
		})

		It("leaves the Service type alone, which is set independently of this feature", func() {
			u := newParameters()
			Expect(unstructured.SetNestedField(u.Object,
				"ClusterIP", "spec", "service", "spec", "type")).To(Succeed())

			Expect(applyAdminInterface(u, true)).To(Succeed())
			Expect(applyAdminInterface(u, false)).To(Succeed())

			svcType, found, err := unstructured.NestedString(u.Object, "spec", "service", "spec", "type")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue(), "the Service type must survive both transitions")
			Expect(svcType).To(Equal("ClusterIP"))
		})
	})

	Describe("adminInterfaceExposed", func() {
		It("reports false when the field is absent, keeping the unauthenticated UI off by default", func() {
			platform := &agentgatewayv1alpha1.AgentGatewayPlatform{}

			Expect(adminInterfaceExposed(platform)).To(BeFalse())
		})

		It("reports false when the block is present but exposed is not set", func() {
			platform := &agentgatewayv1alpha1.AgentGatewayPlatform{
				Spec: agentgatewayv1alpha1.AgentGatewayPlatformSpec{
					AdminInterface: &agentgatewayv1alpha1.AdminInterfaceSpec{},
				},
			}

			Expect(adminInterfaceExposed(platform)).To(BeFalse())
		})

		It("reports true only when explicitly opted in", func() {
			platform := &agentgatewayv1alpha1.AgentGatewayPlatform{
				Spec: agentgatewayv1alpha1.AgentGatewayPlatformSpec{
					AdminInterface: &agentgatewayv1alpha1.AdminInterfaceSpec{Exposed: true},
				},
			}

			Expect(adminInterfaceExposed(platform)).To(BeTrue())
		})

		It("tolerates a nil platform rather than panicking", func() {
			Expect(adminInterfaceExposed(nil)).To(BeFalse())
		})
	})
})
