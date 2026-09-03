package helm

import (
	"bytes"
	"fmt"

	"helm.sh/helm/v4/pkg/chart/loader"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
)

// Chart is the concrete chart type this operator works with.
//
// Helm v4 splits chart handling into an interface (chart.Charter) and versioned
// concrete types. Everything embedded here is a v2 chart, so the alias keeps the
// version out of the rest of the codebase.
type Chart = chartv2.Chart

// LoadArchive parses a chart tarball.
//
// The only parse entrypoint in this operator, so the downcast Helm v4 requires
// is written once rather than at every call site.
func LoadArchive(data []byte) (*Chart, error) {
	c, err := loader.LoadArchive(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("load helm chart archive: %w", err)
	}
	chrt, ok := c.(*Chart)
	if !ok {
		return nil, fmt.Errorf("load helm chart archive: unexpected chart type %T", c)
	}
	return chrt, nil
}
