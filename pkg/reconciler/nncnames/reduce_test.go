package nncnames

import (
	"strings"
	"testing"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/vrfname"
)

func strptr(s string) *string { return &s }

// longName is a synthetic VRF name longer than MaxLen that reduces to a value
// shorter than MaxLen. It is intentionally unrelated to any real deployment.
const longName = "payment_gateway_worker" // reduces via the vowel rule

func TestReduce_RewritesKeysAndReferences(t *testing.T) {
	reduced := vrfname.Reduce(longName)
	if reduced == longName || len(reduced) > vrfname.MaxLen {
		t.Fatalf("test setup: %q did not reduce as expected (got %q)", longName, reduced)
	}

	spec := &v1alpha1.NodeNetworkConfigSpec{
		Layer2s: map[string]v1alpha1.Layer2{
			"l2a": {IRB: &v1alpha1.IRB{VRF: longName}},
		},
		ClusterVRF: &v1alpha1.VRF{
			PolicyRoutes: []v1alpha1.PolicyRoute{
				{NextHop: v1alpha1.NextHop{Vrf: strptr(longName)}},
			},
		},
		FabricVRFs: map[string]v1alpha1.FabricVRF{
			longName: {VNI: 100},
		},
		LocalVRFs: map[string]v1alpha1.VRF{
			"s-abcd1234": {
				VRFImports: []v1alpha1.VRFImport{
					{FromVRF: longName},
					{FromVRF: "cluster"},
				},
				StaticRoutes: []v1alpha1.StaticRoute{
					{Prefix: "0.0.0.0/0", NextHop: &v1alpha1.NextHop{Vrf: strptr(longName)}},
				},
			},
		},
	}

	if err := Reduce(spec); err != nil {
		t.Fatalf("Reduce returned error: %v", err)
	}

	// Fabric key rewritten.
	if _, ok := spec.FabricVRFs[reduced]; !ok {
		t.Errorf("FabricVRFs missing reduced key %q; got keys %v", reduced, keysFabric(spec.FabricVRFs))
	}
	if _, ok := spec.FabricVRFs[longName]; ok {
		t.Errorf("FabricVRFs still contains original long key %q", longName)
	}

	// IRB reference rewritten.
	if got := spec.Layer2s["l2a"].IRB.VRF; got != reduced {
		t.Errorf("IRB.VRF = %q, want %q", got, reduced)
	}

	// ClusterVRF policy-route next hop rewritten.
	if got := *spec.ClusterVRF.PolicyRoutes[0].NextHop.Vrf; got != reduced {
		t.Errorf("ClusterVRF PolicyRoute NextHop.Vrf = %q, want %q", got, reduced)
	}

	// LocalVRF import + static route rewritten; "cluster" preserved.
	local := spec.LocalVRFs["s-abcd1234"]
	if got := local.VRFImports[0].FromVRF; got != reduced {
		t.Errorf("LocalVRF FromVRF = %q, want %q", got, reduced)
	}
	if got := local.VRFImports[1].FromVRF; got != "cluster" {
		t.Errorf("cluster reference must be preserved, got %q", got)
	}
	if got := *local.StaticRoutes[0].NextHop.Vrf; got != reduced {
		t.Errorf("LocalVRF static route NextHop.Vrf = %q, want %q", got, reduced)
	}
}

func TestReduce_ShortNamesUnchanged(t *testing.T) {
	spec := &v1alpha1.NodeNetworkConfigSpec{
		FabricVRFs: map[string]v1alpha1.FabricVRF{
			"tenant_blue":  {VNI: 1},
			"tenant_green": {VNI: 2},
		},
	}
	if err := Reduce(spec); err != nil {
		t.Fatalf("Reduce returned error: %v", err)
	}
	for _, k := range []string{"tenant_blue", "tenant_green"} {
		if _, ok := spec.FabricVRFs[k]; !ok {
			t.Errorf("expected short key %q to be preserved", k)
		}
	}
}

func TestReduce_CollisionIsError(t *testing.T) {
	// Two distinct long names that reduce to the same value must be rejected.
	// They differ only in an inner vowel that the vowel rule strips.
	a := "bcdafg_hjklmnpqr" // 16 -> "bcdfg_hjklmnpqr"
	b := "bcdefg_hjklmnpqr" // 16 -> "bcdfg_hjklmnpqr"
	if vrfname.Reduce(a) != vrfname.Reduce(b) {
		t.Skipf("test names do not collide under current rule (%q vs %q)", vrfname.Reduce(a), vrfname.Reduce(b))
	}
	spec := &v1alpha1.NodeNetworkConfigSpec{
		FabricVRFs: map[string]v1alpha1.FabricVRF{a: {VNI: 1}, b: {VNI: 2}},
	}
	err := Reduce(spec)
	if err == nil {
		t.Fatal("expected collision error, got nil")
	}
	if !strings.Contains(err.Error(), "reduce to") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestReduce_IrreducibleKeyIsError(t *testing.T) {
	// A long incompressible name (no removable vowels, no underscores) cannot be
	// reduced to fit and must be reported rather than silently passed through.
	irreducible := "wwwwwwwwxxxxxxxx" // 16, no vowels/underscores
	if vrfname.CanReduce(irreducible) {
		t.Skipf("test name unexpectedly reducible: %q", vrfname.Reduce(irreducible))
	}
	spec := &v1alpha1.NodeNetworkConfigSpec{
		FabricVRFs: map[string]v1alpha1.FabricVRF{irreducible: {VNI: 1}},
	}
	err := Reduce(spec)
	if err == nil {
		t.Fatal("expected error for irreducible VRF key, got nil")
	}
	if !strings.Contains(err.Error(), "cannot be reduced") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func keysFabric(m map[string]v1alpha1.FabricVRF) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
