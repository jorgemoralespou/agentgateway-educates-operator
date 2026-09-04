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
	//
	// Deliberately not "agentgateway": agentgateway names the data-plane
	// Deployment, Service and ServiceAccount after the Gateway, verbatim and in
	// the Gateway's own namespace. A Gateway called "agentgateway" in
	// agentgateway-system therefore collides head-on with the control-plane
	// objects of the "agentgateway" Helm release this operator installs into
	// that same namespace. The collision is unrecoverable rather than merely
	// untidy: a Deployment's spec.selector is immutable, the two selectors
	// differ, and the data plane retries the rejected apply forever while the
	// Gateway sits at Programmed=False.
	GatewayName = "agentgateway-educates"

	// LegacyGatewayName is the name GatewayName used to have, removed on sight.
	//
	// A cluster that ran an earlier build still has this Gateway, wedged at
	// Programmed=False by the collision described above, and it does not heal
	// on its own: the rename leaves it behind rather than renaming it in
	// place. See pruneLegacyGateway, which only ever deletes one carrying this
	// operator's own managed-by label.
	LegacyGatewayName = "agentgateway"

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

	// KindHTTPRoute is named only in the listener's allowedRoutes, to preserve
	// a default that setting allowedRoutes.kinds would otherwise revoke. This
	// operator creates no HTTPRoute, models attach to the listener directly.
	KindHTTPRoute = "HTTPRoute"
)

// ManagedByLabel marks every object this operator creates, so a cluster
// operator can find them and so a human's own labels are not fought over.
const (
	ManagedByLabel = "app.kubernetes.io/managed-by"
	ManagedByValue = "agentgateway-educates-operator"
)
