package controller

import (
	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
)

// Values renderers emit only the keys that differ from the chart's own
// defaults.
//
// Every knob is a typed field mapped by hand and there is no passthrough of
// arbitrary chart values, following v4 — which removed generic operational knobs
// when they did not hold against the real charts. Emitting only non-default keys
// also keeps the converge fingerprint small and stable: a chart default that
// changes upstream does not read as drift here.

// renderAgentgatewayValues renders values for the agentgateway control-plane
// chart.
//
// Near-empty by design: the chart's defaults are already right for a workshop
// cluster — one controller replica, a ClusterIP control-plane Service, info
// logging. The data-plane Service type, which is *not* right by default, is not
// a chart value at all: it is set through an AgentgatewayParameters overlay
// referenced from the Gateway, because the data plane is rendered per Gateway by
// agentgateway's own controller rather than by this chart (ADR-0005, ADR-0007).
//
// The one exception is the AgentgatewayModel API, which the chart ships disabled
// (values.yaml `agentgatewayModels.enabled: false`, surfacing as
// AGW_ENABLE_AGENTGATEWAY_MODELS on the controller). This operator's whole model
// story is built on that API — the catalog renders AgentgatewayModels and
// nothing else — so leaving it off makes every request 404 with "route not
// found" while every resource still reports healthy. Not a user choice: a
// platform with it off has no working LLM path at all.
func renderAgentgatewayValues(_ *agentgatewayv1alpha1.AgentGatewayPlatform) map[string]any {
	return map[string]any{
		"agentgatewayModels": map[string]any{
			"enabled": true,
		},
	}
}

// renderAgentgatewayCRDsValues renders values for the CRDs chart.
//
// The chart takes no configuration that matters here; it exists to establish
// the CRDs before the control plane starts.
func renderAgentgatewayCRDsValues(_ *agentgatewayv1alpha1.AgentGatewayPlatform) map[string]any {
	return map[string]any{}
}
