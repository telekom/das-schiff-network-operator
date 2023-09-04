package frr

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
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

func (frr *FRRCLI) executeWithJson(args []string) []byte {
	args = append(args, "json")
	return frr.execute(args)
}

func (frr *FRRCLI) execute(args []string) []byte {
	// Ensure JSON is always appended

	joinedArgs := strings.Join(args, " ")
	cmd := &exec.Cmd{
		Path: frr.binaryPath,
		Args: append([]string{frr.binaryPath, "-c"}, joinedArgs), // it is weird to set path and Args[0] ^^
	}
	output, err := cmd.Output()
	if err != nil {
		panic(fmt.Sprintf("Could not run the command, %s %s, output: %s", frr.binaryPath, strings.Join(cmd.Args, " "), cmd.Stderr))
	}
	return output
}

func (frr *FRRCLI) ShowEVPNVNIDetail() (EVPNVniDetail, error) {
	evpnInfo := EVPNVniDetail{}
	data := frr.executeWithJson([]string{
		"show",
		"evpn",
		"vni",
		"detail",
	})
	err := json.Unmarshal(data, &evpnInfo)
	if err != nil {
		return evpnInfo, fmt.Errorf("failed parsing json into struct EVPNVniDetail: %w", err)
	}
	return evpnInfo, err
}

func (frr *FRRCLI) ShowBGPSummary(vrf string) (BGPVrfSummary, error) {
	vrfName, multiVRF := getVRF(vrf)
	data := frr.executeWithJson([]string{
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
		if err != nil {
			return nil, fmt.Errorf("failed parsing json into struct bgpSummary: %w", err)
		}
	} else {
		err = json.Unmarshal(data, &bgpSummarySpec)
		if err != nil {
			return nil, fmt.Errorf("failed parsing json into struct bgpSummarySpec: %w", err)
		}
		bgpSummary[vrfName] = bgpSummarySpec
	}
	return bgpSummary, nil

}

func (frr *FRRCLI) showVRFVnis() (VrfVni, error) {
	vrfInfo := VrfVni{}
	vrfVniData := frr.executeWithJson([]string{
		"show",
		"vrf",
		"vni",
	})
	err := json.Unmarshal(vrfVniData, &vrfInfo)
	if err != nil {
		return vrfInfo, fmt.Errorf("failed parsing json into struct VrfVni: %w", err)
	}
	return vrfInfo, err
}
func (frr *FRRCLI) ShowVRFs() (VrfVni, error) {
	vrfInfo, err := frr.showVRFVnis()
	if err != nil {
		return vrfInfo, fmt.Errorf("cannot get vrf vni mapping from frr: %w", err)
	}
	// now we want all vrfs which do not have
	// a vni assigned
	// this code is ugly as it needs to parse the following output
	//
	// vrf Vrf_coil id 4 table 119
	// vrf Vrf_kubevip id 6 table 198
	// vrf Vrf_nwop id 5 table 130
	// vrf Vrf_om_m2m inactive (configured) #> this one is ignored as it has no table
	// vrf Vrf_om_refm2m id 3 table 3
	// vrf Vrf_underlay id 2 table 2

	data := frr.execute([]string{
		"show",
		"vrf",
	})
	dataAsString := string(data)
	scanner := bufio.NewScanner(strings.NewReader(dataAsString))
	var dataAsSlice []map[string]string
	for scanner.Scan() {
		text := scanner.Text()
		text = strings.ReplaceAll(text, " (configured)", "")
		text = strings.ReplaceAll(text, " inactive", "")
		words := strings.Fields(text)
		chunkedWords := Chunk(words, 2)
		vrfMap := make(map[string]string)
		for _, tuple := range chunkedWords {
			vrfMap[tuple[0]] = tuple[1]
		}
		dataAsSlice = append(dataAsSlice, vrfMap)
	}
	dataAsSlice = append(dataAsSlice, map[string]string{
		frrVRF:  "default",
		"table": strconv.Itoa(unix.RT_CLASS_MAIN),
		"id":    "0",
	})
	for _, vrf := range dataAsSlice {
		table, ok := vrf["table"]
		// If the key exists
		if ok {
			vrfName, ok := vrf[frrVRF]
			if !ok {
				return vrfInfo, nil
			}
			result, index, ok := Find(vrfInfo.Vrfs, func(element VrfVniSpec) bool {
				return element.Vrf == vrfName
			})
			if !ok {
				// as we do not have a VRF with EVPN Type 5 Uplink
				// we just add vrfname and table in spec
				vrfInfo.Vrfs = append(vrfInfo.Vrfs, VrfVniSpec{
					Vrf:       vrfName,
					Vni:       0,
					VxlanIntf: "",
					RouterMac: "",
					SviIntf:   "",
					State:     "",
					Table:     table,
				})
			} else {
				result.Table = table
				// remove the wrong
				vrfInfo.Vrfs = DeleteByIndex(vrfInfo.Vrfs, index)
				vrfInfo.Vrfs = append(vrfInfo.Vrfs, result)
			}

		}
	}
	return vrfInfo, nil
}

func (frr *FRRCLI) getDualStackRoutes(vrf string) (Routes, Routes, error) {
	routesV4 := Routes{}
	routesV6 := Routes{}
	data_v4 := frr.executeWithJson([]string{
		"show",
		"ip",
		"route",
		"vrf",
		vrf,
	})
	data_v6 := frr.executeWithJson([]string{
		"show",
		"ipv6",
		"route",
		"vrf",
		vrf,
	})
	var err error
	err = json.Unmarshal(data_v4, &routesV4)
	if err != nil {
		return nil, nil, fmt.Errorf("failed parsing json into struct ipv4 Routes: %w", err)
	}
	err = json.Unmarshal(data_v6, &routesV6)
	if err != nil {
		return nil, nil, fmt.Errorf("failed parsing json into struct ipv6 Routes: %w", err)
	}
	return routesV4, routesV6, nil
}

func (frr *FRRCLI) ShowRoutes(vrf string) (VRFDualStackRoutes, error) {
	vrfName, multiVrf := getVRF(vrf)
	vrfRoutes := VRFDualStackRoutes{}
	if multiVrf {
		// as the opensource project has issues with correctly representing
		// json in some cli commands
		// we need this ugly loop to get the necessary parseable data mapping.
		vrfVni, err := frr.ShowVRFs()
		if err != nil {
			return nil, fmt.Errorf("cannot get vrfs from frr: %w", err)
		}
		for _, vrf := range vrfVni.Vrfs {
			routesV4, routesV6, err := frr.getDualStackRoutes(vrf.Vrf)
			if err != nil {
				return nil, fmt.Errorf("cannot get DualStackRoutes for vrf %s: %w", vrf.Vrf, err)
			}
			vrfRoutes[vrf.Vrf] = DualStackRoutes{IPv4: routesV4, IPv6: routesV6}
		}

	} else {
		routesV4, routesV6, err := frr.getDualStackRoutes(vrfName)
		if err != nil {
			return nil, fmt.Errorf("cannot get DualStackRoutes for vrf %s: %w", vrfName, err)
		}
		vrfRoutes[vrfName] = DualStackRoutes{IPv4: routesV4, IPv6: routesV6}
	}
	return vrfRoutes, nil
}
