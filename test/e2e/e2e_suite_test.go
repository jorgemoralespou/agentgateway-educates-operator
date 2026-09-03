// Package e2e holds the one test that cannot run under envtest.
//
// envtest is an API server with no controllers, and in particular **no garbage
// collector**: owner references are recorded but cascading deletion never
// happens. The property ADR-0002 rests on — a Secret placed in the workshop
// namespace but owned by the session namespace is collected when the session
// ends — would therefore go untested at the envtest seam no matter how it were
// written.
//
// So exactly one test runs against a real cluster, and it is deliberately
// narrow: create a session grant, delete the owning namespace, and assert the
// Secret is collected and the finalizer removed the registration. Everything
// else belongs at the envtest seam, where it runs in CI in seconds.
//
// Not part of `make test`. Run it with `make test-e2e` against a kind cluster.
package e2e

import (
	"context"
	"os"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
)

var (
	ctx       context.Context
	cancel    context.CancelFunc
	k8sClient client.Client
)

func TestE2E(t *testing.T) {
	if os.Getenv("KUBECONFIG") == "" && os.Getenv("HOME") == "" {
		t.Skip("no kubeconfig available; this test needs a real cluster")
	}
	RegisterFailHandler(Fail)
	RunSpecs(t, "e2e Suite")
}

var _ = BeforeSuite(func() {
	ctx, cancel = context.WithCancel(context.TODO())

	Expect(agentgatewayv1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())

	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
	Expect(err).NotTo(HaveOccurred(),
		"this test needs a real cluster; point KUBECONFIG at a kind cluster running the operator")

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	cancel()
})

const (
	// Generous timeouts: this runs against a real cluster where the garbage
	// collector and namespace controller work on their own schedule.
	e2eTimeout  = 3 * time.Minute
	e2eInterval = 2 * time.Second
)
