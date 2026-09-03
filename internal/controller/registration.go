package controller

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// registrationEntry is one API key as agentgateway reads it from a ConfigMap.
//
// The shape is agentgateway's, not ours: each `data` entry holds JSON with a
// keyHash and arbitrary metadata. A raw key is rejected outright when sourced
// from a ConfigMap — the controller errors with "keys sourced from a ConfigMap
// must use keyHash, not a raw key, since ConfigMaps are not confidential" —
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
// so that a human — or an out-of-band sweep — reading an orphaned registration
// can tell whether it is still live, and so enforcement can move to the gateway
// unchanged if agentgateway ever binds a time function.
const metadataKeyExpiresAt = "expiresAt"

// renderRegistration builds the JSON for one session's key registration.
//
// Holds the hash, the session name, the token budget and the expiry. No key
// material and no provider credential: everything an orphaned registration
// could leak is either public or already known to whoever holds the key.
func renderRegistration(keyHash, sessionName string, tokenBudget int64, expiresAt time.Time) (string, error) {
	entry := registrationEntry{
		KeyHash: keyHash,
		Metadata: map[string]string{
			metadataKeySession: sessionName,
			// Strings because agentgateway's metadata is map[string]string.
			// The policy's CEL converts them back.
			metadataKeyTokenBudget: strconv.FormatInt(tokenBudget, 10),
			// RFC 3339 in UTC, so the value is unambiguous and CEL can parse
			// it with timestamp().
			metadataKeyExpiresAt: expiresAt.UTC().Format(time.RFC3339),
		},
	}
	// Marshalled rather than fmt-ed so a session name with an awkward character
	// cannot produce invalid JSON that agentgateway would reject at load time.
	buf, err := json.Marshal(entry)
	if err != nil {
		return "", fmt.Errorf("render key registration: %w", err)
	}
	return string(buf), nil
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
