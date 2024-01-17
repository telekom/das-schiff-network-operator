package frr

type Peer struct {
	Hostname                   string `json:"hostname"`
	RemoteAs                   int64  `json:"remoteAs"`
	LocalAs                    int64  `json:"localAs"`
	Version                    int    `json:"version"`
	MsgRcvd                    int    `json:"msgRcvd"`
	MsgSent                    int    `json:"msgSent"`
	TableVersion               int    `json:"tableVersion"`
	Outq                       int    `json:"outq"`
	Inq                        int    `json:"inq"`
	PeerUptime                 string `json:"peerUptime"`
	PeerUptimeMsec             int    `json:"peerUptimeMsec"`
	PeerUptimeEstablishedEpoch int    `json:"peerUptimeEstablishedEpoch"`
	PfxRcd                     int    `json:"pfxRcd"`
	PfxSnt                     int    `json:"pfxSnt"`
	State                      string `json:"state"`
	PeerState                  string `json:"peerState"`
	ConnectionsEstablished     int    `json:"connectionsEstablished"`
	ConnectionsDropped         int    `json:"connectionsDropped"`
	IDType                     string `json:"idType"`
}
type bestPath struct {
	MultiPathRelax string `json:"multiPathRelax"`
}
type AfiAndSafi struct {
	RouterID        string          `json:"routerId"`
	As              int64           `json:"as"`
	VrfID           int             `json:"vrfId"`
	VrfName         string          `json:"vrfName"`
	TableVersion    int             `json:"tableVersion"`
	RibCount        int             `json:"ribCount"`
	RibMemory       int             `json:"ribMemory"`
	PeerCount       int             `json:"peerCount"`
	PeerMemory      int             `json:"peerMemory"`
	PeerGroupCount  int             `json:"peerGroupCount"`
	PeerGroupMemory int             `json:"peerGroupMemory"`
	Peers           map[string]Peer `json:"peers"`
	FailedPeers     int             `json:"failedPeers"`
	DisplayedPeers  int             `json:"displayedPeers"`
	TotalPeers      int             `json:"totalPeers"`
	DynamicPeers    int             `json:"dynamicPeers"`
	BestPath        bestPath        `json:"bestPath"`
}

// bgpAF == bgpAddressFamily.
type BGPAddressFamily int

const (
	IPv4Unicast BGPAddressFamily = iota
	IPv4Multicast
	IPv6Unicast
	IPv6Multicast
	L2VpnEvpn
	Unknown
)

func BGPAddressFamilyValues() (families []BGPAddressFamily) {
	for i := 0; i <= int(L2VpnEvpn); i++ {
		families = append(families, BGPAddressFamily(i))
	}
	return families
}

func (af BGPAddressFamily) String() string {
	switch af {
	case IPv4Unicast:
		return "ipv4Unicast"
	case IPv4Multicast:
		return "ipv4Multicast"
	case IPv6Unicast:
		return "ipv6Unicast"
	case IPv6Multicast:
		return "ipv6Multicast"
	case L2VpnEvpn:
		return "l2VpnEvpn"
	}
	return frrUnknown
}

func (af BGPAddressFamily) Afi() string {
	// Address Family Indicator (AFI)
	switch af {
	case IPv4Unicast, IPv4Multicast:
		return "ipv4"
	case IPv6Unicast, IPv6Multicast:
		return "ipv6"
	case L2VpnEvpn:
		return "l2vpn"
	}
	return frrUnknown
}

func (af BGPAddressFamily) Safi() string {
	// Subsequent Address Family Indicator (SAFI)
	switch af {
	case IPv4Unicast, IPv6Unicast:
		return "unicast"
	case IPv4Multicast, IPv6Multicast:
		return "multicast"
	case L2VpnEvpn:
		return "evpn"
	}
	return frrUnknown
}

type BGPVrfSummarySpec map[string]AfiAndSafi

type BGPVrfSummary map[string]BGPVrfSummarySpec

type EVPNVniDetail struct {
	Vni                   int      `json:"vni"`
	Type                  string   `json:"type"`
	Vrf                   string   `json:"vrf"`
	VxlanInterface        string   `json:"vxlanInterface"`
	Ifindex               int      `json:"ifindex"`
	SviInterface          string   `json:"sviInterface"`
	SviIfindex            int      `json:"sviIfindex"`
	VtepIP                string   `json:"vtepIp"`
	McastGroup            string   `json:"mcastGroup"`
	AdvertiseGatewayMacip string   `json:"advertiseGatewayMacip"`
	AdvertiseSviMacip     string   `json:"advertiseSviMacip"`
	NumMacs               int      `json:"numMacs"`
	NumArpNd              int      `json:"numArpNd"`
	NumRemoteVteps        []string `json:"numRemoteVteps"`
}
type RouteSummary struct {
	Fib          int    `json:"fib"`
	Rib          int    `json:"rib"`
	FibOffLoaded int    `json:"fibOffLoaded"`
	FibTrapped   int    `json:"fibTrapped"`
	Type         string `json:"type"`
}

type RouteSummaries struct {
	Routes         []RouteSummary `json:"routes"`
	RoutesTotal    int            `json:"routesTotal"`
	RoutesTotalFib int            `json:"routesTotalFib"`
}

type DualStackRouteSummary struct {
	IPv4  RouteSummaries `json:"ipv4"`
	IPv6  RouteSummaries `json:"ipv6"`
	Table string         `json:"table,omitempty"`
}

type VRFDualStackRouteSummary map[string]DualStackRouteSummary
type VrfVniSpec struct {
	Vrf       string `json:"vrf"`
	Vni       int    `json:"vni"`
	VxlanIntf string `json:"vxlanIntf"`
	SviIntf   string `json:"sviIntf"`
	State     string `json:"state"`
	RouterMac string `json:"routerMac"`
	Table     string `json:"table,omitempty"`
}

type VrfVni struct {
	Vrfs []VrfVniSpec `json:"vrfs"`
}
