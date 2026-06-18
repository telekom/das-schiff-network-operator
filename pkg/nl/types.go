package nl

// MirrorRule describes a traffic mirror rule applied via tc on a source interface.
// Matching traffic is redirected to the GRE interface named by GREInterface (the
// GRE tunnel is created separately from the GRETunnel configuration).
type MirrorRule struct {
	// SourceInterface is the interface to mirror traffic from
	// (e.g. "l2.<vlan>" for a Layer2 bridge, "vx.<vrf>" for a fabric VRF).
	SourceInterface string `json:"sourceInterface"`
	// Direction is the traffic direction to mirror: "ingress", "egress" or "both".
	Direction string `json:"direction"`
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

type NetlinkConfiguration struct {
	VRFs       []VRFInformation    `json:"vrf"`
	Layer2s    []Layer2Information `json:"layer2"`
	GRETunnels []GRETunnel         `json:"greTunnels,omitempty"`
	Loopbacks  []LoopbackConfig    `json:"loopbacks,omitempty"`
	Mirrors    []MirrorRule        `json:"mirrors,omitempty"`
}
