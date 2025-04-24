package operator

import (
	"fmt"
	"sort"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

func (crr *ConfigRevisionReconciler) buildNodeVrf(node *corev1.Node, revision *v1alpha1.NetworkConfigRevision, c *v1alpha1.NodeNetworkConfig) error {
	c.Spec.FabricVRFs = make(map[string]v1alpha1.FabricVRF)

	defaultImportMap := make(map[string]v1alpha1.VRFImport)

	vrfs := revision.Spec.Vrf
	sort.SliceStable(vrfs, func(i, j int) bool {
		return vrfs[i].Seq < vrfs[j].Seq
	})

	for i := range vrfs {
		if !matchSelector(node, vrfs[i].NodeSelector) {
			continue
		}

		if _, ok := c.Spec.FabricVRFs[vrfs[i].VRF]; !ok {
			fabricVrf, err := crr.createFabricVRF(&vrfs[i])
			if err != nil {
				return fmt.Errorf("failed to create fabric VRF definition: %w", err)
			}
			c.Spec.FabricVRFs[vrfs[i].VRF] = fabricVrf
		}

		fabricVrf := c.Spec.FabricVRFs[vrfs[i].VRF]
		if err := updateFabricVRF(&fabricVrf, &vrfs[i], defaultImportMap); err != nil {
			return fmt.Errorf("failed to update fabric VRF definition: %w", err)
		}
		c.Spec.FabricVRFs[vrfs[i].VRF] = fabricVrf
	}

	c.Spec.ClusterVRF = &v1alpha1.VRF{}
	for _, vrfImport := range defaultImportMap {
		c.Spec.ClusterVRF.VRFImports = append(c.Spec.ClusterVRF.VRFImports, vrfImport)
	}

	return nil
}

func (crr *ConfigRevisionReconciler) createFabricVRF(vrf *v1alpha1.VRFRevision) (v1alpha1.FabricVRF, error) {
	vni := uint32(0) //nolint:wastedassign
	rt := ""         //nolint:wastedassign
	if vrf.RouteTarget != nil && vrf.VNI != nil {
		vni = uint32(*vrf.VNI) //nolint:gosec
		rt = *vrf.RouteTarget
	} else if configVni, configRt, err := crr.vrfConfig.GetVNIAndRT(vrf.VRF); err == nil {
		vni = uint32(configVni) //nolint:gosec
		rt = configRt
	} else {
		return v1alpha1.FabricVRF{}, fmt.Errorf("error getting VNI and RT for VRF %s: %w", vrf.VRF, err)
	}

	fabricVrf := v1alpha1.FabricVRF{
		VRF: v1alpha1.VRF{
			VRFImports: []v1alpha1.VRFImport{
				{
					FromVRF: "cluster",
					Filter: v1alpha1.Filter{
						DefaultAction: v1alpha1.Action{
							Type: v1alpha1.Reject,
						},
					},
				},
			},
		},
		VNI:                    vni,
		EVPNImportRouteTargets: []string{},
		EVPNExportRouteTargets: []string{},
		EVPNExportFilter: &v1alpha1.Filter{
			DefaultAction: v1alpha1.Action{
				Type: v1alpha1.Reject,
			},
		},
	}
	if rt != "" {
		fabricVrf.EVPNImportRouteTargets = append(fabricVrf.EVPNImportRouteTargets, rt)
		fabricVrf.EVPNExportRouteTargets = append(fabricVrf.EVPNExportRouteTargets, rt)
	}
	return fabricVrf, nil
}

func updateFabricVRF(fabricVrf *v1alpha1.FabricVRF, vrf *v1alpha1.VRFRevision, defaultImportMap map[string]v1alpha1.VRFImport) error {
	for _, aggregate := range vrf.Aggregate {
		fabricVrf.StaticRoutes = append(fabricVrf.StaticRoutes, v1alpha1.StaticRoute{
			Prefix: aggregate,
		})
	}

	processExports(vrf, fabricVrf)
	processImports(vrf, defaultImportMap)

	return nil
}

func processExports(vrf *v1alpha1.VRFRevision, fabricVrf *v1alpha1.FabricVRF) {
	sort.SliceStable(vrf.Export, func(i, j int) bool {
		return vrf.Export[i].Seq < vrf.Export[j].Seq
	})

	for _, export := range vrf.Export {
		filterItem := v1alpha1.FilterItem{
			Matcher: v1alpha1.Matcher{
				Prefix: &v1alpha1.PrefixMatcher{
					Prefix: export.CIDR,
					Ge:     export.GE,
					Le:     export.LE,
				},
			},
		}
		filterItem.Action = v1alpha1.Action{
			Type: v1alpha1.Reject,
		}
		if export.Action == permitRoute {
			filterItem.Action.Type = v1alpha1.Accept
		}
		fabricVrf.EVPNExportFilter.Items = append(fabricVrf.EVPNExportFilter.Items, filterItem)

		vrfImportItem := filterItem.DeepCopy()
		if vrf.Community != nil {
			additive := true
			vrfImportItem.Action.ModifyRoute = &v1alpha1.ModifyRouteAction{
				AddCommunities:      []string{*vrf.Community},
				AdditiveCommunities: &additive,
			}
		}
		vrfImport := fabricVrf.VRFImports[0]
		vrfImport.Filter.Items = append(vrfImport.Filter.Items, *vrfImportItem)
		fabricVrf.VRFImports[0] = vrfImport
	}
}

func processImports(vrf *v1alpha1.VRFRevision, defaultImportMap map[string]v1alpha1.VRFImport) {
	sort.SliceStable(vrf.Import, func(i, j int) bool {
		return vrf.Import[i].Seq < vrf.Import[j].Seq
	})

	for _, vrfImport := range vrf.Import {
		if defaultImportMap == nil {
			defaultImportMap = make(map[string]v1alpha1.VRFImport)
		}
		if _, ok := defaultImportMap[vrf.VRF]; !ok {
			defaultImportMap[vrf.VRF] = v1alpha1.VRFImport{
				FromVRF: vrf.VRF,
				Filter: v1alpha1.Filter{
					DefaultAction: v1alpha1.Action{
						Type: v1alpha1.Reject,
					},
				},
			}
		}

		filterItem := v1alpha1.FilterItem{
			Matcher: v1alpha1.Matcher{
				Prefix: &v1alpha1.PrefixMatcher{
					Prefix: vrfImport.CIDR,
					Ge:     vrfImport.GE,
					Le:     vrfImport.LE,
				},
			},
		}
		filterItem.Action = v1alpha1.Action{
			Type: v1alpha1.Reject,
		}
		if vrfImport.Action == permitRoute {
			filterItem.Action.Type = v1alpha1.Accept
		}
		vrfImport := defaultImportMap[vrf.VRF]
		vrfImport.Filter.Items = append(vrfImport.Filter.Items, filterItem)
		defaultImportMap[vrf.VRF] = vrfImport
	}
}
