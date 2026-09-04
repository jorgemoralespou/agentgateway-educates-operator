package helm

// Test helpers, deliberately in a non-_test.go file so the controller package's
// envtest suite can use them too.
//
// The real Helm client needs a cluster it can apply manifests to. envtest is an
// API server with no controllers and no kubelet, so a chart's Deployment would
// be accepted and then never do anything, Helm's own apply would still work,
// but slowly and pointlessly. An in-memory release store keeps the converge
// logic under test while leaving the cluster out of it.

import (
	"fmt"
	"io"
	"log/slog"
	"sync"

	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart/common"
	kubefake "helm.sh/helm/v4/pkg/kube/fake"
	"helm.sh/helm/v4/pkg/registry"
	releasecommon "helm.sh/helm/v4/pkg/release/common"
	release "helm.sh/helm/v4/pkg/release/v1"
	"helm.sh/helm/v4/pkg/storage"
	"helm.sh/helm/v4/pkg/storage/driver"
)

// memoryClientKubeVersion is what charts see as the cluster version.
//
// Helm's DefaultCapabilities pins an older version than agentgateway's charts
// accept, so a chart with a kubeVersion constraint would fail to render for a
// reason that has nothing to do with what is under test.
const memoryClientKubeVersion = "v1.36.0"

// NewMemoryClient builds a Helm client backed by an in-memory release store.
func NewMemoryClient(namespace string) (*Client, error) {
	registryClient, err := registry.NewClient()
	if err != nil {
		return nil, fmt.Errorf("new registry client: %w", err)
	}

	kubeVersion, err := common.ParseKubeVersion(memoryClientKubeVersion)
	if err != nil {
		return nil, fmt.Errorf("parse kube version: %w", err)
	}

	caps := *common.DefaultCapabilities
	caps.KubeVersion = *kubeVersion

	cfg := &action.Configuration{
		Releases:       storage.Init(driver.NewMemory()),
		KubeClient:     &kubefake.PrintingKubeClient{Out: io.Discard, LogOutput: io.Discard},
		Capabilities:   &caps,
		RegistryClient: registryClient,
	}
	cfg.SetLogger(discardHandler())

	return &Client{
		cfg:       cfg,
		namespace: namespace,
		// The fake kube client cannot apply CRDs, and no test needs them
		// applied: the CRDs envtest serves come from the chart directory.
		skipCRDs: true,
	}, nil
}

// SeedRelease inserts a release record directly into the store, bypassing
// install and upgrade.
//
// Test-only: it is how a test sets up a failed, pending, or
// somebody-else's-release starting state that would otherwise be hard to
// produce.
func (c *Client) SeedRelease(name string, version int, status releasecommon.Status, chrt *Chart, config map[string]any, labels map[string]string) error {
	rel := &release.Release{
		Name:      name,
		Namespace: c.namespace,
		Version:   version,
		Info:      &release.Info{Status: status},
		Chart:     chrt,
		Config:    config,
		Labels:    labels,
	}
	return c.cfg.Releases.Create(rel)
}

// MemoryHelmFactory hands out one memory-backed client per namespace, keeping
// the same store across calls so a release installed in one reconcile is
// visible to the next.
type MemoryHelmFactory struct {
	mu      sync.Mutex
	clients map[string]*Client
}

// NewMemoryHelmFactory builds an empty factory.
func NewMemoryHelmFactory() *MemoryHelmFactory {
	return &MemoryHelmFactory{clients: map[string]*Client{}}
}

// For returns the client for a namespace, creating it on first use.
func (f *MemoryHelmFactory) For(namespace string) (*Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if c, ok := f.clients[namespace]; ok {
		return c, nil
	}
	c, err := NewMemoryClient(namespace)
	if err != nil {
		return nil, err
	}
	f.clients[namespace] = c
	return c, nil
}

// discardHandler silences Helm's own logging in tests, which is verbose and
// tells you nothing about the code under test.
func discardHandler() slog.Handler {
	return slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})
}
