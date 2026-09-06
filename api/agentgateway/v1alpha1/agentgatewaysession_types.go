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

	// TokenBudget is the ceiling on LLM tokens for one budget window, which
	// defaults to a day. See BudgetWindow.
	//
	// Not a ceiling for the session's lifetime: no lifetime-scoped budget
	// exists in this stack. The window is what is actually enforced, and it is
	// chosen to be longer than a workshop so that in practice one budget covers
	// one session.
	//
	// Measured in tokens rather than requests because cost tracks tokens: one
	// request with a large context can cost more than a hundred small ones.
	//
	// An override, not a setting: leave it unset and the session inherits the
	// ordinary budget the cluster operator configured. A pointer with no schema
	// default so that "unset" survives the round trip through the API server;
	// with a stamped default the controller cannot tell an omitted field from
	// one asking for exactly the default, and no inherited value could ever
	// take effect.
	//
	// Zero is not a legal value. A client that strips zero values would
	// otherwise register a ceiling of no tokens at all, and every request the
	// attendee makes would be rejected.
	// +kubebuilder:validation:Minimum=1
	// +optional
	TokenBudget *int64 `json:"tokenBudget,omitempty"`

	// CostBudget is the ceiling on spend for one budget window, in US dollars.
	//
	// A decimal string, "0.50", not a number: a floating-point field in a
	// custom resource would admit representation errors into a value compared
	// for equality. It is parsed exactly and enforced in micro-dollars.
	//
	// Where TokenBudget counts every model's tokens the same, this charges each
	// request what the provider actually bills for it, so an expensive model
	// drains the budget faster than a cheap one and the ceiling means the same
	// thing whichever model an attendee picks.
	//
	// A cost budget is enforced *alongside* the token budget, not instead of
	// it: whichever runs out first stops the attendee. The token budget stays
	// the backstop, because a cost expression can fail to evaluate and be
	// skipped silently.
	//
	// Unset means no cost ceiling, and the token budget alone applies.
	// +kubebuilder:validation:Pattern=`^[0-9]+\.?[0-9]*$`
	// +optional
	CostBudget string `json:"costBudget,omitempty"`

	// BudgetWindow is how long one budget lasts before it refills. It governs
	// the token budget and the cost budget alike, so the two always cover the
	// same span.
	//
	// Defaults to a day, which is longer than any workshop, so in practice an
	// attendee gets one budget for their whole session. Before this field
	// existed the window was an hour, so an attendee in a two-hour workshop
	// silently received two full budgets.
	//
	// Deliberately separate from TTL, which is the key's expiry backstop and
	// keeps its own free-form duration. The two answer different questions:
	// how long a budget lasts, and how long a key works at all. Restricting TTL
	// to these units was considered and rejected, since it would make the
	// current default illegal and trade the expiry guarantee for the rate
	// limiter's vocabulary.
	//
	// One residual is accepted rather than engineered around: windows are
	// aligned to the Unix epoch, not to a session's first request. A daily
	// window resets at midnight UTC, so a workshop spanning midnight yields two
	// budgets. The guarantee is "at most one reset", not "no reset", and the
	// expiry sweep bounds the exposure because a session past its TTL cannot
	// spend the second budget.
	// +kubebuilder:default=day
	// +optional
	BudgetWindow BudgetWindow `json:"budgetWindow,omitempty"`

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

// BudgetWindow is how long one budget lasts before it refills.
//
// The legal values are exactly the units the rate-limit service accepts, so
// what an author writes reaches the descriptor unchanged rather than being
// translated into a vocabulary the enforcement does not share.
// +kubebuilder:validation:Enum=second;minute;hour;day;month;year
type BudgetWindow string

const (
	BudgetWindowSecond BudgetWindow = "second"
	BudgetWindowMinute BudgetWindow = "minute"
	BudgetWindowHour   BudgetWindow = "hour"
	BudgetWindowDay    BudgetWindow = "day"
	BudgetWindowMonth  BudgetWindow = "month"
	BudgetWindowYear   BudgetWindow = "year"
)

// DefaultBudgetWindow is the window applied when a grant does not set one.
// Matches the CRD's own default, so the two cannot drift.
const DefaultBudgetWindow = BudgetWindowDay

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

	// EffectiveTokenBudget is the ceiling actually enforced, after the grant's
	// own value, the catalog's default and the built-in constant have been
	// resolved.
	//
	// Reported because a grant that inherits its budget carries no budget on
	// its registration, so the registration no longer shows what an attendee
	// is enforced at. This is the object an operator reaches for first when
	// asking why an attendee got a 429.
	// +optional
	EffectiveTokenBudget int64 `json:"effectiveTokenBudget,omitempty"`

	// EffectiveCostBudget is the spend ceiling actually enforced, in US
	// dollars, after the grant's own value, the catalog's default and the
	// catalog's maximum have been resolved.
	//
	// Empty when no cost ceiling applies, which is the ordinary case: this
	// project maintains no pricing data and imposes no spend ceiling of its
	// own.
	// +optional
	EffectiveCostBudget string `json:"effectiveCostBudget,omitempty"`
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

// DefaultTokenBudget is the ceiling applied when a grant does not set one and
// no cluster-wide default is configured either. The last link in the
// resolution chain, and the only one that cannot itself be absent.
const DefaultTokenBudget int64 = 100000

// DefaultTTL is the backstop expiry applied when a grant does not set one.
// Matches the CRD's own default, so the two cannot drift.
const DefaultTTL = "4h"

// TokenBudgetValue is a convenience for setting the nil-able budget, so
// callers writing a grant do not each declare their own local for the address
// of a literal.
func TokenBudgetValue(v int64) *int64 {
	return &v
}

// TokenBudget returns the session's token ceiling, defaulted.
//
// The schema no longer stamps a default, so this accessor is where an omitted
// budget acquires one. Zero is rejected by validation rather than treated as a
// value, so a field stripped by a client that drops zero values arrives here as
// genuinely absent and resolves to the default, instead of registering a
// ceiling of no tokens at all.
func (s *AgentGatewaySession) TokenBudget() int64 {
	if s.Spec.TokenBudget != nil && *s.Spec.TokenBudget > 0 {
		return *s.Spec.TokenBudget
	}
	return DefaultTokenBudget
}

// BudgetWindow returns how long this session's budget lasts, defaulted.
//
// Defaulted here as well as in the CRD, so a grant created before the field
// existed gets the same window as one created after it, rather than an empty
// unit the rate-limit service would reject.
func (s *AgentGatewaySession) BudgetWindow() BudgetWindow {
	if s.Spec.BudgetWindow != "" {
		return s.Spec.BudgetWindow
	}
	return DefaultBudgetWindow
}

func init() {
	register(&AgentGatewaySession{}, &AgentGatewaySessionList{})
}
