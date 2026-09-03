package controller

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The registration is the only thing the gateway reads, so what it carries is
// the contract. These are plain table tests: the shape is pure logic and needs
// no cluster.

func TestRenderRegistrationCarriesTheHashBudgetAndExpiry(t *testing.T) {
	expiry := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

	raw, err := renderRegistration("sha256:abc123", "ws-001", 250000, expiry)
	if err != nil {
		t.Fatalf("renderRegistration() error: %v", err)
	}

	entry, err := parseRegistration(raw)
	if err != nil {
		t.Fatalf("parseRegistration() error: %v", err)
	}

	if entry.KeyHash != "sha256:abc123" {
		t.Errorf("keyHash = %q, want sha256:abc123", entry.KeyHash)
	}
	if got := entry.Metadata[metadataKeySession]; got != "ws-001" {
		t.Errorf("session = %q, want ws-001", got)
	}
	// The per-session ceiling. Without this the policy's limitOverride has
	// nothing to read and every attendee shares one cluster-wide limit.
	if got := entry.Metadata[metadataKeyTokenBudget]; got != "250000" {
		t.Errorf("tokenBudget = %q, want 250000", got)
	}
	if got := entry.Metadata[metadataKeyExpiresAt]; got != "2026-09-03T12:00:00Z" {
		t.Errorf("expiresAt = %q, want 2026-09-03T12:00:00Z", got)
	}
}

// The expiry must be RFC 3339 in UTC. The sweep parses it back with
// time.Parse(time.RFC3339), so a local-zone or non-RFC3339 rendering would make
// every key look unexpirable.
func TestRenderRegistrationNormalisesTheExpiryToUTC(t *testing.T) {
	zone := time.FixedZone("UTC+5", 5*60*60)
	expiry := time.Date(2026, 9, 3, 17, 0, 0, 0, zone)

	raw, err := renderRegistration("sha256:abc", "ws-tz", 1000, expiry)
	if err != nil {
		t.Fatalf("renderRegistration() error: %v", err)
	}

	entry, _ := parseRegistration(raw)
	got := entry.Metadata[metadataKeyExpiresAt]

	if !strings.HasSuffix(got, "Z") {
		t.Errorf("expiresAt = %q, want a UTC (Z-suffixed) timestamp", got)
	}
	parsed, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Fatalf("expiresAt %q does not parse as RFC3339: %v", got, err)
	}
	if !parsed.Equal(expiry) {
		t.Errorf("expiresAt %q is not the same instant as %v", got, expiry)
	}
}

// The registration is a ConfigMap, which is not confidential. It must never
// carry key material — that asymmetry is what makes the ADR-0002 leak
// tolerable.
func TestRenderRegistrationCarriesNoKeyMaterial(t *testing.T) {
	const key = "sk-thisIsTheActualParticipantKey"

	raw, err := renderRegistration("sha256:deadbeef", "ws-002", 1000, time.Now())
	if err != nil {
		t.Fatalf("renderRegistration() error: %v", err)
	}

	if strings.Contains(raw, key) {
		t.Error("the registration contains key material")
	}
	if strings.Contains(raw, "sk-") {
		t.Errorf("the registration contains something key-shaped: %q", raw)
	}
}

// A session name with an awkward character must not be able to produce invalid
// JSON, which agentgateway would reject at load time for every key in the map.
func TestRenderRegistrationEscapesTheSessionName(t *testing.T) {
	raw, err := renderRegistration("sha256:abc", `ws"003\n`, 1000, time.Now())
	if err != nil {
		t.Fatalf("renderRegistration() error: %v", err)
	}

	var probe map[string]any
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		t.Fatalf("rendered registration is not valid JSON: %v", err)
	}
}

// `key` is reserved by agentgateway for the redacted key itself, so no metadata
// field may use that name (ADR-0004).
func TestRenderRegistrationAvoidsTheReservedMetadataName(t *testing.T) {
	raw, err := renderRegistration("sha256:abc", "ws-004", 1000, time.Now())
	if err != nil {
		t.Fatalf("renderRegistration() error: %v", err)
	}

	entry, _ := parseRegistration(raw)
	if _, found := entry.Metadata["key"]; found {
		t.Error(`metadata uses the reserved name "key"`)
	}
}

func TestParseRegistrationRejectsGarbage(t *testing.T) {
	if _, err := parseRegistration("not json at all"); err == nil {
		t.Error("parseRegistration() accepted invalid JSON")
	}
}
