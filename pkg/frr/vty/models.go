package vty

type Base struct {
	FrrVrfLib *FrrVrfLib `json:"frr-vrf:lib,omitempty"`
}

type FrrVrfLib struct {
	VRFs []VRF `json:"vrf,omitempty"`
}

type VRF struct {
	Name           string          `json:"name,omitempty"`
	FrrVrfLibZebra *FrrVrfLibZebra `json:"frr-zebra:zebra,omitempty"`
}

type FrrVrfLibZebra struct {
	L3VNI int32 `json:"l3vni-id,omitempty"`
}

type EvpnType string

const (
	EvpnTypeL2 EvpnType = "L2"
	EvpnTypeL3 EvpnType = "L3"
)

type ShowEvpnVni struct {
	VNI       int32    `json:"vni,omitempty"`
	TenantVRF string   `json:"tenantVrf,omitempty"`
	Type      EvpnType `json:"type,omitempty"`
}
