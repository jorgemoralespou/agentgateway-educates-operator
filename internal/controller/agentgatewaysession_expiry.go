package controller

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
)

// Key expiry.
//
// Every participant key carries a TTL as a backstop, because force-deleting a
// namespace strips finalizers and orphans the registration outright — no
// teardown path this operator controls runs at all in that case (ADR-0002).
//
// The natural home for this would be the gateway: stamp an expiry on the
// registration and let an authorization rule reject a key past it. That is not
// possible on agentgateway 1.5.0. Its CEL provides `timestamp()` and duration
// arithmetic but no `now()`, and exposes no request-time property, so an
// expression has nothing to compare an expiry against. Verified against
// `crates/cel-fork/cel/src/context.rs` (the builtin function table) and
// `crates/agentgateway/src/cel/` at tag v1.5.0.
//
// So enforcement lives here instead, in the one place that does have a clock.
// A sweep deletes registrations whose expiry has passed. Deleting the
// registration is exactly what revokes the key: agentgateway resolves API keys
// by watching these ConfigMaps, so a removed entry stops authenticating on the
// next xDS snapshot.
//
// This is a sweep rather than a per-session timer because the case it exists
// for is precisely the one where the session object is gone: a force-deleted
// namespace leaves the registration behind with nothing watching it.

// expirySweepInterval is how often orphaned registrations are checked.
//
// A key's TTL is measured in hours, so a minute of slack past expiry is
// immaterial, and a frequent sweep would list every registration in the gateway
// namespace for nothing.
const expirySweepInterval = time.Minute

// ExpirySweeper removes registrations whose keys have expired.
//
// Runs as a manager Runnable rather than a reconciler: it is driven by the
// clock, not by changes to any object, and the objects it acts on may have no
// owner left to reconcile.
type ExpirySweeper struct {
	client.Client

	// GatewayNamespaceFor resolves where registrations live. A function so the
	// sweeper does not have to duplicate the platform lookup, and so a test can
	// pin it.
	GatewayNamespaceFor func(ctx context.Context) (string, error)

	// Interval overrides expirySweepInterval. Tests set it short.
	Interval time.Duration

	// Now overrides the clock, so a test can move time forward without
	// sleeping.
	Now func() time.Time
}

// NeedLeaderElection makes the sweep run only on the leader, so two overlapping
// pods during a rollout do not both delete the same registrations.
func (s *ExpirySweeper) NeedLeaderElection() bool { return true }

// Start runs the sweep until the context is cancelled.
func (s *ExpirySweeper) Start(ctx context.Context) error {
	ticker := time.NewTicker(s.interval())
	defer ticker.Stop()

	log := logf.FromContext(ctx).WithName("expiry-sweep")

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.Sweep(ctx); err != nil {
				// Logged rather than returned: a failed sweep must not bring
				// the manager down, and the next tick retries anyway.
				log.Error(err, "expiry sweep failed; will retry on the next tick")
			}
		}
	}
}

// Sweep deletes every registration whose expiry has passed.
func (s *ExpirySweeper) Sweep(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("expiry-sweep")

	namespace, err := s.GatewayNamespaceFor(ctx)
	if err != nil {
		return err
	}
	if namespace == "" {
		// No platform, or no gateway namespace yet. Nothing to sweep.
		return nil
	}

	var registrations corev1.ConfigMapList
	if err := s.List(ctx, &registrations,
		client.InNamespace(namespace),
		client.MatchingLabels{agentgatewayv1alpha1.RegistrationLabel: "true"},
	); err != nil {
		if meta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	now := s.now()

	for i := range registrations.Items {
		cm := &registrations.Items[i]
		if !s.expired(cm, now) {
			continue
		}

		if err := s.Delete(ctx, cm); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			// One stubborn registration must not stop the others being swept.
			log.Error(err, "could not delete an expired key registration",
				"namespace", cm.Namespace, "name", cm.Name)
			continue
		}

		log.Info("revoked an expired participant key",
			"namespace", cm.Namespace,
			"name", cm.Name,
			"session", cm.Labels[agentgatewayv1alpha1.SessionLabel])
	}

	return nil
}

// expired reports whether every entry in a registration has passed its expiry.
//
// A registration holds one session's key, but the loop is over entries rather
// than assuming one: a ConfigMap with any still-valid entry must survive, or a
// sweep would revoke a live attendee.
func (s *ExpirySweeper) expired(cm *corev1.ConfigMap, now time.Time) bool {
	if len(cm.Data) == 0 {
		return false
	}

	for _, raw := range cm.Data {
		entry, err := parseRegistration(raw)
		if err != nil {
			// Unparseable: left alone rather than deleted. Something else wrote
			// it, and deleting another party's object on a parse failure is a
			// worse failure than leaving it.
			return false
		}

		expiry, ok := entry.Metadata[metadataKeyExpiresAt]
		if !ok {
			// No expiry recorded, so nothing to enforce. A registration written
			// by an older operator, or by hand.
			return false
		}

		at, err := time.Parse(time.RFC3339, expiry)
		if err != nil {
			return false
		}
		if now.Before(at) {
			return false
		}
	}

	return true
}

func (s *ExpirySweeper) interval() time.Duration {
	if s.Interval > 0 {
		return s.Interval
	}
	return expirySweepInterval
}

func (s *ExpirySweeper) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// SetupWithManager registers the sweep with the manager.
func (s *ExpirySweeper) SetupWithManager(mgr manager.Manager) error {
	return mgr.Add(s)
}

// gatewayNamespaceFromPlatform reads the gateway namespace off the platform
// singleton, returning empty when there is nothing to sweep.
func gatewayNamespaceFromPlatform(c client.Client) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		platform := &agentgatewayv1alpha1.AgentGatewayPlatform{}
		err := c.Get(ctx, client.ObjectKey{Name: agentgatewayv1alpha1.SingletonName}, platform)
		if err != nil {
			if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
				return "", nil
			}
			return "", err
		}
		if platform.Status.GatewayNamespace != "" {
			return platform.Status.GatewayNamespace, nil
		}
		return platform.GatewayNamespace(), nil
	}
}

var _ manager.Runnable = &ExpirySweeper{}
var _ manager.LeaderElectionRunnable = &ExpirySweeper{}
