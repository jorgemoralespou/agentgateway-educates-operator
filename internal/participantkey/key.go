// Package participantkey generates and hashes the credential an attendee's
// workshop code uses to reach the Gateway.
//
// In agentgateway's Kubernetes mode there is no minting API — nothing upstream
// issues a credential, so whatever appears in a key registration is whatever
// the author of that object put there. This operator generates the key itself
// (ADR-0004), which is a real departure from the LiteLLM prior art where the
// proxy minted keys with server-side identity.
package participantkey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// Prefix marks a participant key, matching the convention OpenAI-compatible
// clients expect. Some client libraries validate it.
const Prefix = "sk-"

// randomBytes is how much entropy each key carries. 32 bytes is 256 bits, well
// beyond what a workshop needs, and the resulting key is still short enough to
// paste.
const randomBytes = 32

// Generate returns a new cryptographically random participant key.
//
// Uses crypto/rand: a predictable key would let one attendee use another's
// budget, and there is no upstream record to cross-check against.
func Generate() (string, error) {
	buf := make([]byte, randomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate participant key: %w", err)
	}
	// URL-safe base64 without padding: no characters needing escaping in an
	// HTTP header, a shell command, or a YAML scalar.
	return Prefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// Hash returns the key's hash in the form agentgateway expects.
//
// SHA-256 over the exact key bytes, lowercase hex, no canonicalisation and no
// trailing newline (ADR-0004). agentgateway normalises hex to lowercase on
// input and verifies the same way in both control plane and data plane, so any
// deviation here silently rejects every request.
func Hash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// IsParticipantKey reports whether a string looks like a key this package
// generated.
//
// Used only to sanity-check a Secret's contents before trusting it; it says
// nothing about whether the key is registered or still valid.
func IsParticipantKey(key string) bool {
	if !strings.HasPrefix(key, Prefix) {
		return false
	}
	encoded := strings.TrimPrefix(key, Prefix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	return err == nil && len(decoded) == randomBytes
}
