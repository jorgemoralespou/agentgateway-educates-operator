package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
)

// renderPolicySpec is a pure function so the shape of the policy: the part
// that is easy to get subtly wrong and expensive to debug on a cluster, can be
// asserted without an API server.
var _ = Describe("renderPolicySpec", func() {
	traffic := func(spec map[string]any) map[string]any {
		GinkgoHelper()
		t, ok := spec["traffic"].(map[string]any)
		Expect(ok).To(BeTrue(), "the policy must carry a traffic block")
		return t
	}

	Describe("the upstream request timeout", func() {
		// A reasoning model served locally can take well over a minute for one
		// reply, past agentgateway's own default. The attendee then sees
		// "upstream call failed: connection closed before message completed",
		// which reads like a broken gateway rather than a slow model.
		It("is written when the catalog asks for one", func() {
			spec := renderPolicySpec(agentgatewayv1alpha1.FailClosed, "agentgateway-system", "180s", defaultBudgetWindow, agentgatewayv1alpha1.DefaultTokenBudget)

			timeouts, ok := traffic(spec)["timeouts"].(map[string]any)
			Expect(ok).To(BeTrue(), "a catalog requestTimeout must reach the policy")
			Expect(timeouts["request"]).To(Equal("180s"))
		})

		// Writing agentgateway's own default back at it would pin a value this
		// operator has no opinion about, and would need changing every time
		// that default moved.
		It("is absent when the catalog does not ask, leaving agentgateway's default", func() {
			spec := renderPolicySpec(agentgatewayv1alpha1.FailClosed, "agentgateway-system", "", defaultBudgetWindow, agentgatewayv1alpha1.DefaultTokenBudget)

			Expect(traffic(spec)).NotTo(HaveKey("timeouts"),
				"an unset timeout must not be written at all")
		})

		It("does not disturb the rest of the traffic block", func() {
			spec := renderPolicySpec(agentgatewayv1alpha1.FailOpen, "agentgateway-system", "90s", defaultBudgetWindow, agentgatewayv1alpha1.DefaultTokenBudget)

			t := traffic(spec)
			Expect(t).To(HaveKey("apiKeyAuthentication"))
			Expect(t).To(HaveKey("rateLimit"))
			Expect(t).To(HaveKey("timeouts"))

			rateLimit, _ := t["rateLimit"].(map[string]any)
			global, _ := rateLimit["global"].(map[string]any)
			Expect(global["failureMode"]).To(Equal(string(agentgatewayv1alpha1.FailOpen)),
				"the failure mode must survive alongside the timeout")
		})
	})

	Describe("the token budget limit override", func() {
		limitOverride := func(spec map[string]any) string {
			GinkgoHelper()
			rateLimit, _ := traffic(spec)["rateLimit"].(map[string]any)
			global, _ := rateLimit["global"].(map[string]any)
			descriptors, ok := global["descriptors"].([]any)
			Expect(ok).To(BeTrue(), "the rate limit must carry descriptors")
			Expect(descriptors).NotTo(BeEmpty())
			first, _ := descriptors[0].(map[string]any)
			override, ok := first["limitOverride"].(string)
			Expect(ok).To(BeTrue(), "the token descriptor must carry a limitOverride")
			return override
		}

		// Pins the prefactor: parameterizing the window and the fallback must
		// not change a single byte of what the gateway is asked to evaluate.
		It("is byte-identical to the expression this replaced", func() {
			spec := renderPolicySpec(agentgatewayv1alpha1.FailClosed, "agentgateway-system", "",
				defaultBudgetWindow, agentgatewayv1alpha1.DefaultTokenBudget)

			Expect(limitOverride(spec)).To(Equal(
				`has(apiKey.tokenBudget) ? ` +
					`{"unit": "hour", "requestsPerUnit": int(apiKey.tokenBudget)} : ` +
					`{"unit": "hour", "requestsPerUnit": 100000}`))
		})

		It("carries the window and fallback it was given", func() {
			spec := renderPolicySpec(agentgatewayv1alpha1.FailClosed, "agentgateway-system", "",
				"day", int64(250000))

			Expect(limitOverride(spec)).To(Equal(
				`has(apiKey.tokenBudget) ? ` +
					`{"unit": "day", "requestsPerUnit": int(apiKey.tokenBudget)} : ` +
					`{"unit": "day", "requestsPerUnit": 250000}`))
		})
	})

	Describe("the policy target", func() {
		// targetRefs has no namespace field: the target must be in the policy's
		// own namespace, which is the whole reason registrations cannot live
		// beside the attendee's pod (ADR-0002).
		It("names the Gateway this operator creates", func() {
			spec := renderPolicySpec(agentgatewayv1alpha1.FailClosed, "agentgateway-system", "", defaultBudgetWindow, agentgatewayv1alpha1.DefaultTokenBudget)

			refs, ok := spec["targetRefs"].([]any)
			Expect(ok).To(BeTrue())
			Expect(refs).To(HaveLen(1))

			ref, _ := refs[0].(map[string]any)
			Expect(ref["group"]).To(Equal(GatewayAPIGroup))
			Expect(ref["kind"]).To(Equal(KindGateway))
			Expect(ref["name"]).To(Equal(GatewayName))
			Expect(ref).NotTo(HaveKey("namespace"),
				"targetRefs takes no namespace; adding one is silently ignored")
		})
	})
})
