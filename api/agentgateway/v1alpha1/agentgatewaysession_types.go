package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CatalogReference names the catalog a session draws its models from.
type CatalogReference struct {
	// Name of the AgentGatewayCatalog. Cluster-scoped, so no namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:default=cluster
	// +optional
	Name string `json:"name,omitempty"`
}

// AgentGatewaySessionSpec declares one attendee's access to the Gateway.
//
// This is the resource a workshop author writes into session.objects. It must
// carry `namespace: $(workshop_namespace)`: placement is load-bearing and
// asymmetric (ADR-0002), and a grant in a session namespace is rejected rather
// than reconciled.
type AgentGatewaySessionSpec struct {
	// CatalogRef names the catalog this session's models come from.
	// +optional
	CatalogRef CatalogReference `json:"catalogRef,omitempty"`

	// TokenBudget is the ceiling on LLM tokens for this session's lifetime.
	//
	// Measured in tokens rather than requests because cost tracks tokens: one
	// request with a large context can cost more than a hundred small ones.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=100000
	// +optional
	TokenBudget int64 `json:"tokenBudget,omitempty"`

	// TTL is a backstop expiry on the participant key, independent of any
	// cleanup path.
	//
	// Not optional in spirit: force-deleting a namespace strips finalizers and
	// orphans the registration outright, and this is the only protection in that
	// case (ADR-0002).
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(s|m|h))+$`
	// +kubebuilder:default="4h"
	// +optional
	TTL string `json:"ttl,omitempty"`
}

// SessionPhase is an advisory summary. Conditions are authoritative.
// +kubebuilder:validation:Enum=Pending;Ready;Failed;Rejected;Terminating
type SessionPhase string

const (
	SessionPending     SessionPhase = "Pending"
	SessionReady       SessionPhase = "Ready"
	SessionFailed      SessionPhase = "Failed"
	SessionRejected    SessionPhase = "Rejected"
	SessionTerminating SessionPhase = "Terminating"
)

// SecretReference names the Secret holding the participant key.
type SecretReference struct {
	// Name of the Secret, in the workshop namespace.
	Name string `json:"name"`
}

// AgentGatewaySessionStatus reports the session's wiring.
//
// Never carries the participant key or its hash, so it is safe to paste into a
// support conversation.
type AgentGatewaySessionStatus struct {
	// Phase is an advisory summary. Conditions are authoritative.
	// +optional
	Phase SessionPhase `json:"phase,omitempty"`

	// Conditions are authoritative.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the .metadata.generation this status was computed
	// from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// SecretRef names the Secret holding this attendee's key, so the wiring can
	// be checked by hand.
	// +optional
	SecretRef *SecretReference `json:"secretRef,omitempty"`

	// GatewayURL is the base URL written into that Secret.
	// +optional
	GatewayURL string `json:"gatewayURL,omitempty"`

	// ExpiresAt is when the participant key stops working regardless of whether
	// any cleanup ran.
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`
}

// AgentGatewaySession is one attendee's access to the Gateway for the duration
// of one session.
//
// Named for what it confers rather than for the credential that implements it:
// the key is an implementation detail, and a grant outlives any particular key
// when the key is rotated.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:path=agentgatewaysessions,scope=Namespaced,shortName=agwsession
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Secret",type=string,JSONPath=`.status.secretRef.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type AgentGatewaySession struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec AgentGatewaySessionSpec `json:"spec,omitempty"`
	// +optional
	Status AgentGatewaySessionStatus `json:"status,omitempty"`
}

// AgentGatewaySessionList contains a list of AgentGatewaySession.
// +kubebuilder:object:root=true
type AgentGatewaySessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentGatewaySession `json:"items"`
}

const (
	// SessionFinalizer removes the key registration from the gateway namespace.
	// Bounded by a timeout: a wedged finalizer holds the whole session namespace
	// in Terminating, which is worse than a leaked hash (ADR-0002).
	SessionFinalizer = "agentgateway.operators.educates.dev/registration"

	// SecretSuffix is appended to the session name to form the Secret and
	// registration names. Workshop content references
	// `$(session_name)-agentgateway`, so this string is part of the author-facing
	// contract and must not change.
	SecretSuffix = "-agentgateway"

	// SecretKeyAPIKey holds the participant key plaintext.
	SecretKeyAPIKey = "api-key"

	// SecretKeyBaseURL holds the gateway base URL.
	SecretKeyBaseURL = "base-url"

	// RegistrationLabel marks a ConfigMap as a key registration, and is what the
	// single API-key policy selects on.
	RegistrationLabel = "agentgateway.dev/apikey"

	// SessionLabel records which session a registration belongs to, so leaked
	// registrations can be found and removed.
	SessionLabel = "agentgateway.operators.educates.dev/session"

	// SessionNamespaceLabel records the namespace the grant was created in.
	SessionNSLabel = "agentgateway.operators.educates.dev/session-namespace"
)

// CatalogName returns the referenced catalog name, defaulted to the singleton.
func (s *AgentGatewaySession) CatalogName() string {
	if s.Spec.CatalogRef.Name != "" {
		return s.Spec.CatalogRef.Name
	}
	return SingletonName
}

// ResourceName is the name of both the participant key Secret and the key
// registration ConfigMap. They share a name because they are two halves of one
// thing, distinguished by namespace.
func (s *AgentGatewaySession) ResourceName() string {
	return s.Name + SecretSuffix
}

// DefaultTokenBudget is the ceiling applied when a grant does not set one.
// Matches the CRD's own default, so the two cannot drift.
const DefaultTokenBudget int64 = 100000

// DefaultTTL is the backstop expiry applied when a grant does not set one.
// Matches the CRD's own default, so the two cannot drift.
const DefaultTTL = "4h"

// TokenBudget returns the session's token ceiling, defaulted.
//
// Defaulted here as well as in the CRD because a grant created before the
// default existed, or through a client that strips zero values, would otherwise
// register a budget of zero — which the gateway would read as "no tokens at
// all" and reject every request the attendee makes.
func (s *AgentGatewaySession) TokenBudget() int64 {
	if s.Spec.TokenBudget > 0 {
		return s.Spec.TokenBudget
	}
	return DefaultTokenBudget
}

func init() {
	register(&AgentGatewaySession{}, &AgentGatewaySessionList{})
}
