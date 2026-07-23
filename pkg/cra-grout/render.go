package cra

import (
	"fmt"
	"sort"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
)

// bridgeName derives the grout L2 bridge domain name for a Layer2 VNI. Kept
// short to stay within grout's interface-name limits.
func bridgeName(vni uint32) string {
	return fmt.Sprintf("br%d", vni)
}

// RenderGrcli renders the desired grout fast-path state for a node as a grcli
// batch. It creates the cluster/fabric/local VRFs, the EVPN L3VNI/L2VNI VXLAN
// interfaces, the L2 bridge domains with their attached ports (both the shared
// trunk VLAN sub-interface and any routed-CNI access ports), and the routed
// workload ports (net_tap for the veth transport, net_vhost for vhostuser) with
// their on-link gateway addresses and host routes.
func RenderGrcli(baseConfig *config.BaseConfig, spec *v1alpha1.NodeNetworkConfigSpec) (string, error) {
	b := NewBatch()
	vtep := baseConfig.VTEPLoopbackIP

	clusterVRFName := baseConfig.ClusterVRF.Name

	// VRFs first: ports, bridges and VXLANs reference them.
	b.Commentf("VRFs")
	if spec.ClusterVRF != nil && clusterVRFName != "" {
		b.AddVRF(clusterVRFName)
	}
	for _, name := range sortedFabricVRFNames(spec.FabricVRFs) {
		if name == baseConfig.ManagementVRF.Name {
			continue
		}
		b.AddVRF(name)
	}
	for _, name := range sortedVRFNames(spec.LocalVRFs) {
		b.AddVRF(name)
	}

	// EVPN L3VNI VXLAN interfaces for fabric VRFs carrying a VNI.
	for _, name := range sortedFabricVRFNames(spec.FabricVRFs) {
		if name == baseConfig.ManagementVRF.Name {
			continue
		}
		vrf := spec.FabricVRFs[name]
		if vrf.VNI != 0 {
			b.Commentf("L3VNI for VRF %s", name)
			b.AddL3VNI(vrf.VNI, vtep, name)
		}
	}

	// EVPN L2VNI bridges + VXLANs, and their L2-attached routed-CNI ports.
	if err := renderLayer2s(b, spec, vtep, baseConfig.TrunkInterfaceName); err != nil {
		return "", err
	}

	// Routed workload ports per VRF.
	if err := renderRoutedPorts(b, spec, clusterVRFName); err != nil {
		return "", err
	}

	return b.String(), nil
}

func renderLayer2s(b *Batch, spec *v1alpha1.NodeNetworkConfigSpec, vtep, trunk string) error {
	for _, key := range sortedLayer2Keys(spec.Layer2s) {
		l2 := spec.Layer2s[key]
		br := bridgeName(l2.VNI)
		irbVRF := ""
		if l2.IRB != nil {
			irbVRF = l2.IRB.VRF
		}
		b.Commentf("L2VNI %d (bridge %s)", l2.VNI, br)
		b.AddL2Bridge(br, irbVRF)
		// Attach the L2VNI VXLAN to the bridge domain before addressing the IRB
		// SVI, so the domain is established when the anycast-gateway address is
		// added. The IRB gateway IP lives on the L2VNI bridge SVI (bound to the
		// tenant VRF via AddL2Bridge) -- the L3VNI is a pure L3 transit VNI and
		// carries no address.
		b.AddL2VNI(l2.VNI, vtep, br)
		if l2.IRB != nil {
			for _, gw := range l2.IRB.IPAddresses {
				b.AddAddress(gw, br)
			}
		}

		// Map the L2's VLAN on the shared fabric trunk into this bridge domain,
		// so workloads attached via macvlan on the host-side trunk netdev (which
		// tag with l2.VLAN) are bridged into the L2VNI. Only emitted when the
		// node has a trunk configured and the L2 carries a VLAN; the trunk port
		// itself stays in VRF mode so grout performs VLAN demux into this
		// sub-interface. Access ports attached directly via the routed CNI
		// (AttachedPorts, below) are an independent path onto the same bridge.
		if trunk != "" && l2.VLAN != 0 {
			b.AddTrunkVlanToBridge(trunk, l2.VLAN, br)
		}

		for i := range l2.AttachedPorts {
			ap := &l2.AttachedPorts[i]
			if err := renderAttachedPort(b, ap, br); err != nil {
				return fmt.Errorf("L2VNI %d attached port %q: %w", l2.VNI, ap.Interface, err)
			}
		}
	}
	return nil
}

func renderAttachedPort(b *Batch, ap *v1alpha1.AttachedPort, bridge string) error {
	switch ap.Transport {
	case v1alpha1.PortTransportVhostUser:
		if ap.SocketPath == "" {
			return fmt.Errorf("vhostuser transport requires a socket path")
		}
		b.AddVhostPortToBridge(ap.Interface, ap.SocketPath, groutIsClient(ap.SocketMode), bridge)
	case v1alpha1.PortTransportVeth, "":
		b.AddTapPortToBridge(ap.Interface, ap.Interface, bridge)
	default:
		return fmt.Errorf("unsupported transport %q", ap.Transport)
	}
	return nil
}

func renderRoutedPorts(b *Batch, spec *v1alpha1.NodeNetworkConfigSpec, clusterVRFName string) error {
	if spec.ClusterVRF != nil {
		if err := renderVRFRoutedPorts(b, spec.ClusterVRF.RoutedPorts, clusterVRFName); err != nil {
			return err
		}
	}
	for _, name := range sortedFabricVRFNames(spec.FabricVRFs) {
		vrf := spec.FabricVRFs[name]
		if err := renderVRFRoutedPorts(b, vrf.RoutedPorts, name); err != nil {
			return err
		}
	}
	for _, name := range sortedVRFNames(spec.LocalVRFs) {
		vrf := spec.LocalVRFs[name]
		if err := renderVRFRoutedPorts(b, vrf.RoutedPorts, name); err != nil {
			return err
		}
	}
	return nil
}

func renderVRFRoutedPorts(b *Batch, ports []v1alpha1.RoutedPort, vrf string) error {
	for i := range ports {
		p := &ports[i]
		b.Commentf("routed port %s (vrf %s)", p.Interface, vrf)
		switch p.Transport {
		case v1alpha1.PortTransportVhostUser:
			if p.SocketPath == "" {
				return fmt.Errorf("routed port %q: vhostuser transport requires a socket path", p.Interface)
			}
			b.AddVhostPort(p.Interface, p.SocketPath, groutIsClient(p.SocketMode), vrf)
		case v1alpha1.PortTransportVeth, "":
			b.AddTapPort(p.Interface, p.Interface, vrf)
		default:
			return fmt.Errorf("routed port %q: unsupported transport %q", p.Interface, p.Transport)
		}

		if p.GatewayV4 != "" {
			b.AddAddress(p.GatewayV4, p.Interface)
		}
		if p.GatewayV6 != "" {
			b.AddAddress(p.GatewayV6, p.Interface)
		}
		for _, hr := range p.HostRoutes {
			if _, err := b.AddOnLinkHostRoute(hr, p.Interface, vrf); err != nil {
				return fmt.Errorf("routed port %q host route: %w", p.Interface, err)
			}
		}
	}
	return nil
}

// groutIsClient maps the workload-perspective vhost-user socket mode onto
// grout's net_vhost client flag. The two ends of a vhost-user socket must take
// opposite roles (like VSR's invertSocketMode): when the workload owns the
// socket ("server"), grout must connect as the client (client=1); when the
// workload connects ("client") or the mode is unset, grout owns the socket
// (server, client=0). grout net_vhost client=1 => grout connects to an existing
// socket; client=0 => grout creates and listens on it.
func groutIsClient(socketMode string) bool {
	return socketMode == "server"
}

func sortedLayer2Keys(m map[string]v1alpha1.Layer2) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedFabricVRFNames(m map[string]v1alpha1.FabricVRF) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedVRFNames(m map[string]v1alpha1.VRF) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
