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
	BestPath        struct {
		MultiPathRelax string `json:"multiPathRelax"`
	} `json:"bestPath"`
}

// bgpAF == bgpAddressFamily.
type bgpAF int

const (
	IPv4Unicast bgpAF = iota
	IPv4Multicast
	IPv6Unicast
	IPv6Multicast
	L2VpnEvpn
	Unknown
)

var (
	bgpAddressFamily = map[string]bgpAF{
		"ipv4Unicast":   IPv4Unicast,
		"ipv4Multicast": IPv4Multicast,
		"ipv6Unicast":   IPv6Unicast,
		"ipv6Multicast": IPv6Unicast,
		"l2VpnEvpn":     L2VpnEvpn,
		"unknown":       Unknown,
	}
)

func (af bgpAF) Values() (families []bgpAF) {
	for i := 0; i <= int(L2VpnEvpn); i++ {
		families = append(families, bgpAF(i))
	}
	return families

}

func (af bgpAF) String() string {
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

func (af bgpAF) Afi() string {
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

func (af bgpAF) Safi() string {
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

type bgpVrfSummarySpec map[string]AfiAndSafi

type bgpVrfSummary map[string]bgpVrfSummarySpec

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

type NextHop struct {
	Flags          int    `json:"flags"`
	Fib            bool   `json:"fib"`
	IP             string `json:"ip"`
	Afi            string `json:"afi"`
	InterfaceIndex int    `json:"interfaceIndex"`
	InterfaceName  string `json:"interfaceName"`
	Active         bool   `json:"active"`
	OnLink         bool   `json:"onLink"`
	Weight         int    `json:"weight"`
}

type Route struct {
	Prefix                   string    `json:"prefix"`
	PrefixLen                int       `json:"prefixLen"`
	Protocol                 string    `json:"protocol"`
	VrfID                    int       `json:"vrfId"`
	VrfName                  string    `json:"vrfName"`
	Selected                 bool      `json:"selected"`
	DestSelected             bool      `json:"destSelected"`
	Distance                 int       `json:"distance"`
	Metric                   int       `json:"metric"`
	Installed                bool      `json:"installed"`
	Tag                      int       `json:"tag"`
	Table                    int       `json:"table"`
	InternalStatus           int       `json:"internalStatus"`
	InternalFlags            int       `json:"internalFlags"`
	InternalNextHopNum       int       `json:"internalNextHopNum"`
	InternalNextHopActiveNum int       `json:"internalNextHopActiveNum"`
	NexthopGroupID           int       `json:"nexthopGroupId"`
	InstalledNexthopGroupID  int       `json:"installedNexthopGroupId"`
	Uptime                   string    `json:"uptime"`
	Nexthops                 []NextHop `json:"nexthops"`
}

type Routes map[string][]Route

type DualStackRoutes struct {
	IPv4 Routes `json:"ipv4"`
	IPv6 Routes `json:"ipv6"`
}

type VRFDualStackRoutes map[string]DualStackRoutes
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
