package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AgentGatewayProvider selects whether this operator installs the gateway or
// configures one somebody else runs.
//
// This is an enum rather than a boolean in the v4 house style: the value encodes
// both whether we install and by what mechanism, with one optional sibling
// struct per variant. A boolean would not leave room for a third variant.
// +kubebuilder:validation:Enum=BundledAgentgateway;ExternalAgentgateway
type AgentGatewayProvider string

const (
	// ProviderBundled means this operator installs and reconciles the gateway.
	// The default.
	ProviderBundled AgentGatewayProvider = "BundledAgentgateway"

	// ProviderExternal means the gateway is installed by someone else. This
	// operator configures it and never upgrades, uninstalls, or takes it over.
	ProviderExternal AgentGatewayProvider = "ExternalAgentgateway"
)

// GatewayAPIProvider selects whether this operator installs the Gateway API
// CRDs. Skipping matters on a cluster where an ingress controller already owns
// them, which this operator must not fight.
// +kubebuilder:validation:Enum=Managed;Existing
type GatewayAPIProvider string

const (
	// GatewayAPIManaged means this operator applies the Gateway API CRDs.
	GatewayAPIManaged GatewayAPIProvider = "Managed"

	// GatewayAPIExisting means the CRDs are already present and this operator
	// leaves them alone.
	GatewayAPIExisting GatewayAPIProvider = "Existing"
)

// RateLimitFailureMode decides what happens to LLM traffic when the rate-limit
// service is unreachable.
//
// Deliberately a cluster-operator choice (ADR-0003): FailClosed makes an outage
// visibly stop traffic, FailOpen makes it silently remove all budget
// enforcement. For a workshop the former is usually the lesser evil, but it is
// not this operator's call to make.
// +kubebuilder:validation:Enum=FailClosed;FailOpen
type RateLimitFailureMode string

const (
	FailClosed RateLimitFailureMode = "FailClosed"
	FailOpen   RateLimitFailureMode = "FailOpen"
)

// BundledAgentgatewaySpec configures the gateway this operator installs.
type BundledAgentgatewaySpec struct {
	// Namespace is where agentgateway, its Gateway, and every key registration
	// live. Significant beyond placement: agentgateway resolves API-key
	// credentials only within its own namespace (ADR-0002), so this is the
	// namespace registrations must be written to.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:default=agentgateway-system
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// ExternalAgentgatewaySpec references a gateway this operator did not install.
type ExternalAgentgatewaySpec struct {
	// Namespace is the namespace of the existing Gateway. Key registrations are
	// written here, so the operator must be able to write to it.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace"`

	// GatewayName is the name of the existing Gateway resource.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	GatewayName string `json:"gatewayName"`

	// GatewayURL is the base URL sessions use to reach the gateway. Set when the
	// operator cannot derive it, which it can only do for a Gateway it installed.
	// +kubebuilder:validation:MinLength=1
	// +optional
	GatewayURL string `json:"gatewayURL,omitempty"`
}

// RateLimitSpec configures the token-budget enforcement path.
type RateLimitSpec struct {
	// FailureMode decides whether a rate-limit service outage stops LLM traffic
	// or removes enforcement. Always written explicitly into the rendered
	// policy: the agentgateway CRD declares no schema default, so an omitted
	// value leaves the field absent and only the data plane applies FailClosed.
	// +kubebuilder:default=FailClosed
	// +optional
	FailureMode RateLimitFailureMode `json:"failureMode,omitempty"`
}

// AgentGatewayPlatformSpec declares the running gateway.
type AgentGatewayPlatformSpec struct {
	// Provider selects whether this operator installs the gateway or configures
	// one that already exists.
	// +kubebuilder:default=BundledAgentgateway
	// +optional
	Provider AgentGatewayProvider `json:"provider,omitempty"`

	// GatewayAPI selects whether this operator installs the Gateway API CRDs.
	// +kubebuilder:default=Managed
	// +optional
	GatewayAPI GatewayAPIProvider `json:"gatewayAPI,omitempty"`

	// BundledAgentgateway configures the gateway this operator installs. Ignored
	// unless provider is BundledAgentgateway.
	// +optional
	BundledAgentgateway *BundledAgentgatewaySpec `json:"bundledAgentgateway,omitempty"`

	// ExternalAgentgateway references a gateway installed by someone else.
	// Required when provider is ExternalAgentgateway.
	// +optional
	ExternalAgentgateway *ExternalAgentgatewaySpec `json:"externalAgentgateway,omitempty"`
}

// PlatformPhase is an advisory summary of the install. Conditions are
// authoritative.
// +kubebuilder:validation:Enum=Pending;Installing;Ready;Failed;Uninstalling
type PlatformPhase string

const (
	PlatformPending      PlatformPhase = "Pending"
	PlatformInstalling   PlatformPhase = "Installing"
	PlatformReady        PlatformPhase = "Ready"
	PlatformFailed       PlatformPhase = "Failed"
	PlatformUninstalling PlatformPhase = "Uninstalling"
)

// ChartVersions reports what is actually installed, so a cluster operator can
// see it without cracking open the operator image.
type ChartVersions struct {
	// Agentgateway is the version of the agentgateway chart.
	// +optional
	Agentgateway string `json:"agentgateway,omitempty"`

	// AgentgatewayCRDs is the version of the agentgateway-crds chart.
	// +optional
	AgentgatewayCRDs string `json:"agentgatewayCRDs,omitempty"`
}

// AgentGatewayPlatformStatus is the published contract every other resource
// here reads.
type AgentGatewayPlatformStatus struct {
	// Phase is an advisory summary. Conditions are authoritative.
	// +optional
	Phase PlatformPhase `json:"phase,omitempty"`

	// Conditions are authoritative.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the .metadata.generation this status was computed
	// from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// GatewayURL is where sessions reach the gateway, over in-cluster DNS. Every
	// other resource reads the address from here rather than reconstructing it.
	// +optional
	GatewayURL string `json:"gatewayURL,omitempty"`

	// GatewayNamespace is where the Gateway and its key registrations live.
	// +optional
	GatewayNamespace string `json:"gatewayNamespace,omitempty"`

	// BundledChartVersions reports the installed chart versions.
	// +optional
	BundledChartVersions *ChartVersions `json:"bundledChartVersions,omitempty"`
}

// AgentGatewayPlatform installs and owns the running gateway.
//
// Cluster-scoped singleton named `cluster`, modelled on EducatesClusterConfig
// and enforced by a CEL rule the way the v4 installer does.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:path=agentgatewayplatforms,scope=Cluster,shortName=agwplatform
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Gateway URL",type=string,JSONPath=`.status.gatewayURL`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'cluster'",message="AgentGatewayPlatform is a singleton and must be named 'cluster'"
type AgentGatewayPlatform struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec AgentGatewayPlatformSpec `json:"spec,omitempty"`
	// +optional
	Status AgentGatewayPlatformStatus `json:"status,omitempty"`
}

// AgentGatewayPlatformList contains a list of AgentGatewayPlatform.
// +kubebuilder:object:root=true
type AgentGatewayPlatformList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentGatewayPlatform `json:"items"`
}

// GatewayNamespace returns the namespace the gateway runs in for either
// provider, so callers do not branch on the provider enum to find it.
func (p *AgentGatewayPlatform) GatewayNamespace() string {
	if p.Spec.Provider == ProviderExternal {
		if p.Spec.ExternalAgentgateway != nil {
			return p.Spec.ExternalAgentgateway.Namespace
		}
		return ""
	}
	if p.Spec.BundledAgentgateway != nil && p.Spec.BundledAgentgateway.Namespace != "" {
		return p.Spec.BundledAgentgateway.Namespace
	}
	return DefaultGatewayNamespace
}

// DefaultGatewayNamespace is where the bundled gateway is installed when the
// spec does not say otherwise.
const DefaultGatewayNamespace = "agentgateway-system"

// SingletonName is the only permitted name for the cluster-scoped singletons in
// this group.
const SingletonName = "cluster"

func init() {
	register(&AgentGatewayPlatform{}, &AgentGatewayPlatformList{})
}
