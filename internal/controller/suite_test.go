package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
)

// The reconciler under envtest is the primary test seam. A test creates a
// resource against a real API server and asserts on the objects that result:
// which exist, in which namespace, owned by what, containing what shape of
// data, and what the status says. That is what an attendee or a cluster
// operator could observe, and it is where the interesting failures live —
// placement, ownership, deletion ordering, idempotency.
//
// Tests here deliberately do not assert how the reconciler is structured or how
// many API calls it made. A test that would still pass after a rewrite of the
// controller's internals, and fail if placement or lifecycle changed, is the
// one worth having.

var (
	ctx       context.Context
	cancel    context.CancelFunc
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client
)

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.TODO())

	Expect(agentgatewayv1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())
	Expect(gatewayv1.Install(scheme.Scheme)).To(Succeed())

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			// The chart's crds/ is the canonical location for this operator's
			// own CRDs — there is no config/ kustomize tree.
			filepath.Join("..", "..", "charts", "agentgateway-educates-operator", "crds"),
			// Third-party kinds the reconcilers create. In production this
			// operator installs these; a test cannot wait for that.
			filepath.Join("testdata", "crds", "gateway-api"),
			filepath.Join("testdata", "crds", "agentgateway"),
		},
		ErrorIfCRDPathMissing: true,
	}

	if dir := firstFoundEnvTestBinaryDir(); dir != "" {
		testEnv.BinaryAssetsDirectory = dir
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancel()
	Eventually(func() error {
		return testEnv.Stop()
	}, time.Minute, time.Second).Should(Succeed())
})

// firstFoundEnvTestBinaryDir locates the API server binaries so the suite can
// also be run from an IDE, which does not set KUBEBUILDER_ASSETS.
func firstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}
