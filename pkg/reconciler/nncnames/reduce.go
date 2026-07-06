// Package nncnames applies the VRF name-reduction transform to a fully
// assembled NodeNetworkConfigSpec. It rewrites every VRF name — both the map
// keys (FabricVRFs, LocalVRFs) and every cross-reference to a VRF name
// (Layer2 IRB, VRFImport.FromVRF and NextHop.Vrf in static/policy routes) — to
// its datapath-safe reduced form.
//
// Because the reduction is deterministic, a key and any reference to it reduce
// identically, so references stay consistent without an explicit rename map.
package nncnames

import (
	"fmt"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/vrfname"
)

// Reduce rewrites every VRF name in spec to its reduced form. It is
// deterministic and idempotent. It returns an error if two distinct VRF names
// reduce to the same value (a collision), which must be surfaced rather than
// silently merging two VRFs.
func Reduce(spec *v1alpha1.NodeNetworkConfigSpec) error {
	if spec == nil {
		return nil
	}

	fabric, err := reduceKeys(spec.FabricVRFs)
	if err != nil {
		return fmt.Errorf("fabric VRFs: %w", err)
	}
	spec.FabricVRFs = fabric

	local, err := reduceKeys(spec.LocalVRFs)
	if err != nil {
		return fmt.Errorf("local VRFs: %w", err)
	}
	spec.LocalVRFs = local

	if spec.ClusterVRF != nil {
		reduceVRFRefs(spec.ClusterVRF)
	}
	for k := range spec.FabricVRFs {
		v := spec.FabricVRFs[k]
		reduceVRFRefs(&v.VRF)
		spec.FabricVRFs[k] = v
	}
	for k := range spec.LocalVRFs {
		v := spec.LocalVRFs[k]
		reduceVRFRefs(&v)
		spec.LocalVRFs[k] = v
	}

	for k := range spec.Layer2s {
		l2 := spec.Layer2s[k]
		if l2.IRB != nil {
			l2.IRB.VRF = vrfname.Reduce(l2.IRB.VRF)
			spec.Layer2s[k] = l2
		}
	}

	return nil
}

// reduceKeys returns a copy of m with every key reduced. It errors if two
// distinct keys reduce to the same value.
func reduceKeys[T any](m map[string]T) (map[string]T, error) {
	if len(m) == 0 {
		return m, nil
	}
	out := make(map[string]T, len(m))
	origin := make(map[string]string, len(m))
	for k, v := range m {
		nk := vrfname.Reduce(k)
		// Reduce is best-effort: an incompressible name can still exceed the
		// limit. Fail early here so the reconciler surfaces it, rather than
		// emitting a NodeNetworkConfig the datapath will reject (IFNAMSIZ).
		if len(nk) > vrfname.MaxLen {
			return nil, fmt.Errorf("VRF name %q cannot be reduced to fit the %d-character interface-name limit (best effort %q)", k, vrfname.MaxLen, nk)
		}
		if prev, dup := origin[nk]; dup {
			return nil, fmt.Errorf("VRF names %q and %q both reduce to %q", prev, k, nk)
		}
		origin[nk] = k
		out[nk] = v
	}
	return out, nil
}

// reduceVRFRefs rewrites every VRF-name reference inside a VRF in place.
func reduceVRFRefs(v *v1alpha1.VRF) {
	for i := range v.VRFImports {
		v.VRFImports[i].FromVRF = vrfname.Reduce(v.VRFImports[i].FromVRF)
	}
	for i := range v.StaticRoutes {
		if v.StaticRoutes[i].NextHop != nil && v.StaticRoutes[i].NextHop.Vrf != nil {
			r := vrfname.Reduce(*v.StaticRoutes[i].NextHop.Vrf)
			v.StaticRoutes[i].NextHop.Vrf = &r
		}
	}
	for i := range v.PolicyRoutes {
		if v.PolicyRoutes[i].NextHop.Vrf != nil {
			r := vrfname.Reduce(*v.PolicyRoutes[i].NextHop.Vrf)
			v.PolicyRoutes[i].NextHop.Vrf = &r
		}
	}
}
