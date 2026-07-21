package nl

// MirrorRule describes a traffic mirror rule applied via tc on a source interface.
// Matching traffic is redirected to the GRE interface named by GREInterface (the
// GRE tunnel is created separately from the GRETunnel configuration).
type MirrorRule struct {
	// SourceInterface is the interface to mirror traffic from
	// (e.g. "vlan.<id>" for a Layer2 access port, "vx.<vrf>" for a fabric VRF).
	SourceInterface string `json:"sourceInterface"`
	// Direction is the traffic direction to mirror, from the workload's point of
	// view: "ingress" (traffic to the workload), "egress" (traffic from the
	// workload) or "both".
	Direction string `json:"direction"`
	// WorkloadFacing indicates that SourceInterface faces the workload (the L2
	// `vlan.<id>` access port). The tc hooks are then inverted relative to the
	// workload direction: traffic *to* the workload leaves the bridge via the
	// port's egress hook, and traffic *from* the workload arrives via its ingress
	// hook. A fabric-facing source (the `vx.<vrf>` VXLAN port) leaves this false.
	WorkloadFacing bool `json:"workloadFacing,omitempty"`
	// GREInterface is the name of the GRE interface to mirror matching traffic to.
	GREInterface string `json:"greInterface"`
	// Protocol is the IP protocol to match (e.g. "tcp", "udp", "icmp"), or empty for all.
	Protocol string `json:"protocol,omitempty"`
	// SrcPrefix is the source CIDR to match, or empty for all.
	SrcPrefix string `json:"srcPrefix,omitempty"`
	// DstPrefix is the destination CIDR to match, or empty for all.
	DstPrefix string `json:"dstPrefix,omitempty"`
	// SrcPort is the source port to match, or 0 for all.
	SrcPort uint16 `json:"srcPort,omitempty"`
	// DstPort is the destination port to match, or 0 for all.
	DstPort uint16 `json:"dstPort,omitempty"`
}

// GRETunnel describes a GRE tunnel interface to create inside a VRF via netlink.
type GRETunnel struct {
	// Name is the GRE interface name (e.g. "gre-abc12345").
	Name string `json:"name"`
	// VRF is the VRF the tunnel is enslaved to.
	VRF string `json:"vrf"`
	// Local is the tunnel source IP.
	Local string `json:"local"`
	// Remote is the tunnel destination (collector) IP.
	Remote string `json:"remote"`
	// SourceInterface is the interface that owns the source IP. It is set as the
	// tunnel's link device so the kernel resolves the source address in the
	// correct VRF (l3mdev) domain; without it an IPv6 GRE whose source lives in a
	// VRF is rejected at xmit ("Local address not yet configured").
	SourceInterface string `json:"sourceInterface,omitempty"`
	// Key is the optional GRE encapsulation key.
	Key *uint32 `json:"key,omitempty"`
	// Layer2 selects GRETAP (Ethernet) encapsulation instead of plain GRE.
	Layer2 bool `json:"layer2,omitempty"`
}

// LoopbackConfig describes a loopback (dummy) interface to create in a VRF.
type LoopbackConfig struct {
	// Name is the loopback interface name (e.g. "lo.mir").
	Name string `json:"name"`
	// VRF is the VRF to place the loopback in.
	VRF string `json:"vrf"`
	// Addresses are the IP addresses to assign (CIDR notation).
	Addresses []string `json:"addresses"`
}

// RoutedPort describes a routed CNI attachment whose CRA-side veth was moved
// into this (CRA-FRR) network namespace by the routed CNI. The frr-cra server
// programs its on-link datapath: enslave to the target VRF (or leave in the main
// table for the underlay), bring it up, add the on-link gateway addresses and
// install the workload host routes so FRR redistributes them into BGP.
type RoutedPort struct {
	// Interface is the moved interface name inside the CRA netns.
	Interface string `json:"interface"`
	// VRF is the target VRF device name; empty (or "default"/"main") keeps the
	// port in the main table (the underlay).
	VRF string `json:"vrf,omitempty"`
	// GatewayV4 is the on-link IPv4 gateway address (CIDR, e.g. 169.254.1.1/32).
	GatewayV4 string `json:"gatewayV4,omitempty"`
	// GatewayV6 is the on-link IPv6 gateway address (CIDR, e.g. fe80::1/128).
	GatewayV6 string `json:"gatewayV6,omitempty"`
	// HostRoutes are the workload host addresses (CIDR /32, /128) installed as
	// on-link routes via Interface.
	HostRoutes []string `json:"hostRoutes,omitempty"`
}

type NetlinkConfiguration struct {
	VRFs        []VRFInformation    `json:"vrf"`
	Layer2s     []Layer2Information `json:"layer2"`
	GRETunnels  []GRETunnel         `json:"greTunnels,omitempty"`
	Loopbacks   []LoopbackConfig    `json:"loopbacks,omitempty"`
	Mirrors     []MirrorRule        `json:"mirrors,omitempty"`
	RoutedPorts []RoutedPort        `json:"routedPorts,omitempty"`
}
