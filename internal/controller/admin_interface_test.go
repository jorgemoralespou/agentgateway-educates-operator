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
