package helm

import (
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// restClientGetter adapts a *rest.Config into the RESTClientGetter the Helm SDK
// wants.
//
// The Helm SDK is built for a CLI that starts from a kubeconfig file. Inside an
// operator the REST config already exists, so this hands it over directly rather
// than round-tripping through a file on disk.
type restClientGetter struct {
	cfg       *rest.Config
	namespace string
}

func newRESTClientGetter(cfg *rest.Config, namespace string) genericclioptions.RESTClientGetter {
	return &restClientGetter{cfg: cfg, namespace: namespace}
}

func (g *restClientGetter) ToRESTConfig() (*rest.Config, error) {
	return g.cfg, nil
}

func (g *restClientGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	dc, err := discovery.NewDiscoveryClientForConfig(g.cfg)
	if err != nil {
		return nil, err
	}
	return memory.NewMemCacheClient(dc), nil
}

func (g *restClientGetter) ToRESTMapper() (meta.RESTMapper, error) {
	dc, err := g.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}
	return restmapper.NewDeferredDiscoveryRESTMapper(dc), nil
}

// ToRawKubeConfigLoader returns a client config whose only real job is to
// answer Namespace(). Helm calls it to resolve the default namespace; nothing
// here loads a kubeconfig, since ToRESTConfig above already supplies the
// connection.
func (g *restClientGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	return clientcmd.NewDefaultClientConfig(
		*clientcmdapi.NewConfig(),
		&clientcmd.ConfigOverrides{Context: clientcmdapi.Context{Namespace: g.namespace}},
	)
}
