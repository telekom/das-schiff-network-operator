package nl

type NetlinkConfiguration struct {
	VRFs    []VRFInformation    `json:"vrf"`
	Layer2s []Layer2Information `json:"layer2"`
}
