package helm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	releasecommon "helm.sh/helm/v4/pkg/release/common"
	release "helm.sh/helm/v4/pkg/release/v1"
)

// Action reports what a converge actually did, so a reconciler can turn it into
// a condition without re-deriving it.
type Action string

const (
	ActionInstalled         Action = "installed"
	ActionUpgraded          Action = "upgraded"
	ActionRepairedUpgrade   Action = "repaired-upgrade"
	ActionRepairedRollback  Action = "repaired-rollback"
	ActionRepairedReinstall Action = "repaired-reinstall"
	ActionUnchanged         Action = "unchanged"
	ActionHeldFailed        Action = "held-failed"
	ActionWaitingUninstall  Action = "waiting-uninstall"
	ActionRefusedNotOwned   Action = "refused-not-owned"
)

// Result is the outcome of a converge.
type Result struct {
	Action  Action
	Release *release.Release
}

// decision is the internal verdict of the decision table, kept separate from
// Action so the table can be tested without a Helm client.
type decision int

const (
	decInstall decision = iota
	decUpgrade
	decRepair
	decUnchanged
	decHold
	decWait
	decRefuse
)

// EnsureRelease converges a release towards the given chart and values.
//
// Repeated calls with unchanged inputs do nothing; a changed chart version or a
// changed value upgrades; a release this operator does not own is refused rather
// than taken over.
func (c *Client) EnsureRelease(ctx context.Context, name string, chrt *Chart, vals map[string]any) (Result, error) {
	desiredFP := fingerprint(chartVersion(chrt), vals)

	live, err := c.Status(name)
	notFound := errors.Is(err, ErrReleaseNotFound)
	if err != nil && !notFound {
		return Result{}, err
	}

	switch classify(live, notFound, desiredFP) {
	case decInstall:
		rel, err := c.Install(ctx, name, chrt, vals)
		return Result{Action: ActionInstalled, Release: rel}, err

	case decUpgrade:
		rel, err := c.Upgrade(ctx, name, chrt, vals)
		return Result{Action: ActionUpgraded, Release: rel}, err

	case decRepair:
		rev, hasDeployed, err := c.LastDeployedRevision(name)
		if err != nil {
			return Result{}, err
		}
		switch repairMethod(live.Info.Status, hasDeployed) {
		case ActionRepairedRollback:
			if err := c.Rollback(name, rev); err != nil {
				return Result{}, err
			}
			return Result{Action: ActionRepairedRollback, Release: live}, nil
		case ActionRepairedUpgrade:
			rel, err := c.Upgrade(ctx, name, chrt, vals)
			return Result{Action: ActionRepairedUpgrade, Release: rel}, err
		default:
			if err := c.Uninstall(name); err != nil {
				return Result{}, err
			}
			rel, err := c.Install(ctx, name, chrt, vals)
			return Result{Action: ActionRepairedReinstall, Release: rel}, err
		}

	case decHold:
		// A failed or pending release whose inputs have not changed. Retrying
		// the same inputs would just fail the same way, climbing revisions in a
		// churn loop; hold and report instead.
		return Result{Action: ActionHeldFailed, Release: live}, nil

	case decWait:
		// A prior uninstall is still in flight. Installing now fails with
		// "cannot reuse a name that is still in use", so wait it out and let
		// the race self-heal.
		return Result{Action: ActionWaitingUninstall, Release: live}, nil

	case decRefuse:
		return Result{Action: ActionRefusedNotOwned, Release: live}, nil

	default:
		return Result{Action: ActionUnchanged, Release: live}, nil
	}
}

// classify is the decision table: given the live release and the desired
// fingerprint, what should happen.
//
// A pure function so every branch is unit-testable without a cluster.
func classify(live *release.Release, notFound bool, desiredFP string) decision {
	if notFound || live == nil || live.Info == nil {
		return decInstall
	}

	// The ownership check comes before any convergence decision. A release
	// somebody else owns must not be upgraded, repaired, or reinstalled — that
	// is the collision the v4 installer does not guard against, where two
	// operators sharing a release name revert each other forever.
	if !IsOwned(live) {
		return decRefuse
	}

	switch live.Info.Status {
	case releasecommon.StatusDeployed:
		if liveFingerprint(live) != desiredFP {
			return decUpgrade
		}
		return decUnchanged

	case releasecommon.StatusFailed,
		releasecommon.StatusPendingInstall,
		releasecommon.StatusPendingUpgrade,
		releasecommon.StatusPendingRollback:
		if liveFingerprint(live) != desiredFP {
			return decRepair
		}
		return decHold

	case releasecommon.StatusUninstalling:
		return decWait

	default:
		// uninstalled, superseded-as-latest, unknown: no usable release record,
		// so install fresh.
		return decInstall
	}
}

// repairMethod picks the least destructive way out of a failed or pending
// release.
//
//	pending-*, a deployed revision exists → rollback (clears the lock; the
//	                                        next pass applies the desired state)
//	failed,    a deployed revision exists → upgrade in place
//	either,    no deployed revision       → uninstall and reinstall
func repairMethod(status releasecommon.Status, hasDeployed bool) Action {
	pending := status == releasecommon.StatusPendingInstall ||
		status == releasecommon.StatusPendingUpgrade ||
		status == releasecommon.StatusPendingRollback

	switch {
	case pending && hasDeployed:
		return ActionRepairedRollback
	case hasDeployed:
		return ActionRepairedUpgrade
	default:
		return ActionRepairedReinstall
	}
}

// fingerprint identifies a desired release state as chart version plus values.
//
// Two normalisations matter here, both of them load-bearing:
//
//   - json.Marshal sorts map keys, so the digest is stable across map iteration
//     order; and marshalling both sides identically makes int32(1) and
//     float64(1) compare equal, which they otherwise would not after a
//     round-trip through the release store.
//   - an empty values map is normalised to {}. Helm stores empty values as a nil
//     Config that marshals to "null", not "{}", so without this a release with
//     no values reads as perpetually drifted and climbs revisions forever.
func fingerprint(chartVersion string, vals map[string]any) string {
	if len(vals) == 0 {
		vals = map[string]any{}
	}
	b, _ := json.Marshal(vals)
	sum := sha256.Sum256(b)
	return chartVersion + ":" + hex.EncodeToString(sum[:])
}

func liveFingerprint(live *release.Release) string {
	return fingerprint(chartVersion(live.Chart), live.Config)
}

func chartVersion(chrt *Chart) string {
	if chrt == nil || chrt.Metadata == nil {
		return ""
	}
	return chrt.Metadata.Version
}
