package v1alpha1

// Conditions are authoritative; the Phase enums below are advisory summaries
// for humans reading `kubectl get`. This matches the v4 installer operator, and
// is deliberately not the status.educates.* nesting the Python components use —
// that is a kopf artefact.

// Condition types set by the AgentGatewayPlatform controller.
const (
	// ConditionClusterConfigAvailable reports whether EducatesClusterConfig has
	// published the status this operator reads. A missing cluster config is
	// "not ready yet", never an error.
	ConditionClusterConfigAvailable = "ClusterConfigAvailable"

	// ConditionGatewayAPIReady reports whether the Gateway API CRDs are present.
	ConditionGatewayAPIReady = "GatewayAPIReady"

	// ConditionControlPlaneReady reports whether both agentgateway charts are
	// converged and the controller Deployment is available.
	ConditionControlPlaneReady = "ControlPlaneReady"

	// ConditionGatewayProgrammed reports whether the Gateway reports Programmed
	// and its data-plane Deployment is available. Readiness deliberately does
	// not gate on the Gateway's addresses (ADR-0005).
	ConditionGatewayProgrammed = "GatewayProgrammed"

	// ConditionRateLimitReady reports whether the rate-limit service and its
	// counter store are converged and available.
	ConditionRateLimitReady = "RateLimitReady"

	// ConditionPolicyReady reports whether the single API-key policy is applied.
	ConditionPolicyReady = "PolicyReady"
)

// Condition types set by the AgentGatewayCatalog controller.
const (
	// ConditionPlatformReady reports whether the platform declaration this
	// catalog depends on is itself Ready.
	ConditionPlatformReady = "PlatformReady"

	// ConditionModelsRendered reports whether every catalog entry has been
	// rendered into agentgateway's own resources.
	ConditionModelsRendered = "ModelsRendered"
)

// Condition types set by the AgentGatewaySession controller.
const (
	// ConditionCatalogAvailable reports whether the referenced catalog resolved
	// and is Ready.
	ConditionCatalogAvailable = "CatalogAvailable"

	// ConditionSecretWritten reports whether the participant key Secret exists
	// in the workshop namespace.
	ConditionSecretWritten = "SecretWritten"

	// ConditionKeyRegistered reports whether the key registration exists in the
	// gateway namespace.
	ConditionKeyRegistered = "KeyRegistered"

	// ConditionPlacementValid reports whether the grant was created in a
	// namespace where the resulting Secret is reachable by the attendee's pod.
	// A grant in a session namespace is rejected here rather than reconciled.
	ConditionPlacementValid = "PlacementValid"
)

// ConditionReady is the summary condition on every kind in this group, set last.
const ConditionReady = "Ready"

// Condition reasons. Kept as constants so status output stays stable enough to
// assert on and to grep for in a support conversation.
const (
	ReasonReconciling         = "Reconciling"
	ReasonReady               = "Ready"
	ReasonFailed              = "Failed"
	ReasonWaiting             = "Waiting"
	ReasonNotFound            = "NotFound"
	ReasonReleaseConflict     = "ReleaseConflict"
	ReasonWrongNamespace      = "WrongNamespace"
	ReasonInstalling          = "Installing"
	ReasonUninstalling        = "Uninstalling"
	ReasonCustomResourceKinds = "CustomResourceKindsMissing"
)
