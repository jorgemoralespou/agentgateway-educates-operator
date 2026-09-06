package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ModelProvider is an LLM provider agentgateway can reach.
//
// These values are passed through to AgentgatewayModel.spec.provider, so the
// casing is agentgateway's, not ours: `OpenAI`, not `openai`. A mismatch is
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
	// Name is what attendees address, `fast`, `smart`. Never the provider's own
	// model name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Provider is the LLM provider serving this model.
	Provider ModelProvider `json:"provider"`

	// Model is the provider's own model name, `gpt-4o-mini`,
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

	// RateLimit configures how token-budget enforcement behaves.
	//
	// It lives on the catalog rather than the platform because it is a choice
	// about serving LLM traffic, which is what the catalog governs, and the
	// platform's job is installing the gateway rather than deciding how it
	// behaves under load.
	// +optional
	RateLimit *RateLimitSpec `json:"rateLimit,omitempty"`

	// Budgets declares the cluster-wide budget policy every grant inherits
	// from.
	//
	// On the catalog for the same reason the rate-limit failure mode is: it is
	// a choice about serving LLM traffic, which is what the catalog governs,
	// and the person who owns the provider credential and pays the invoice is
	// the person who edits this object.
	// +optional
	Budgets *BudgetSpec `json:"budgets,omitempty"`

	// RequestTimeout is how long the gateway waits for a complete response from
	// an upstream model.
	//
	// Exists for slow models rather than as a tuning knob. A reasoning model
	// served locally by Ollama can take well over a minute for one reply, past
	// agentgateway's own default, and the attendee sees `upstream call failed:
	// connection closed before message completed`, which reads like a broken
	// gateway, not a slow model.
	//
	// A Go duration string: "90s", "3m". Left unset, agentgateway's default
	// applies, which is right for every hosted provider.
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ms|s|m|h))+$`
	// +optional
	RequestTimeout string `json:"requestTimeout,omitempty"`
}

// BudgetSpec is the cluster-wide budget policy grants inherit from.
//
// Every field is optional and absent means "no opinion", so a catalog that
// declares no budgets behaves exactly as one written before this block
// existed.
type BudgetSpec struct {
	// DefaultTokenBudget is the ceiling applied to a grant that does not ask
	// for a specific one.
	//
	// Changing it takes effect for running sessions that set no budget of
	// their own, without any grant being reconciled. That falls out of how the
	// value reaches the gateway rather than from a watch: an inheriting grant's
	// registration carries no budget metadata at all, so the gateway falls
	// through to the descriptor row in the shared rate-limit configuration,
	// and this field is what renders that row.
	// +kubebuilder:validation:Minimum=1
	// +optional
	DefaultTokenBudget *int64 `json:"defaultTokenBudget,omitempty"`

	// MaxTokenBudget is the most any grant may be enforced at.
	//
	// This is the trust boundary: the person who owns the provider credential
	// and pays for it decides the ceiling, and a workshop author writing
	// session.objects cannot exceed it.
	//
	// A grant asking for more is clamped, not rejected. Rejecting would fail
	// every attendee's session at start, where clamping means the workshop
	// still runs, at a budget the operator is willing to pay for. The clamp is
	// reported on the grant's status so an author can see their requested value
	// did not survive, rather than spending a workshop wondering why attendees
	// hit a limit earlier than planned.
	//
	// Left unset, nothing is clamped, so this stays opt-in for operators who do
	// not need it.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxTokenBudget *int64 `json:"maxTokenBudget,omitempty"`

	// DefaultCostBudget is the spend ceiling applied to a grant that does not
	// ask for a specific one, in US dollars.
	//
	// A decimal string for the same reason the grant's is: no floating-point
	// field belongs in a custom resource. Unset means grants inherit no cost
	// ceiling, and the token budget alone applies to them.
	// +kubebuilder:validation:Pattern=`^(0\.[0-9]*[1-9][0-9]*|[1-9][0-9]*(\.[0-9]+)?)$`
	// +optional
	DefaultCostBudget string `json:"defaultCostBudget,omitempty"`

	// MaxCostBudget is the most any grant may spend in one window, in US
	// dollars.
	//
	// The cost half of the same trust boundary MaxTokenBudget draws, and it
	// clamps rather than rejects for the same reason. Left unset, nothing is
	// clamped.
	// +kubebuilder:validation:Pattern=`^(0\.[0-9]*[1-9][0-9]*|[1-9][0-9]*(\.[0-9]+)?)$`
	// +optional
	MaxCostBudget string `json:"maxCostBudget,omitempty"`
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
// Configures a Gateway; never installs one: that is AgentGatewayPlatform's job,
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

// FailureMode returns the configured rate-limit failure mode, defaulted.
//
// Defaulted to FailClosed: agentgateway's CRD declares no schema default, so an
// omitted value would leave the field absent from the rendered policy and leave
// the choice to the data plane. For a workshop, an outage that silently removes
// budget enforcement is worse than one that visibly stops traffic (ADR-0003),
// but it stays a cluster-operator decision, not this operator's.
func (c *AgentGatewayCatalog) FailureMode() RateLimitFailureMode {
	if c.Spec.RateLimit != nil && c.Spec.RateLimit.FailureMode != "" {
		return c.Spec.RateLimit.FailureMode
	}
	return FailClosed
}
