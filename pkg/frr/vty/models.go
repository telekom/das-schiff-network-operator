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
