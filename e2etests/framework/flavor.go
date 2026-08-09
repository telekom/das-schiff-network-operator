package framework

import (
	"bytes"
	"os"

	"github.com/onsi/ginkgo/v2"
)

const (
	craFlavorEnv   = "E2E_CRA_FLAVOR"
	craFlavorFRR   = "frr"
	craFlavorGrout = "grout"
)

// CRAFlavor returns the configured CRA flavor. It mirrors e2e/setup/flavor.go.
func (*Framework) CRAFlavor() string {
	switch os.Getenv(craFlavorEnv) {
	case craFlavorGrout:
		return craFlavorGrout
	default:
		return craFlavorFRR
	}
}

// IsGrout reports whether the e2e lab is using the grout CRA flavor.
func (f *Framework) IsGrout() bool {
	return f.CRAFlavor() == craFlavorGrout
}

// SkipIfGrout skips the calling spec when the lab runs the grout CRA flavor.
//
// grout is a DPDK fast path, so anything the other flavors implement by putting
// rules on kernel interfaces inside the CRA has no effect there: the packets
// never reach the kernel. Where grout has no equivalent of its own, the feature
// is simply unavailable and the test cannot pass. Pass the feature name so the
// skip message says which one, e.g. SkipIfGrout("traffic mirroring").
func (f *Framework) SkipIfGrout(feature string) {
	if f.IsGrout() {
		ginkgo.Skip(feature + " is not supported by the grout CRA flavor")
	}
}

// TransportPlaceholder is the token workload-CNI fixtures carry where the
// CRA-side transport belongs, so one set of NADs can drive every flavour.
const TransportPlaceholder = "__TRANSPORT__"

// WorkloadCNITransport is the `transport` a workload-CNI NAD has to request on
// this flavour.
//
// It is not cosmetic on grout: that fast path cannot adopt a netdev created
// outside it, so the CNI must let grout create the net_tap and move that into
// the pod instead of building a veth pair. The agent enforces it
// (workloadcni.RequireGroutTap), so a fixture left on the default would fail
// CNI ADD rather than quietly attach the wrong way.
func (f *Framework) WorkloadCNITransport() string {
	if f.IsGrout() {
		return "grouttap"
	}
	return "veth"
}

// ResolveTransport substitutes TransportPlaceholder in a manifest with the
// transport this flavour needs.
func (f *Framework) ResolveTransport(manifest []byte) []byte {
	return bytes.ReplaceAll(manifest, []byte(TransportPlaceholder), []byte(f.WorkloadCNITransport()))
}
