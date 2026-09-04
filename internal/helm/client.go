package helm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/kube"
	releaseiface "helm.sh/helm/v4/pkg/release"
	releasecommon "helm.sh/helm/v4/pkg/release/common"
	release "helm.sh/helm/v4/pkg/release/v1"
	"helm.sh/helm/v4/pkg/storage/driver"
	"k8s.io/client-go/rest"
)

// helmDriver stores release state in Secrets, Helm's own default.
const helmDriver = "secrets"

// actionTimeout bounds any single Helm action. Generous because it covers
// applying a large CRD chart, but finite so a wedged apply surfaces as an error
// rather than hanging the reconcile forever.
const actionTimeout = 5 * time.Minute

// ErrReleaseNotFound is returned by Status when no release exists. A sentinel
// rather than Helm's own error string so callers can branch on it.
var ErrReleaseNotFound = errors.New("helm release not found")

// OwnerLabel marks a release as belonging to this operator.
//
// The Educates v4 installer does not do this, and without it two operators
// sharing a release name fight in an upgrade loop, each reverting the other
// (ADR-0005). Helm v4 persists custom release labels onto the release Secret and
// reads them back through the storage driver, so this survives round-trips and
// is queryable.
const OwnerLabel = "agentgateway.operators.educates.dev/owner"

// OwnerValue is the value written into OwnerLabel.
const OwnerValue = "agentgateway-educates-operator"

// ErrNotOwned is returned when a release exists but carries no ownership marker
// of ours. Converging it would mean taking over somebody else's release, which
// this operator refuses to do.
var ErrNotOwned = errors.New("helm release is not owned by this operator")

// Client wraps the Helm SDK for one namespace.
type Client struct {
	cfg       *action.Configuration
	namespace string

	// skipCRDs is set only by NewMemoryClient: the fake kube client cannot
	// apply CRDs, and tests do not need them applied.
	skipCRDs bool
}

// NewClient builds a Helm client scoped to a namespace, driven by the operator's
// own REST config rather than a kubeconfig on disk.
func NewClient(cfg *rest.Config, namespace string) (*Client, error) {
	actionCfg := new(action.Configuration)
	getter := newRESTClientGetter(cfg, namespace)
	if err := actionCfg.Init(getter, namespace, helmDriver); err != nil {
		return nil, fmt.Errorf("init helm action config: %w", err)
	}
	actionCfg.SetLogger(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	return &Client{cfg: actionCfg, namespace: namespace}, nil
}

// Namespace returns the namespace this client acts in.
func (c *Client) Namespace() string { return c.namespace }

// asRelease downcasts Helm v4's release.Releaser interface to the concrete v1
// type. Written once here so the assertion is not repeated at every call site.
func asRelease(r releaseiface.Releaser) (*release.Release, error) {
	if r == nil {
		return nil, nil
	}
	rel, ok := r.(*release.Release)
	if !ok {
		return nil, fmt.Errorf("unexpected helm release type %T", r)
	}
	return rel, nil
}

// Status returns the current release, or ErrReleaseNotFound.
func (c *Client) Status(name string) (*release.Release, error) {
	get := action.NewGet(c.cfg)
	rel, err := get.Run(name)
	if err != nil {
		if errors.Is(err, driver.ErrReleaseNotFound) {
			return nil, ErrReleaseNotFound
		}
		return nil, fmt.Errorf("get helm release %s: %w", name, err)
	}
	return asRelease(rel)
}

// Install creates a release, stamped with this operator's ownership marker.
func (c *Client) Install(ctx context.Context, name string, chrt *Chart, vals map[string]any) (*release.Release, error) {
	install := action.NewInstall(c.cfg)
	install.ReleaseName = name
	install.Namespace = c.namespace
	install.Labels = ownerLabels()
	install.SkipCRDs = c.skipCRDs

	// Readiness is the reconciler's concern, never Helm's: the reconciler has
	// to gate on things Helm cannot see anyway: a GatewayClass that appears
	// only after leader election, a Gateway reporting Programmed. Blocking here
	// would just move that wait somewhere it cannot be observed.
	install.WaitStrategy = kube.HookOnlyStrategy
	install.Timeout = actionTimeout

	// The operator creates namespaces itself, with labels and owner references
	// Helm would not add.
	install.CreateNamespace = false

	rel, err := install.RunWithContext(ctx, chrt, vals)
	if err != nil {
		return nil, fmt.Errorf("install helm release %s: %w", name, err)
	}
	return asRelease(rel)
}

// Upgrade upgrades an existing release.
func (c *Client) Upgrade(ctx context.Context, name string, chrt *Chart, vals map[string]any) (*release.Release, error) {
	upgrade := action.NewUpgrade(c.cfg)
	upgrade.Namespace = c.namespace
	upgrade.Labels = ownerLabels()
	upgrade.WaitStrategy = kube.HookOnlyStrategy
	upgrade.Timeout = actionTimeout

	rel, err := upgrade.RunWithContext(ctx, name, chrt, vals)
	if err != nil {
		return nil, fmt.Errorf("upgrade helm release %s: %w", name, err)
	}
	return asRelease(rel)
}

// Uninstall removes a release. Idempotent: a release that is already gone is
// not an error, which matters because teardown is retried.
func (c *Client) Uninstall(name string) error {
	uninstall := action.NewUninstall(c.cfg)
	uninstall.IgnoreNotFound = true
	uninstall.WaitStrategy = kube.HookOnlyStrategy
	uninstall.Timeout = actionTimeout

	if _, err := uninstall.Run(name); err != nil {
		if errors.Is(err, driver.ErrReleaseNotFound) {
			return nil
		}
		return fmt.Errorf("uninstall helm release %s: %w", name, err)
	}
	return nil
}

// Rollback returns a release to a given revision. Used to clear a pending lock
// so a later upgrade can apply the desired state.
func (c *Client) Rollback(name string, revision int) error {
	rollback := action.NewRollback(c.cfg)
	rollback.Version = revision
	rollback.CleanupOnFail = true
	rollback.WaitStrategy = kube.HookOnlyStrategy
	rollback.Timeout = actionTimeout

	if err := rollback.Run(name); err != nil {
		return fmt.Errorf("rollback helm release %s to revision %d: %w", name, revision, err)
	}
	return nil
}

// LastDeployedRevision finds the highest revision that reached a usable state,
// which is what a repair rolls back to.
func (c *Client) LastDeployedRevision(name string) (revision int, found bool, err error) {
	history := action.NewHistory(c.cfg)
	history.Max = 0

	rels, err := history.Run(name)
	if err != nil {
		if errors.Is(err, driver.ErrReleaseNotFound) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("history for helm release %s: %w", name, err)
	}

	for _, r := range rels {
		rel, convErr := asRelease(r)
		if convErr != nil {
			return 0, false, convErr
		}
		if rel == nil || rel.Info == nil {
			continue
		}
		switch rel.Info.Status {
		case releasecommon.StatusDeployed, releasecommon.StatusSuperseded:
			if rel.Version > revision {
				revision = rel.Version
				found = true
			}
		}
	}
	return revision, found, nil
}

// FailureMessage extracts a human-readable reason from a release, for a status
// condition.
func FailureMessage(rel *release.Release, fallback string) string {
	if rel != nil && rel.Info != nil && rel.Info.Description != "" {
		return rel.Info.Description
	}
	return fallback
}

// IsOwned reports whether a release carries this operator's ownership marker.
//
// A release with no labels at all is treated as not ours. That is deliberately
// strict: adopting an unmarked release is exactly the silent takeover this
// guard exists to prevent.
func IsOwned(rel *release.Release) bool {
	if rel == nil {
		return false
	}
	return rel.Labels[OwnerLabel] == OwnerValue
}

func ownerLabels() map[string]string {
	return map[string]string{OwnerLabel: OwnerValue}
}
