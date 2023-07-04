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

func NewFRRCLI() (*FRRCLI, error) {
	return &FRRCLI{
		binaryPath: "/usr/bin/vtysh",
	}, nil
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
	if multiVRF {
		json.Unmarshal(data, &bgpSummary)
	} else {
		json.Unmarshal(data, &bgpSummarySpec)
		bgpSummary[vrfName] = bgpSummarySpec
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
	json.Unmarshal(data, &vrfInfo)
	return vrfInfo, nil
}

func (frr *FRRCLI) ShowIPRoute(vrf string) (VrfRoutes, error) {
	vrfName, multiVrf := getVRF(vrf)
	vrfRoute := VrfRoutes{}
	if multiVrf {
		// as the opensource project has issues with correctly representing
		// json in some cli commands
		// we need this ugly loop to get the necessary parseable data mapping.
		vrfs, err := frr.ShowVRFs()
		if err != nil {
			return nil, err
		}
		for _, vrf := range vrfs.VrfVni {
			routes := Routes{}
			data := frr.execute([]string{
				"show",
				"ip",
				"route",
				vrf.Vrf,
			})
			json.Unmarshal(data, &routes)
			vrfRoute[vrfName] = routes

		}

	} else {
		routes := Routes{}
		data := frr.execute([]string{
			"show",
			"ip",
			"route",
			vrfName,
		})
		json.Unmarshal(data, &routes)
		vrfRoute[vrfName] = routes
	}
	return vrfRoute, nil
}
