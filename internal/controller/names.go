package controller

// Fixed names for everything this operator creates.
//
// Release names are constants rather than derived from the resource, because the
// resource is a singleton: there is exactly one of each per cluster, and a
// derived name would only invite two operators to disagree about it.
const (
	// AgentgatewayReleaseName is the Helm release for the agentgateway control
	// plane.
	AgentgatewayReleaseName = "agentgateway"

	// AgentgatewayCRDsReleaseName is the Helm release for agentgateway's CRDs.
	// Installed first: the control plane comes up but never becomes ready
	// without them, which is a silent failure (ADR-0005).
	AgentgatewayCRDsReleaseName = "agentgateway-crds"

	// RateLimitReleaseName is the Helm release for the Envoy rate-limit service
	// and its counter store.
	RateLimitReleaseName = "agentgateway-ratelimit"
)

const (
	// GatewayClassName is the GatewayClass agentgateway's own controller
	// creates, after leader election. This operator waits for it and never
	// creates it (ADR-0005).
	GatewayClassName = "agentgateway"

	// GatewayName is the Gateway this operator creates. Creating it is what
	// causes agentgateway to provision data-plane pods.
	GatewayName = "agentgateway"

	// GatewayListenerName is the name of the Gateway's only listener.
	GatewayListenerName = "llm"

	// GatewayPort is the port the data plane serves LLM traffic on.
	GatewayPort = 4000

	// ParametersName is the AgentgatewayParameters overlay attached to the
	// GatewayClass, which forces the data-plane Service to ClusterIP.
	ParametersName = "agentgateway-educates"

	// PolicyName is the single API-key policy. Exactly one: two policies
	// targeting one Gateway silently overwrite each other with no error
	// surfaced (ADR-0002).
	PolicyName = "agentgateway-educates-apikeys"

	// RateLimitDomain is the rate-limit domain descriptors are keyed under.
	RateLimitDomain = "agentgateway"

	// RateLimitServiceName is the Service the policy sends rate-limit checks to.
	RateLimitServiceName = "agentgateway-ratelimit"

	// RateLimitPort is the rate-limit service's gRPC port.
	RateLimitPort = 8081
)

// agentgateway's own API group, for the resources this operator renders.
const (
	AgentgatewayGroup   = "agentgateway.dev"
	AgentgatewayVersion = "v1alpha1"

	KindAgentgatewayPolicy     = "AgentgatewayPolicy"
	KindAgentgatewayModel      = "AgentgatewayModel"
	KindAgentgatewayParameters = "AgentgatewayParameters"
)

// Gateway API group, for the Gateway and GatewayClass.
const (
	GatewayAPIGroup   = "gateway.networking.k8s.io"
	GatewayAPIVersion = "v1"

	KindGateway      = "Gateway"
	KindGatewayClass = "GatewayClass"
)

// ManagedByLabel marks every object this operator creates, so a cluster
// operator can find them and so a human's own labels are not fought over.
const (
	ManagedByLabel = "app.kubernetes.io/managed-by"
	ManagedByValue = "agentgateway-educates-operator"
)
