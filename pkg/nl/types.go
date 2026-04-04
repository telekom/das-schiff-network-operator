package nl

// MirrorRule describes a traffic mirror rule to be applied via netlink.
// The CRA agent creates a GRE tunnel and tc mirror filters based on this.
type MirrorRule struct {
	// SourceInterface is the interface to mirror traffic from (e.g., "br.100" for L2, VRF device for L3).
	SourceInterface string `json:"sourceInterface"`
	// Direction is the traffic direction to mirror: "ingress", "egress", or "both".
	Direction string `json:"direction"`
	// GRERemote is the collector IP address (GRE tunnel remote endpoint).
	GRERemote string `json:"greRemote"`
	// GRELocal is the local GRE tunnel endpoint IP (loopback in mirror VRF).
	GRELocal string `json:"greLocal"`
	// GREVRF is the VRF in which the GRE tunnel lives (mirror VRF).
	GREVRF string `json:"greVrf"`
	// Protocol is the IP protocol to match (e.g., "tcp", "udp", "icmp"), or empty for all.
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

// LoopbackConfig describes a loopback (dummy) interface to create in a VRF via netlink.
type LoopbackConfig struct {
	// Name is the interface name (e.g., "lo.mirror").
	Name string `json:"name"`
	// VRF is the VRF to place the loopback in.
	VRF string `json:"vrf"`
	// Addresses are the IP addresses to assign (CIDR notation).
	Addresses []string `json:"addresses"`
}

type NetlinkConfiguration struct {
	VRFs      []VRFInformation    `json:"vrf"`
	Layer2s   []Layer2Information `json:"layer2"`
	Mirrors   []MirrorRule        `json:"mirrors,omitempty"`
	Loopbacks []LoopbackConfig    `json:"loopbacks,omitempty"`
}
