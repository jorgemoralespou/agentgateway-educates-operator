package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The TTL is the only protection when a namespace is force-deleted: finalizers
// are stripped, so nothing this operator controls runs at teardown. The sweep
// is what makes that promise real, and these tests pin the decision it makes.

func registrationFor(t *testing.T, session string, expiresAt time.Time) *corev1.ConfigMap {
	t.Helper()

	payload, err := renderRegistration("sha256:abc", session, 1000, expiresAt)
	if err != nil {
		t.Fatalf("renderRegistration() error: %v", err)
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: session + "-agentgateway"},
		Data:       map[string]string{session: payload},
	}
}

func TestExpiredDecidesOnTheRecordedExpiry(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	sweeper := &ExpirySweeper{}

	tests := []struct {
		name      string
		expiresAt time.Time
		want      bool
	}{
		{
			name:      "an hour past its expiry is expired",
			expiresAt: now.Add(-time.Hour),
			want:      true,
		},
		{
			name:      "an hour before its expiry is live",
			expiresAt: now.Add(time.Hour),
			want:      false,
		},
		{
			// A key exactly at its expiry is treated as expired: the ceiling is
			// inclusive, so a TTL of 4h means four hours of access and not a
			// moment more.
			name:      "exactly at its expiry is expired",
			expiresAt: now,
			want:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cm := registrationFor(t, "ws-001", tc.expiresAt)
			if got := sweeper.expired(cm, now); got != tc.want {
				t.Errorf("expired() = %v, want %v", got, tc.want)
			}
		})
	}
}

// A registration this operator did not write, or wrote before expiries existed,
// must be left alone rather than deleted. Revoking a key on a parse failure
// would be a worse failure than leaving a stale one.
func TestExpiredLeavesUnrecognisedRegistrationsAlone(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	sweeper := &ExpirySweeper{}

	tests := []struct {
		name string
		data map[string]string
	}{
		{
			name: "no data at all",
			data: nil,
		},
		{
			name: "unparseable entry",
			data: map[string]string{"ws": "not json"},
		},
		{
			name: "no expiry recorded",
			data: map[string]string{"ws": `{"keyHash":"sha256:abc","metadata":{"session":"ws"}}`},
		},
		{
			name: "expiry is not a timestamp",
			data: map[string]string{"ws": `{"keyHash":"sha256:abc","metadata":{"expiresAt":"soon"}}`},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "ws-agentgateway"},
				Data:       tc.data,
			}
			if sweeper.expired(cm, now) {
				t.Error("expired() = true; an unrecognised registration must be left alone")
			}
		})
	}
}

// A ConfigMap holding several keys must survive while any one of them is still
// live, or a sweep would revoke an attendee mid-session.
func TestExpiredKeepsARegistrationWithAnyLiveEntry(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	sweeper := &ExpirySweeper{}

	expired, err := renderRegistration("sha256:a", "ws-old", 1000, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("renderRegistration() error: %v", err)
	}
	live, err := renderRegistration("sha256:b", "ws-new", 1000, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("renderRegistration() error: %v", err)
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-agentgateway"},
		Data:       map[string]string{"ws-old": expired, "ws-new": live},
	}

	if sweeper.expired(cm, now) {
		t.Error("expired() = true; a registration with a live entry must survive")
	}
}

// The sweep must do nothing when there is no platform, rather than erroring in
// a loop. This is the state during install and after teardown.
func TestSweepIsANoOpWithoutAGatewayNamespace(t *testing.T) {
	sweeper := &ExpirySweeper{
		GatewayNamespaceFor: func(context.Context) (string, error) { return "", nil },
	}

	// No client is set, so a sweep that tried to list would panic. Reaching the
	// end without one is the assertion.
	if err := sweeper.Sweep(context.Background()); err != nil {
		t.Errorf("Sweep() error: %v", err)
	}
}

func TestSweeperDefaults(t *testing.T) {
	sweeper := &ExpirySweeper{}

	if got := sweeper.interval(); got != expirySweepInterval {
		t.Errorf("interval() = %v, want %v", got, expirySweepInterval)
	}
	if sweeper.now().IsZero() {
		t.Error("now() returned the zero time")
	}

	pinned := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	sweeper.Now = func() time.Time { return pinned }
	if !sweeper.now().Equal(pinned) {
		t.Error("now() did not honour the injected clock")
	}
}

// The sweep runs only on the leader, so two overlapping pods during a rollout
// do not both delete the same registrations.
func TestSweeperNeedsLeaderElection(t *testing.T) {
	if !(&ExpirySweeper{}).NeedLeaderElection() {
		t.Error("NeedLeaderElection() = false; the sweep must not run on a non-leader")
	}
}
