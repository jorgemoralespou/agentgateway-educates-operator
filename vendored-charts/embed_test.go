package vendoredcharts

import (
	"strings"
	"testing"

	"github.com/educates/agentgateway-educates-operator/internal/helm"
)

// The version constants above are what the operator reports in status and uses
// in every converge fingerprint. If a constant and its tarball ever disagree —
// someone refreshed a chart without updating the constant, or the reverse — the
// operator would report a version it is not running. Fail the build instead.
func TestChartVersionsMatchTarballs(t *testing.T) {
	tests := []struct {
		name  string
		load  func() (*helm.Chart, error)
		want  string
		cname string
	}{
		{
			name:  "agentgateway",
			load:  Agentgateway,
			want:  AgentgatewayChartVersion,
			cname: "agentgateway",
		},
		{
			name:  "agentgateway-crds",
			load:  AgentgatewayCRDs,
			want:  AgentgatewayCRDsChartVersion,
			cname: "agentgateway-crds",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chrt, err := tc.load()
			if err != nil {
				t.Fatalf("load chart: %v", err)
			}
			if chrt.Metadata == nil {
				t.Fatal("chart has no metadata")
			}
			if chrt.Metadata.Version != tc.want {
				t.Errorf("chart version = %q, constant = %q; refresh the constant or the tarball",
					chrt.Metadata.Version, tc.want)
			}
			if chrt.Metadata.Name != tc.cname {
				t.Errorf("chart name = %q, want %q", chrt.Metadata.Name, tc.cname)
			}
		})
	}
}

// Both charts install the same agentgateway release, and the app version
// constant is what a cluster operator reads to know which agentgateway is
// running.
func TestAppVersionMatchesTarballs(t *testing.T) {
	for _, load := range []func() (*helm.Chart, error){Agentgateway, AgentgatewayCRDs} {
		chrt, err := load()
		if err != nil {
			t.Fatalf("load chart: %v", err)
		}
		if chrt.Metadata.AppVersion != AgentgatewayAppVersion {
			t.Errorf("%s appVersion = %q, constant = %q",
				chrt.Metadata.Name, chrt.Metadata.AppVersion, AgentgatewayAppVersion)
		}
	}
}

// Both charts must actually parse. A truncated or corrupt tarball would
// otherwise only fail at install time, on a real cluster.
func TestChartsLoad(t *testing.T) {
	if _, err := Agentgateway(); err != nil {
		t.Errorf("agentgateway chart failed to load: %v", err)
	}
	if _, err := AgentgatewayCRDs(); err != nil {
		t.Errorf("agentgateway-crds chart failed to load: %v", err)
	}
}

// The CRDs chart is the reason install order matters: the control plane comes up
// but never becomes ready without these, which is a silent failure (ADR-0005).
func TestCRDsChartCarriesTheAgentgatewayCRDs(t *testing.T) {
	chrt, err := AgentgatewayCRDs()
	if err != nil {
		t.Fatalf("load chart: %v", err)
	}

	// The CRDs ship as templates rather than in crds/, so count those.
	if len(chrt.Templates) == 0 {
		t.Fatal("agentgateway-crds chart carries no templates")
	}

	wanted := map[string]bool{
		"agentgatewaypolicies":   false,
		"agentgatewaymodels":     false,
		"agentgatewayparameters": false,
		"agentgatewaybackends":   false,
	}
	for _, tpl := range chrt.Templates {
		for kind := range wanted {
			if strings.Contains(tpl.Name, kind) {
				wanted[kind] = true
			}
		}
	}
	for kind, found := range wanted {
		if !found {
			t.Errorf("agentgateway-crds chart has no template for %s", kind)
		}
	}
}
