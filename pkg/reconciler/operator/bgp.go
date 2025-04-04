package operator

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sort"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
	corev1 "k8s.io/api/core/v1"
)

const (
	bgpMultihop = uint32(2)
)

var (
	hbnHostNextHop = os.Getenv("HBN_HOST_NEXTHOP")
)

func buildBgpPeer(loopbackIP, listenRange *string, peer *v1alpha1.BGPRevision) (*v1alpha1.BGPPeer, error) {
	var family AddressFamily
	if loopbackIP != nil {
		ip := net.ParseIP(*loopbackIP)
		if ip == nil {
			return nil, fmt.Errorf("failed to parse loopback IP %s", *loopbackIP)
		}

		if ip.To4() != nil {
			family = IPv4
		} else {
			family = IPv6
		}
	} else if listenRange != nil {
		ip, _, err := net.ParseCIDR(*listenRange)
		if err != nil {
			return nil, fmt.Errorf("failed to parse listen range %s: %w", *listenRange, err)
		}

		if ip.To4() != nil {
			family = IPv4
		} else {
			family = IPv6
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

	filterItems, err := buildFilterItems(peer.Export, family)
	if err != nil {
		return nil, fmt.Errorf("failed to build filter items for export: %w", err)
	}
	bgpAddressFamily.ExportFilter.Items = append(bgpAddressFamily.ExportFilter.Items, filterItems...)

	filterItems, err = buildFilterItems(peer.Import, family)
	if err != nil {
		return nil, fmt.Errorf("failed to build filter items for import: %w", err)
	}
	bgpAddressFamily.ImportFilter.Items = append(bgpAddressFamily.ImportFilter.Items, filterItems...)

	bgpPeer := v1alpha1.BGPPeer{
		ListenRange:   listenRange,
		Address:       loopbackIP,
		RemoteASN:     peer.RemoteASN,
		HoldTime:      peer.HoldTime,
		KeepaliveTime: peer.KeepaliveTime,
	}

	if loopbackIP != nil {
		multihop := bgpMultihop
		bgpPeer.Multihop = &multihop
	}

	if family == IPv4 {
		bgpPeer.IPv4 = bgpAddressFamily
	} else {
		bgpPeer.IPv6 = bgpAddressFamily
	}
	return &bgpPeer, nil
}

func buildPeeringVlanPeer(node *corev1.Node, peer *v1alpha1.BGPRevision, revision *v1alpha1.NetworkConfigRevision, c *v1alpha1.NodeNetworkConfig) error {
	if peer.PeeringVlan == nil {
		return fmt.Errorf("peering VLAN is nil")
	}

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
		bgpPeer, err := buildBgpPeer(nil, &listenRange, peer)
		if err != nil {
			return fmt.Errorf("failed to build BGP peer: %w", err)
		}

		if vrf != "" {
			fabricVrf, ok := c.Spec.FabricVRFs[vrf]
			if !ok {
				return fmt.Errorf("fabric VRF %s not found", vrf)
			}
			fabricVrf.BGPPeers = append(fabricVrf.BGPPeers, *bgpPeer)
			c.Spec.FabricVRFs[vrf] = fabricVrf
		} else {
			c.Spec.ClusterVRF.BGPPeers = append(c.Spec.ClusterVRF.BGPPeers, *bgpPeer)
		}
	}
	return nil
}

func buildNodeBgpPeers(node *corev1.Node, revision *v1alpha1.NetworkConfigRevision, c *v1alpha1.NodeNetworkConfig) error {
	bgp := revision.Spec.BGP
	sort.SliceStable(bgp, func(i, j int) bool {
		return bgp[i].Name < bgp[j].Name
	})

	for i := range bgp {
		if !matchSelector(node, bgp[i].NodeSelector) {
			continue
		}

		switch {
		case bgp[i].PeeringVlan != nil:
			if err := buildPeeringVlanPeer(node, &bgp[i], revision, c); err != nil {
				return fmt.Errorf("failed to build peering VLAN peer: %w", err)
			}
		case bgp[i].LoopbackPeer != nil && len(bgp[i].LoopbackPeer.IPAddresses) > 0:
			for i := range bgp[i].LoopbackPeer.IPAddresses {
				bgpPeer, err := buildBgpPeer(&bgp[i].LoopbackPeer.IPAddresses[i], nil, &bgp[i])
				if err != nil {
					return fmt.Errorf("failed to build BGP peer: %w", err)
				}
				c.Spec.ClusterVRF.BGPPeers = append(c.Spec.ClusterVRF.BGPPeers, *bgpPeer)
				c.Spec.ClusterVRF.StaticRoutes = append(c.Spec.ClusterVRF.StaticRoutes, v1alpha1.StaticRoute{
					Prefix: bgp[i].LoopbackPeer.IPAddresses[i],
					NextHop: &v1alpha1.NextHop{
						Address: &hbnHostNextHop,
					},
				})
			}
		default:
			return fmt.Errorf("no loopback IPs found for BGP peer %s", bgp[i].Name)
		}
	}
	return nil
}

func buildNetplanDummies(node *corev1.Node, revision *v1alpha1.NetworkConfigRevision) (map[string]netplan.Device, error) {
	dummies := make(map[string]netplan.Device)
	for _, bgp := range revision.Spec.BGP {
		if !matchSelector(node, bgp.NodeSelector) {
			continue
		}
		h := sha256.New()
		h.Write([]byte(bgp.Name))
		interfaceName := fmt.Sprintf("%x", h.Sum(nil))[:10]

		if bgp.LoopbackPeer != nil && len(bgp.LoopbackPeer.IPAddresses) > 0 {
			addresses := make([]string, len(bgp.LoopbackPeer.IPAddresses))
			for _, ip := range bgp.LoopbackPeer.IPAddresses {
				// convert ip to CIDR format
				ipAddr := net.ParseIP(ip)
				if ipAddr == nil {
					return nil, fmt.Errorf("failed to parse IP address %s", ip)
				}
				if ipAddr.To4() != nil {
					addresses = append(addresses, fmt.Sprintf("%s/32", ip))
				} else {
					addresses = append(addresses, fmt.Sprintf("%s/128", ip))
				}
			}
			dummy := map[string]interface{}{
				"addresses": addresses,
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
