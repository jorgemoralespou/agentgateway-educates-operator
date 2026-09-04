package participantkey

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestGenerateProducesAPrefixedKey(t *testing.T) {
	key, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}
	if !strings.HasPrefix(key, Prefix) {
		t.Errorf("key %q does not start with %q", key, Prefix)
	}
	if !IsParticipantKey(key) {
		t.Errorf("key %q did not round-trip through IsParticipantKey", key)
	}
}

// Two keys must never collide, or one attendee would spend another's budget.
func TestGenerateProducesDistinctKeys(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		key, err := Generate()
		if err != nil {
			t.Fatalf("Generate() error: %v", err)
		}
		if seen[key] {
			t.Fatalf("Generate() returned a duplicate key: %q", key)
		}
		seen[key] = true
	}
}

// The key goes into an HTTP header, a YAML scalar, and an environment variable.
// Anything needing escaping in one of those would break a workshop in a way
// that is tedious to diagnose.
func TestGenerateProducesATransportSafeKey(t *testing.T) {
	const safe = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_"

	for range 100 {
		key, err := Generate()
		if err != nil {
			t.Fatalf("Generate() error: %v", err)
		}
		body := strings.TrimPrefix(key, Prefix)
		for _, r := range body {
			if !strings.ContainsRune(safe, r) {
				t.Fatalf("key %q contains character %q, which needs escaping somewhere", key, r)
			}
		}
	}
}

// Hash must be SHA-256 over the exact key bytes, lowercase hex, prefixed
// `sha256:`, with no canonicalisation and no trailing newline. agentgateway
// verifies it the same way in both control plane and data plane, so any
// deviation here silently rejects every request (ADR-0004).
func TestHashMatchesAgentgatewayExpectations(t *testing.T) {
	const key = "sk-testkey"

	got := Hash(key)

	want := sha256.Sum256([]byte(key))
	expected := "sha256:" + hex.EncodeToString(want[:])

	if got != expected {
		t.Errorf("Hash(%q) = %q, want %q", key, got, expected)
	}
	if !strings.HasPrefix(got, "sha256:") {
		t.Errorf("Hash() = %q, want a sha256: prefix", got)
	}
	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("Hash() = %q, must not contain a newline", got)
	}
	if got != strings.ToLower(got) {
		t.Errorf("Hash() = %q, want lowercase hex", got)
	}
}

// Hashing must be stable: the same key always produces the same registration,
// so a reconcile does not rewrite an unchanged ConfigMap.
//
// The key is round-tripped through a copy rather than hashed twice in one
// expression, so this tests the function rather than a constant the compiler
// could fold away.
func TestHashIsStable(t *testing.T) {
	key, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}

	first := Hash(key)

	// A distinct string with the same contents, as a reconcile would read back
	// out of a Secret.
	roundTripped := string([]byte(key))
	second := Hash(roundTripped)

	if first != second {
		t.Errorf("Hash() is not stable: %q then %q", first, second)
	}
}

func TestHashDistinguishesKeys(t *testing.T) {
	if Hash("sk-one") == Hash("sk-two") {
		t.Error("Hash() did not distinguish two different keys")
	}
}

// The hash must not contain the key, or the registration ConfigMap, which is
// not confidential, would leak the credential. This is what makes the
// ADR-0002 leak tolerable.
func TestHashDoesNotContainTheKey(t *testing.T) {
	key, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}
	body := strings.TrimPrefix(key, Prefix)

	hash := Hash(key)

	if strings.Contains(hash, body) {
		t.Errorf("Hash() output contains the key material")
	}
}

func TestIsParticipantKey(t *testing.T) {
	valid, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}

	tests := []struct {
		name string
		key  string
		want bool
	}{
		{name: "a generated key", key: valid, want: true},
		{name: "empty", key: "", want: false},
		{name: "no prefix", key: "abcdef", want: false},
		{name: "prefix only", key: "sk-", want: false},
		{name: "too short", key: "sk-abc", want: false},
		{name: "not base64", key: "sk-!!!!", want: false},
		{name: "a provider key", key: "sk-proj-realOpenAIKeyShape", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsParticipantKey(tc.key); got != tc.want {
				t.Errorf("IsParticipantKey(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}
