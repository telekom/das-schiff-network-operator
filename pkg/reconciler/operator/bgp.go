package operator

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"sort"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
	corev1 "k8s.io/api/core/v1"
)

func (crr *ConfigRevisionReconciler) buildBgpPeer(loopbackIP *string, listenRange *string, peer v1alpha1.BGPRevision) (*v1alpha1.BGPPeer, error) {
	var family AddressFamily
	if loopbackIP != nil {
		if ip := net.ParseIP(*loopbackIP); ip != nil {
			if ip.To4() != nil {
				family = IPv4
			} else {
				family = IPv6
			}
		} else {
			return nil, fmt.Errorf("failed to parse loopback IP %s", *loopbackIP)
		}
	} else if listenRange != nil {
		if ip, _, err := net.ParseCIDR(*listenRange); err != nil {
			return nil, fmt.Errorf("failed to parse listen range %s: %w", *listenRange, err)
		} else {
			if ip.To4() != nil {
				family = IPv4
			} else {
				family = IPv6
			}
		}
	}

	bgpAddressFamily := &v1alpha1.AddressFamily{
		MaxPrefixes: peer.MaximumPrefixes,
		ExportFilter: &v1alpha1.Filter{
			DefaultAction: v1alpha1.Action{
				Type: v1alpha1.Reject,
			},
		},
		ImportFilter: &v1alpha1.Filter{
			DefaultAction: v1alpha1.Action{
				Type: v1alpha1.Reject,
			},
		},
	}

	if filterItems, err := crr.buildFilterItems(peer.Export, family); err != nil {
		return nil, fmt.Errorf("failed to build filter items for export: %w", err)
	} else {
		bgpAddressFamily.ExportFilter.Items = append(bgpAddressFamily.ExportFilter.Items, filterItems...)
	}
	if filterItems, err := crr.buildFilterItems(peer.Import, family); err != nil {
		return nil, fmt.Errorf("failed to build filter items for import: %w", err)
	} else {
		bgpAddressFamily.ImportFilter.Items = append(bgpAddressFamily.ImportFilter.Items, filterItems...)
	}

	bgpPeer := v1alpha1.BGPPeer{
		ListenRange:   listenRange,
		Address:       loopbackIP,
		RemoteASN:     peer.RemoteASN,
		HoldTime:      peer.HoldTime,
		KeepaliveTime: peer.KeepaliveTime,
	}

	if family == IPv4 {
		bgpPeer.IPv4 = bgpAddressFamily
	} else {
		bgpPeer.IPv6 = bgpAddressFamily
	}
	return &bgpPeer, nil
}

func (crr *ConfigRevisionReconciler) buildNodeBgpPeers(node *corev1.Node, revision *v1alpha1.NetworkConfigRevision, c *v1alpha1.NodeNetworkConfig) error {
	bgp := revision.Spec.BGP
	sort.SliceStable(bgp, func(i, j int) bool {
		return bgp[i].Name < bgp[j].Name
	})

	for _, peer := range bgp {
		if !matchSelector(node, peer.NodeSelector) {
			continue
		}

		if peer.PeeringVlan != nil {
			// Find peering VLAN...
			var irbIPs []string
			var vrf string
			for _, l2 := range revision.Spec.Layer2 {
				if !matchSelector(node, l2.NodeSelector) {
					continue
				}

				if l2.Name == peer.PeeringVlan.Name {
					irbIPs = l2.AnycastGateways
					vrf = l2.VRF
					break
				}
			}
			if len(irbIPs) == 0 {
				return fmt.Errorf("no IRB IPs found for peering VLAN %s", peer.PeeringVlan.Name)
			}

			for _, ip := range irbIPs {
				// Parse IP address (which is CIDR) and get network+CIDR
				_, ipNet, err := net.ParseCIDR(ip)
				if err != nil {
					return fmt.Errorf("failed to parse IP address %s: %w", ip, err)
				}

				listenRange := ipNet.String()
				bgpPeer, err := crr.buildBgpPeer(nil, &listenRange, peer)
				if err != nil {
					return fmt.Errorf("failed to build BGP peer: %w", err)
				}

				if vrf != "" {
					if fabricVrf, ok := c.Spec.FabricVRFs[vrf]; ok {
						fabricVrf.BGPPeers = append(fabricVrf.BGPPeers, *bgpPeer)
						c.Spec.FabricVRFs[vrf] = fabricVrf
					} else {
						return fmt.Errorf("fabric VRF %s not found", vrf)
					}
				} else {
					c.Spec.DefaultVRF.BGPPeers = append(c.Spec.DefaultVRF.BGPPeers, *bgpPeer)
				}
			}
		} else if peer.LoopbackPeer != nil && len(peer.LoopbackPeer.IPAddresses) > 0 {
			for _, ip := range peer.LoopbackPeer.IPAddresses {
				bgpPeer, err := crr.buildBgpPeer(&ip, nil, peer)
				if err != nil {
					return fmt.Errorf("failed to build BGP peer: %w", err)
				}
				c.Spec.DefaultVRF.BGPPeers = append(c.Spec.DefaultVRF.BGPPeers, *bgpPeer)
			}
		} else {
			return fmt.Errorf("no loopback IPs found for BGP peer %s", peer.Name)
		}
	}
	return nil
}

func (crr *ConfigRevisionReconciler) buildNetplanDummies(node *corev1.Node, revision *v1alpha1.NetworkConfigRevision) (map[string]netplan.Device, error) {
	dummies := make(map[string]netplan.Device)
	for _, bgp := range revision.Spec.BGP {
		if !matchSelector(node, bgp.NodeSelector) {
			continue
		}
		h := sha256.New()
		h.Write([]byte(bgp.Name))
		interfaceName := fmt.Sprintf("%x", h.Sum(nil))[:10]

		if bgp.LoopbackPeer != nil && len(bgp.LoopbackPeer.IPAddresses) > 0 {
			dummy := map[string]interface{}{
				"addresses": bgp.LoopbackPeer.IPAddresses,
			}

			rawDummy, err := json.Marshal(dummy)
			if err != nil {
				return nil, fmt.Errorf("error marshaling dummy: %w", err)
			}

			dummies[fmt.Sprintf("bgp%s", interfaceName)] = netplan.Device{
				Raw: rawDummy,
			}
		}
	}
	return dummies, nil
}
