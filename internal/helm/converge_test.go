package helm

import (
	"testing"

	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	releasecommon "helm.sh/helm/v4/pkg/release/common"
	release "helm.sh/helm/v4/pkg/release/v1"
)

// ownedRelease builds a live release owned by this operator, with the given
// status and the fingerprint implied by chart version and values.
func ownedRelease(status releasecommon.Status, version string, vals map[string]any) *release.Release {
	return &release.Release{
		Name:   "test",
		Info:   &release.Info{Status: status},
		Chart:  &chartv2.Chart{Metadata: &chartv2.Metadata{Version: version}},
		Config: vals,
		Labels: map[string]string{OwnerLabel: OwnerValue},
	}
}

func TestClassify(t *testing.T) {
	const version = "1.5.0"
	vals := map[string]any{"replicas": 1}
	matching := fingerprint(version, vals)
	different := fingerprint(version, map[string]any{"replicas": 2})

	tests := []struct {
		name      string
		live      *release.Release
		notFound  bool
		desiredFP string
		want      decision
	}{
		{
			name:      "no release installs",
			notFound:  true,
			desiredFP: matching,
			want:      decInstall,
		},
		{
			name:      "nil release installs",
			live:      nil,
			desiredFP: matching,
			want:      decInstall,
		},
		{
			name:      "release with no info installs",
			live:      &release.Release{Name: "test"},
			desiredFP: matching,
			want:      decInstall,
		},
		{
			name:      "deployed and unchanged does nothing",
			live:      ownedRelease(releasecommon.StatusDeployed, version, vals),
			desiredFP: matching,
			want:      decUnchanged,
		},
		{
			name:      "deployed with changed values upgrades",
			live:      ownedRelease(releasecommon.StatusDeployed, version, vals),
			desiredFP: different,
			want:      decUpgrade,
		},
		{
			name:      "deployed with changed chart version upgrades",
			live:      ownedRelease(releasecommon.StatusDeployed, "1.4.0", vals),
			desiredFP: matching,
			want:      decUpgrade,
		},
		{
			name:      "failed with unchanged inputs holds",
			live:      ownedRelease(releasecommon.StatusFailed, version, vals),
			desiredFP: matching,
			want:      decHold,
		},
		{
			name:      "failed with changed inputs repairs",
			live:      ownedRelease(releasecommon.StatusFailed, version, vals),
			desiredFP: different,
			want:      decRepair,
		},
		{
			name:      "pending-install with unchanged inputs holds",
			live:      ownedRelease(releasecommon.StatusPendingInstall, version, vals),
			desiredFP: matching,
			want:      decHold,
		},
		{
			name:      "pending-upgrade with changed inputs repairs",
			live:      ownedRelease(releasecommon.StatusPendingUpgrade, version, vals),
			desiredFP: different,
			want:      decRepair,
		},
		{
			name:      "pending-rollback with unchanged inputs holds",
			live:      ownedRelease(releasecommon.StatusPendingRollback, version, vals),
			desiredFP: matching,
			want:      decHold,
		},
		{
			name:      "uninstalling waits",
			live:      ownedRelease(releasecommon.StatusUninstalling, version, vals),
			desiredFP: matching,
			want:      decWait,
		},
		{
			name:      "uninstalled installs fresh",
			live:      ownedRelease(releasecommon.StatusUninstalled, version, vals),
			desiredFP: matching,
			want:      decInstall,
		},
		{
			name:      "superseded as latest installs fresh",
			live:      ownedRelease(releasecommon.StatusSuperseded, version, vals),
			desiredFP: matching,
			want:      decInstall,
		},
		{
			name:      "unknown status installs fresh",
			live:      ownedRelease(releasecommon.StatusUnknown, version, vals),
			desiredFP: matching,
			want:      decInstall,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(tc.live, tc.notFound, tc.desiredFP)
			if got != tc.want {
				t.Errorf("classify() = %v, want %v", got, tc.want)
			}
		})
	}
}

// A release this operator does not own is refused rather than converged,
// whatever state it is in. This is the collision the v4 installer does not
// guard against: two operators sharing a release name revert each other in an
// upgrade loop forever.
func TestClassifyRefusesReleaseNotOwned(t *testing.T) {
	const version = "1.5.0"
	vals := map[string]any{"replicas": 1}
	desiredFP := fingerprint(version, vals)

	statuses := []releasecommon.Status{
		releasecommon.StatusDeployed,
		releasecommon.StatusFailed,
		releasecommon.StatusPendingInstall,
		releasecommon.StatusPendingUpgrade,
		releasecommon.StatusUninstalling,
	}

	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			// Same shape as an owned release, but with somebody else's marker.
			live := ownedRelease(status, version, vals)
			live.Labels = map[string]string{OwnerLabel: "some-other-operator"}

			if got := classify(live, false, desiredFP); got != decRefuse {
				t.Errorf("classify() = %v, want decRefuse", got)
			}
		})
	}

	t.Run("no labels at all", func(t *testing.T) {
		live := ownedRelease(releasecommon.StatusDeployed, version, vals)
		live.Labels = nil

		if got := classify(live, false, desiredFP); got != decRefuse {
			t.Errorf("classify() = %v, want decRefuse", got)
		}
	})
}

func TestRepairMethod(t *testing.T) {
	tests := []struct {
		name        string
		status      releasecommon.Status
		hasDeployed bool
		want        Action
	}{
		{
			name:        "pending with a deployed revision rolls back",
			status:      releasecommon.StatusPendingUpgrade,
			hasDeployed: true,
			want:        ActionRepairedRollback,
		},
		{
			name:        "pending-install with a deployed revision rolls back",
			status:      releasecommon.StatusPendingInstall,
			hasDeployed: true,
			want:        ActionRepairedRollback,
		},
		{
			name:        "failed with a deployed revision upgrades in place",
			status:      releasecommon.StatusFailed,
			hasDeployed: true,
			want:        ActionRepairedUpgrade,
		},
		{
			name:        "failed with no deployed revision reinstalls",
			status:      releasecommon.StatusFailed,
			hasDeployed: false,
			want:        ActionRepairedReinstall,
		},
		{
			name:        "pending with no deployed revision reinstalls",
			status:      releasecommon.StatusPendingInstall,
			hasDeployed: false,
			want:        ActionRepairedReinstall,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := repairMethod(tc.status, tc.hasDeployed); got != tc.want {
				t.Errorf("repairMethod() = %v, want %v", got, tc.want)
			}
		})
	}
}

// An empty values map and a nil one must fingerprint identically. Helm stores
// empty values as a nil Config that marshals to "null" rather than "{}", so
// without this normalisation a release with no values reads as perpetually
// drifted and climbs revisions forever.
func TestFingerprintTreatsNilAndEmptyValuesAlike(t *testing.T) {
	nilVals := fingerprint("1.5.0", nil)
	emptyVals := fingerprint("1.5.0", map[string]any{})

	if nilVals != emptyVals {
		t.Errorf("fingerprint(nil) = %q, fingerprint(empty) = %q; want equal", nilVals, emptyVals)
	}
}

// Values that survive a round-trip through the release store come back as
// float64 where they went in as int. Marshalling both sides the same way makes
// those compare equal, so a values-identical release is not seen as drifted.
func TestFingerprintTreatsIntAndFloatAlike(t *testing.T) {
	asInt := fingerprint("1.5.0", map[string]any{"replicas": 1})
	asFloat := fingerprint("1.5.0", map[string]any{"replicas": float64(1)})

	if asInt != asFloat {
		t.Errorf("fingerprint(int) = %q, fingerprint(float64) = %q; want equal", asInt, asFloat)
	}
}

// Key order must not affect the fingerprint, or a converge would flip between
// upgrade and no-op depending on map iteration order.
func TestFingerprintIsKeyOrderStable(t *testing.T) {
	first := fingerprint("1.5.0", map[string]any{"a": 1, "b": 2, "c": 3})
	second := fingerprint("1.5.0", map[string]any{"c": 3, "b": 2, "a": 1})

	if first != second {
		t.Errorf("fingerprint differs by key order: %q vs %q", first, second)
	}
}

// A values-only change must still produce a different fingerprint, so it
// upgrades rather than reading as unchanged.
func TestFingerprintDistinguishesValues(t *testing.T) {
	one := fingerprint("1.5.0", map[string]any{"replicas": 1})
	two := fingerprint("1.5.0", map[string]any{"replicas": 2})

	if one == two {
		t.Error("fingerprint did not distinguish differing values")
	}
}

// A chart version change with identical values must also upgrade.
func TestFingerprintDistinguishesChartVersion(t *testing.T) {
	vals := map[string]any{"replicas": 1}

	if fingerprint("1.5.0", vals) == fingerprint("1.4.0", vals) {
		t.Error("fingerprint did not distinguish differing chart versions")
	}
}

func TestIsOwned(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{name: "our marker", labels: map[string]string{OwnerLabel: OwnerValue}, want: true},
		{name: "another operator", labels: map[string]string{OwnerLabel: "other"}, want: false},
		{name: "no labels", labels: nil, want: false},
		{name: "unrelated labels only", labels: map[string]string{"app": "x"}, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rel := &release.Release{Labels: tc.labels}
			if got := IsOwned(rel); got != tc.want {
				t.Errorf("IsOwned() = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("nil release", func(t *testing.T) {
		if IsOwned(nil) {
			t.Error("IsOwned(nil) = true, want false")
		}
	})
}

func TestChartVersion(t *testing.T) {
	if got := chartVersion(nil); got != "" {
		t.Errorf("chartVersion(nil) = %q, want empty", got)
	}
	if got := chartVersion(&chartv2.Chart{}); got != "" {
		t.Errorf("chartVersion(no metadata) = %q, want empty", got)
	}
	chrt := &chartv2.Chart{Metadata: &chartv2.Metadata{Version: "1.5.0"}}
	if got := chartVersion(chrt); got != "1.5.0" {
		t.Errorf("chartVersion() = %q, want 1.5.0", got)
	}
}
