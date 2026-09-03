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
// Deliberately near-empty: the chart's defaults are already right for a
// workshop cluster — one controller replica, a ClusterIP control-plane Service,
// info logging. The data-plane Service type, which is *not* right by default,
// is not a chart value at all: it is set through an AgentgatewayParameters
// overlay on the GatewayClass, because the data plane is rendered per Gateway by
// agentgateway's own controller rather than by this chart (ADR-0005).
func renderAgentgatewayValues(_ *agentgatewayv1alpha1.AgentGatewayPlatform) map[string]any {
	return map[string]any{}
}

// renderAgentgatewayCRDsValues renders values for the CRDs chart.
//
// The chart takes no configuration that matters here; it exists to establish
// the CRDs before the control plane starts.
func renderAgentgatewayCRDsValues(_ *agentgatewayv1alpha1.AgentGatewayPlatform) map[string]any {
	return map[string]any{}
}
