package controller

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
)

// registrationEntry is one API key as agentgateway reads it from a ConfigMap.
//
// The shape is agentgateway's, not ours: each `data` entry holds JSON with a
// keyHash and arbitrary metadata. A raw key is rejected outright when sourced
// from a ConfigMap: the controller errors with "keys sourced from a ConfigMap
// must use keyHash, not a raw key, since ConfigMaps are not confidential",
// which is exactly the constraint that makes this registration honestly a
// ConfigMap rather than a Secret (ADR-0004).
type registrationEntry struct {
	KeyHash  string            `json:"keyHash"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// metadataKeySession is the metadata field the rate-limit descriptor keys on.
//
// Referenced from CEL in the flattened form `apiKey.session`, never
// `apiKey.metadata.session`: agentgateway flattens metadata onto the apiKey
// object. The field name `key` is reserved for the redacted key itself and must
// not be used here (ADR-0004).
const metadataKeySession = "session"

// metadataKeyTokenBudget carries the session's token ceiling.
//
// It rides on the registration rather than on the rate-limit service's own
// config because the budget is per-session and the config is cluster-wide: the
// policy's limitOverride reads it back through CEL, so a per-attendee limit
// needs no per-attendee entry in a shared file.
const metadataKeyTokenBudget = "tokenBudget"

// metadataKeyExpiresAt is when the key stops being valid.
//
// The backstop ADR-0002 calls "the only protection" when a namespace is
// force-deleted, because finalizers are stripped in that case and the
// registration is orphaned outright.
//
// It is recorded on the registration but **not enforced by the gateway**.
// agentgateway 1.5.0's CEL has `timestamp()` and duration arithmetic but no
// `now()`, and exposes no request-time property, so an authorization rule
// cannot compare an expiry against the current time. Verified against
// `crates/cel-fork/cel/src/context.rs` and `crates/agentgateway/src/cel/` at
// tag v1.5.0.
//
// Enforcement therefore lives in the operator, which does have a clock: see
// the expiry sweep in agentgatewaysession_expiry.go. The value is written here
// so that a human, or an out-of-band sweep, reading an orphaned registration
// can tell whether it is still live, and so enforcement can move to the gateway
// unchanged if agentgateway ever binds a time function.
const metadataKeyExpiresAt = "expiresAt"

// metadataKeyBudgetWindow carries how long the session's budget lasts.
//
// Rides on the registration for the same reason the budget does: the policy is
// one object cluster-wide and cannot hold a row per attendee, so a per-grant
// window has to travel with the key and be read back through CEL.
const metadataKeyBudgetWindow = "budgetWindow"

// metadataKeyCostBudget carries the session's spend ceiling, in micro-dollars.
//
// Micro-dollars rather than dollars because the protocol field carrying a
// descriptor's cost is an unsigned integer, so the value the CEL expression
// compares against has to be a whole number.
const metadataKeyCostBudget = "costBudget"

// registrationInputs is everything one session's registration carries.
//
// A struct rather than a parameter list: four of these are optional or easily
// transposed, and a positional call would let two same-typed values swap
// silently.
type registrationInputs struct {
	keyHash     string
	sessionName string

	// tokenBudget is nil when the grant inherits its ceiling.
	tokenBudget *int64

	// costMicroDollars is nil when no cost ceiling applies.
	costMicroDollars *int64

	window    agentgatewayv1alpha1.BudgetWindow
	expiresAt time.Time
}

// buildRegistration assembles the entry for one session's key registration.
//
// Holds the hash, the session name, the budgets and the expiry. No key
// material and no provider credential: everything an orphaned registration
// could leak is either public or already known to whoever holds the key.
//
// Split from the marshalling so a reconcile can compare the entry it wants
// against the entry already published, rather than comparing serialized JSON
// whose key order is an implementation detail of the marshaller.
//
// A nil tokenBudget writes no budget metadata at all. That is the mechanism by
// which a grant inherits the cluster-wide default: with the field absent, the
// policy's limit override falls through to the shared descriptor row, so an
// operator editing the catalog changes what a running session is enforced at
// without any grant being reconciled or any registration rewritten. Writing the
// resolved number here instead would freeze it at the moment the grant was
// last reconciled.
//
// A cost budget has no such shared row to fall through to, since the cost
// descriptor keys on the field's presence, so it is written whenever one
// applies, inherited or not.
func buildRegistration(in registrationInputs) registrationEntry {
	metadata := map[string]string{
		metadataKeySession: in.sessionName,
		// RFC 3339 in UTC, so the value is unambiguous and CEL can parse
		// it with timestamp().
		metadataKeyExpiresAt: in.expiresAt.UTC().Format(time.RFC3339),
		// Always written, unlike the budgets: the window is a property of the
		// grant whether or not a ceiling is inherited, and the rate-limit
		// service rejects an empty unit.
		metadataKeyBudgetWindow: string(in.window),
	}
	if in.tokenBudget != nil {
		// A string because agentgateway's metadata is map[string]string. The
		// policy's CEL converts it back.
		metadata[metadataKeyTokenBudget] = strconv.FormatInt(*in.tokenBudget, 10)
	}
	if in.costMicroDollars != nil {
		metadata[metadataKeyCostBudget] = strconv.FormatInt(*in.costMicroDollars, 10)
	}
	return registrationEntry{KeyHash: in.keyHash, Metadata: metadata}
}

// equals reports whether two entries would enforce the same thing.
//
// Compares the whole payload rather than the hash alone. A reconcile that
// changed only the budget or the expiry still has to be written, or the
// gateway keeps enforcing a ceiling the grant no longer asks for.
func (e registrationEntry) equals(other registrationEntry) bool {
	if e.KeyHash != other.KeyHash || len(e.Metadata) != len(other.Metadata) {
		return false
	}
	for k, v := range e.Metadata {
		if w, ok := other.Metadata[k]; !ok || v != w {
			return false
		}
	}
	return true
}

// marshalRegistration serializes an entry for the ConfigMap.
func marshalRegistration(entry registrationEntry) (string, error) {
	// Marshalled rather than fmt-ed so a session name with an awkward character
	// cannot produce invalid JSON that agentgateway would reject at load time.
	buf, err := json.Marshal(entry)
	if err != nil {
		return "", fmt.Errorf("render key registration: %w", err)
	}
	return string(buf), nil
}

// renderRegistration builds the JSON for one session's key registration.
//
// Kept for the tests, which assert on the serialized form and would otherwise
// each repeat the build-then-marshal pair. The reconcile deliberately does not
// use it: it needs the entry as well as the payload, so that it can compare
// what it wants against what is published without going through JSON.
func renderRegistration(keyHash, sessionName string, tokenBudget *int64, window agentgatewayv1alpha1.BudgetWindow, expiresAt time.Time) (string, error) {
	return marshalRegistration(buildRegistration(registrationInputs{
		keyHash:     keyHash,
		sessionName: sessionName,
		tokenBudget: tokenBudget,
		window:      window,
		expiresAt:   expiresAt,
	}))
}

// parseRegistration reads back a registration entry, so a reconcile can tell
// whether the live ConfigMap already matches the key it holds.
func parseRegistration(raw string) (registrationEntry, error) {
	var entry registrationEntry
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		return registrationEntry{}, fmt.Errorf("parse key registration: %w", err)
	}
	return entry, nil
}
