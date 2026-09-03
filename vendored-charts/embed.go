// Package vendoredcharts holds the agentgateway Helm charts this operator
// installs, embedded into its own binary.
//
// The chart versions are compile-time constants rather than spec fields:
// upgrading agentgateway means upgrading this operator (ADR-0005). That is the
// same stance the Educates v4 installer takes, and it is honest about the
// coupling — the operator is tested against the charts it embeds. A cluster
// operator needing a different version points the operator at their own gateway
// with the External provider instead.
//
// Tarballs are committed next to this file because //go:embed cannot escape the
// package directory. Their integrity is pinned in SHA256SUMS and checked by
// `make verify-vendored-charts`; TestChartVersionsMatchTarballs also fails if a
// constant here and its tarball ever disagree.
package vendoredcharts

import (
	_ "embed"

	"github.com/educates/agentgateway-educates-operator/internal/helm"
)

// AgentgatewayChartVersion is the version of the agentgateway chart, which
// installs the control plane. Reported in
// status.bundledChartVersions.agentgateway.
const AgentgatewayChartVersion = "1.5.0"

// AgentgatewayCRDsChartVersion is the version of the agentgateway-crds chart.
// Installed before the main chart: the control plane comes up but never becomes
// ready without its CRDs, which is a silent failure.
const AgentgatewayCRDsChartVersion = "1.5.0"

// AgentgatewayAppVersion is the agentgateway release the embedded charts
// install. Both charts carry the same appVersion.
const AgentgatewayAppVersion = "1.5.0"

//go:embed agentgateway-1.5.0.tgz
var agentgatewayTarball []byte

//go:embed agentgateway-crds-1.5.0.tgz
var agentgatewayCRDsTarball []byte

// Agentgateway parses the embedded agentgateway chart and returns it ready for
// the Helm SDK.
//
// Source: oci://ghcr.io/agentgateway/charts/agentgateway
func Agentgateway() (*helm.Chart, error) {
	return helm.LoadArchive(agentgatewayTarball)
}

// AgentgatewayCRDs parses the embedded agentgateway-crds chart and returns it
// ready for the Helm SDK.
//
// Source: oci://ghcr.io/agentgateway/charts/agentgateway-crds
func AgentgatewayCRDs() (*helm.Chart, error) {
	return helm.LoadArchive(agentgatewayCRDsTarball)
}
