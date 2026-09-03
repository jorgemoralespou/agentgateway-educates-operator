package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ModelProvider is an LLM provider agentgateway can reach.
//
// These values are passed through to AgentgatewayModel.spec.provider, so the
// casing is agentgateway's, not ours — `OpenAI`, not `openai`. A mismatch is
// rejected by the CRD's own validation with an unhelpful message, so the enum is
// restated here to catch it at the catalog instead.
// +kubebuilder:validation:Enum=Anthropic;Azure;Baseten;Bedrock;Cerebras;Cohere;Deepinfra;Deepseek;Fireworks;Gemini;Groq;Huggingface;Mistral;Ollama;OpenAI;Openrouter;TogetherAI;VertexAI;XAI
type ModelProvider string

const (
	ProviderAnthropic  ModelProvider = "Anthropic"
	ProviderOpenAI     ModelProvider = "OpenAI"
	ProviderGemini     ModelProvider = "Gemini"
	ProviderGroq       ModelProvider = "Groq"
	ProviderMistral    ModelProvider = "Mistral"
	ProviderOllama     ModelProvider = "Ollama"
	ProviderTogetherAI ModelProvider = "TogetherAI"
)

// CredentialReference names a Secret holding a provider credential.
//
// A name and key only: no credential material appears in this resource, so a
// catalog stays safe to commit.
type CredentialReference struct {
	// Name of the Secret, in the gateway namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Key within the Secret holding the credential.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:default=api-key
	// +optional
	Key string `json:"key,omitempty"`
}

// CatalogModel is one name an attendee may address, bound to a provider and an
// upstream model.
//
// The catalog name is deliberately not the upstream model's name, so the binding
// can change without touching workshop content.
type CatalogModel struct {
	// Name is what attendees address — `fast`, `smart`. Never the provider's own
	// model name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Provider is the LLM provider serving this model.
	Provider ModelProvider `json:"provider"`

	// Model is the provider's own model name — `gpt-4o-mini`,
	// `claude-sonnet-4-0`. Never visible to attendees.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	Model string `json:"model"`

	// CredentialRef names the Secret holding this provider's real API key.
	CredentialRef CredentialReference `json:"credentialRef"`

	// BaseURL overrides the provider's address. Required by agentgateway for
	// the Ollama provider.
	// +kubebuilder:validation:Pattern=`^https?://`
	// +optional
	BaseURL string `json:"baseURL,omitempty"`
}

// AgentGatewayCatalogSpec declares the models the Gateway offers.
type AgentGatewayCatalogSpec struct {
	// Models is the flat list of models workshops may use.
	//
	// Deliberately no failover, weighted routing, or per-model overrides: all
	// are additive later, and none is needed to teach against an LLM.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	// +listType=map
	// +listMapKey=name
	Models []CatalogModel `json:"models"`
}

// CatalogPhase is an advisory summary. Conditions are authoritative.
// +kubebuilder:validation:Enum=Pending;Rendering;Ready;Failed
type CatalogPhase string

const (
	CatalogPending   CatalogPhase = "Pending"
	CatalogRendering CatalogPhase = "Rendering"
	CatalogReady     CatalogPhase = "Ready"
	CatalogFailed    CatalogPhase = "Failed"
)

// AgentGatewayCatalogStatus reports what the catalog rendered.
type AgentGatewayCatalogStatus struct {
	// Phase is an advisory summary. Conditions are authoritative.
	// +optional
	Phase CatalogPhase `json:"phase,omitempty"`

	// Conditions are authoritative.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the .metadata.generation this status was computed
	// from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// GatewayURL is read from the platform's status, never reconstructed.
	// +optional
	GatewayURL string `json:"gatewayURL,omitempty"`

	// AvailableModels lists the catalog names an author may reference, so they
	// can find out what exists without reading the spec of a resource they may
	// not have access to.
	// +listType=atomic
	// +optional
	AvailableModels []string `json:"availableModels,omitempty"`
}

// AgentGatewayCatalog declares the models the Gateway offers.
//
// Configures a Gateway; never installs one — that is AgentGatewayPlatform's job,
// and this resource waits for it to be ready.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:path=agentgatewaycatalogs,scope=Cluster,shortName=agwcatalog
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Models",type=string,JSONPath=`.status.availableModels`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'cluster'",message="AgentGatewayCatalog is a singleton and must be named 'cluster'"
type AgentGatewayCatalog struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec AgentGatewayCatalogSpec `json:"spec,omitempty"`
	// +optional
	Status AgentGatewayCatalogStatus `json:"status,omitempty"`
}

// AgentGatewayCatalogList contains a list of AgentGatewayCatalog.
// +kubebuilder:object:root=true
type AgentGatewayCatalogList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentGatewayCatalog `json:"items"`
}

// CredentialKey returns the Secret key holding the credential, defaulted.
func (m CatalogModel) CredentialKey() string {
	if m.CredentialRef.Key != "" {
		return m.CredentialRef.Key
	}
	return "api-key"
}

func init() {
	register(&AgentGatewayCatalog{}, &AgentGatewayCatalogList{})
}
