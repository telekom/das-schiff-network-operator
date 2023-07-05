package frr

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type FRRCLI struct {
	binaryPath string
}

func NewFRRCLI() *FRRCLI {
	return &FRRCLI{
		binaryPath: "/usr/bin/vtysh",
	}
}

// getVRF returns either the provided vrf from the function params or if its empty.
// the constant string "all" as the vrf name.
// if it returns it also provides feedback if the vrf parameter was empty or not as
// the second return value.
func getVRF(vrf string) (string, bool) {
	if vrf == "" {
		return "all", true
	}
	return vrf, false
}

func (frr *FRRCLI) execute(args []string) []byte {
	// Ensure JSON is always appended
	args = append(args, "json")
	joinedArgs := strings.Join(args[:], " ")
	cmd := &exec.Cmd{
		Path: frr.binaryPath,
		Args: append([]string{"-c"}, joinedArgs),
	}
	output, err := cmd.Output()
	if err != nil {
		panic(fmt.Sprintf("Could not run the command, %s %s, output: %s", frr.binaryPath, strings.Join(cmd.Args, " "), cmd.Stderr))
	}
	return output
}

func (frr *FRRCLI) ShowEVPNVNIDetail() (EVPNVniDetail, error) {
	evpnInfo := EVPNVniDetail{}
	data := frr.execute([]string{
		"show",
		"evpn",
		"vni",
		"detail",
	})
	err := json.Unmarshal(data, &evpnInfo)
	return evpnInfo, err
}

func (frr *FRRCLI) ShowBGPSummary(vrf string) (BGPVrfSummary, error) {
	vrfName, multiVRF := getVRF(vrf)
	data := frr.execute([]string{
		"show",
		"bgp",
		"vrf",
		vrfName,
		"summary",
	})
	bgpSummary := BGPVrfSummary{}
	bgpSummarySpec := BGPVrfSummarySpec{}
	var err error
	if multiVRF {
		err = json.Unmarshal(data, &bgpSummary)

	} else {
		err = json.Unmarshal(data, &bgpSummarySpec)
		bgpSummary[vrfName] = bgpSummarySpec
	}
	if err != nil {
		return nil, err
	}
	return bgpSummary, nil

}

func (frr *FRRCLI) ShowVRFs() (VrfVni, error) {
	vrfInfo := VrfVni{}
	data := frr.execute([]string{
		"show",
		"vrf",
		"vni",
	})
	err := json.Unmarshal(data, &vrfInfo)
	if err != nil {
		return vrfInfo, err
	}
	return vrfInfo, nil
}

func (frr *FRRCLI) getDualStackRoutes(vrf string) (Routes, Routes, error) {
	routes_v4 := Routes{}
	routes_v6 := Routes{}
	data_v4 := frr.execute([]string{
		"show",
		"ip",
		"route",
		vrf,
	})
	data_v6 := frr.execute([]string{
		"show",
		"ipv6",
		"route",
		vrf,
	})
	var err error
	err = json.Unmarshal(data_v4, &routes_v4)
	if err != nil {
		return nil, nil, err
	}
	err = json.Unmarshal(data_v6, &routes_v6)
	if err != nil {
		return nil, nil, err
	}
	return routes_v4, routes_v6, nil
}

func (frr *FRRCLI) ShowRoutes(vrf string) (VrfDualStackRoutes, error) {
	vrfName, multiVrf := getVRF(vrf)
	vrfRoutes := VrfDualStackRoutes{}
	if multiVrf {
		// as the opensource project has issues with correctly representing
		// json in some cli commands
		// we need this ugly loop to get the necessary parseable data mapping.
		vrfVni, err := frr.ShowVRFs()
		if err != nil {
			return nil, err
		}
		for _, vrf := range vrfVni.Vrfs {
			routes_v4, routes_v6, err := frr.getDualStackRoutes(vrf.Vrf)
			if err != nil {
				return nil, err
			}
			vrfRoutes[vrf.Vrf] = DualStackRoutes{IPv4: routes_v4, IPv6: routes_v6}
		}

	} else {
		routes_v4, routes_v6, err := frr.getDualStackRoutes(vrfName)
		if err != nil {
			return nil, err
		}
		vrfRoutes[vrfName] = DualStackRoutes{IPv4: routes_v4, IPv6: routes_v6}
	}
	return vrfRoutes, nil
}
