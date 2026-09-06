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
			spec := renderPolicySpec(agentgatewayv1alpha1.FailClosed, "agentgateway-system", "180s", string(agentgatewayv1alpha1.DefaultBudgetWindow), agentgatewayv1alpha1.DefaultTokenBudget)

			timeouts, ok := traffic(spec)["timeouts"].(map[string]any)
			Expect(ok).To(BeTrue(), "a catalog requestTimeout must reach the policy")
			Expect(timeouts["request"]).To(Equal("180s"))
		})

		// Writing agentgateway's own default back at it would pin a value this
		// operator has no opinion about, and would need changing every time
		// that default moved.
		It("is absent when the catalog does not ask, leaving agentgateway's default", func() {
			spec := renderPolicySpec(agentgatewayv1alpha1.FailClosed, "agentgateway-system", "", string(agentgatewayv1alpha1.DefaultBudgetWindow), agentgatewayv1alpha1.DefaultTokenBudget)

			Expect(traffic(spec)).NotTo(HaveKey("timeouts"),
				"an unset timeout must not be written at all")
		})

		It("does not disturb the rest of the traffic block", func() {
			spec := renderPolicySpec(agentgatewayv1alpha1.FailOpen, "agentgateway-system", "90s", string(agentgatewayv1alpha1.DefaultBudgetWindow), agentgatewayv1alpha1.DefaultTokenBudget)

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

		// The window is read off each attendee's own registration, because the
		// policy is one object cluster-wide and cannot hold a row per attendee.
		It("reads the window off the registration, falling back to the rendered default", func() {
			spec := renderPolicySpec(agentgatewayv1alpha1.FailClosed, "agentgateway-system", "",
				string(agentgatewayv1alpha1.DefaultBudgetWindow), agentgatewayv1alpha1.DefaultTokenBudget)

			Expect(limitOverride(spec)).To(Equal(
				`has(apiKey.tokenBudget) ? ` +
					`{"unit": (has(apiKey.budgetWindow) ? apiKey.budgetWindow : "day"), ` +
					`"requestsPerUnit": int(apiKey.tokenBudget)} : ` +
					`{"unit": (has(apiKey.budgetWindow) ? apiKey.budgetWindow : "day"), ` +
					`"requestsPerUnit": 100000}`))
		})

		// A registration written before the window existed carries none, and
		// must not produce an empty unit the rate-limit service would reject.
		It("carries the window and fallback it was given", func() {
			spec := renderPolicySpec(agentgatewayv1alpha1.FailClosed, "agentgateway-system", "",
				"hour", int64(250000))

			Expect(limitOverride(spec)).To(ContainSubstring(`: "hour")`),
				"the rendered default is the fallback for a registration with no window")
			Expect(limitOverride(spec)).To(ContainSubstring(`"requestsPerUnit": 250000`))
		})
	})

	Describe("the policy target", func() {
		// targetRefs has no namespace field: the target must be in the policy's
		// own namespace, which is the whole reason registrations cannot live
		// beside the attendee's pod (ADR-0002).
		It("names the Gateway this operator creates", func() {
			spec := renderPolicySpec(agentgatewayv1alpha1.FailClosed, "agentgateway-system", "", string(agentgatewayv1alpha1.DefaultBudgetWindow), agentgatewayv1alpha1.DefaultTokenBudget)

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
