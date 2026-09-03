package controller

import (
	"encoding/json"
	"fmt"
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

// renderRegistration builds the JSON for one session's key registration.
//
// Holds only the hash and the session name. No key material, no budget, no
// provider credential — everything an orphaned registration could leak is
// already public.
func renderRegistration(keyHash, sessionName string) (string, error) {
	entry := registrationEntry{
		KeyHash: keyHash,
		Metadata: map[string]string{
			metadataKeySession: sessionName,
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
