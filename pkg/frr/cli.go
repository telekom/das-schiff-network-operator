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

type Cli struct {
	binaryPath string
}

func NewCli() *Cli {
	return &Cli{
		binaryPath: "/usr/bin/vtysh",
	}
}

// getVRFInfo returns either the provided vrf from the function params - or if it's empty -
// the constant string "all" as the vrf name as well as whether it received an empty input.
func getVRFInfo(vrf string) (name string, isMulti bool) {
	if vrf == "" {
		return "all", true
	}
	return vrf, false
}

func (frr *Cli) executeWithJSON(args []string) []byte {
	// Ensure JSON is always appended
	args = append(args, "json")
	return frr.execute(args)
}

func (frr *Cli) execute(args []string) []byte {
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

func (frr *Cli) ShowEVPNVNIDetail() (EVPNVniDetail, error) {
	evpnInfo := EVPNVniDetail{}
	data := frr.executeWithJSON([]string{
		"show",
		"evpn",
		"vni",
		"detail",
	})
	err := json.Unmarshal(data, &evpnInfo)
	if err != nil {
		return evpnInfo, fmt.Errorf("failed parsing json into struct EVPNVniDetail: %w", err)
	}
	return evpnInfo, nil
}

func (frr *Cli) ShowBGPSummary(vrf string) (BGPVrfSummary, error) {
	vrfName, multiVRF := getVRFInfo(vrf)
	data := frr.executeWithJSON([]string{
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

func (frr *Cli) showVRFVnis() (VrfVni, error) {
	vrfInfo := VrfVni{}
	vrfVniData := frr.executeWithJSON([]string{
		"show",
		"vrf",
		"vni",
	})
	err := json.Unmarshal(vrfVniData, &vrfInfo)
	if err != nil {
		return vrfInfo, fmt.Errorf("failed parsing json into struct VrfVni: %w", err)
	}
	return vrfInfo, nil
}

func parseVRFS(data string) ([]map[string]string, error) {
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
	const chunkSize = 2
	scanner := bufio.NewScanner(strings.NewReader(data))
	var dataAsSlice []map[string]string
	for scanner.Scan() {
		text := scanner.Text()
		text = strings.ReplaceAll(text, " (configured)", "")
		text = strings.ReplaceAll(text, " inactive", "")
		words := strings.Fields(text)
		chunkedWords, err := Chunk(words, chunkSize)
		if err != nil {
			return nil, fmt.Errorf("failed batching of line [%s]: %w", text, err)
		}
		vrfMap := make(map[string]string)
		for _, tuple := range chunkedWords {
			vrfMap[tuple[0]] = tuple[1]
		}
		dataAsSlice = append(dataAsSlice, vrfMap)
	}
	dataAsSlice = append(dataAsSlice, map[string]string{
		"vrf":   "default",
		"table": strconv.Itoa(unix.RT_CLASS_MAIN),
		"id":    "0",
	})
	return dataAsSlice, nil
}

func (frr *Cli) ShowVRFs(vrfName string) (VrfVni, error) {
	vrfInfo, err := frr.showVRFVnis()
	if err != nil {
		return vrfInfo, fmt.Errorf("cannot get vrf vni mapping from frr: %w", err)
	}
	data := frr.execute([]string{
		"show",
		"vrf",
	})
	dataAsString := string(data)
	dataAsSlice, err := parseVRFS(dataAsString)
	if err != nil {
		return vrfInfo, fmt.Errorf("cannot get vrf list information from frr: %w", err)
	}
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
	if vrfName != "" {
		var vrfVniInfo VrfVni
		result, _, ok := Find(vrfInfo.Vrfs, func(element VrfVniSpec) bool {
			return element.Vrf == vrfName
		})
		if !ok {
			return vrfVniInfo, fmt.Errorf("cannot find %s in frr and kernel", vrfName)
		}
		vrfVniInfo = VrfVni{
			Vrfs: []VrfVniSpec{result},
		}
		return vrfVniInfo, nil
	}
	return vrfInfo, nil
}

func (frr *Cli) getDualStackRouteSummaries(vrf string) (routeSummariesV4, routeSummariesV6 RouteSummaries, err error) {
	dataV4 := frr.executeWithJSON([]string{
		"show",
		"ip",
		"route",
		"vrf",
		vrf,
		"summary",
	})
	dataV6 := frr.executeWithJSON([]string{
		"show",
		"ipv6",
		"route",
		"vrf",
		vrf,
		"summary",
	})
	err = json.Unmarshal(dataV4, &routeSummariesV4)
	if err != nil {
		return routeSummariesV4, routeSummariesV6, fmt.Errorf("failed parsing json into struct ipv4 Routes: %w", err)
	}
	err = json.Unmarshal(dataV6, &routeSummariesV6)
	if err != nil {
		return routeSummariesV4, routeSummariesV6, fmt.Errorf("failed parsing json into struct ipv6 Routes: %w", err)
	}
	return routeSummariesV4, routeSummariesV6, nil
}

func (frr *Cli) ShowRouteSummary(vrf string) (VRFDualStackRouteSummary, error) {
	vrfName, multiVrf := getVRFInfo(vrf)
	vrfRoutes := VRFDualStackRouteSummary{}
	if multiVrf {
		// as the opensource project has issues with correctly representing
		// json in some Cli commands
		// we need this ugly loop to get the necessary parseable data mapping.
		vrfVni, err := frr.ShowVRFs("")
		if err != nil {
			return nil, fmt.Errorf("cannot get vrfs from frr: %w", err)
		}
		for _, vrf := range vrfVni.Vrfs {
			routeSummariesV4, routesSummariesV6, err := frr.getDualStackRouteSummaries(vrf.Vrf)
			if err != nil {
				return nil, fmt.Errorf("cannot get DualStackRoutes for vrf %s: %w", vrf.Vrf, err)
			}
			vrfRoutes[vrf.Vrf] = DualStackRouteSummary{IPv4: routeSummariesV4, IPv6: routesSummariesV6, Table: vrf.Table}
		}
	} else {
		vrfVni, err := frr.ShowVRFs(vrfName)
		if err != nil {
			return nil, fmt.Errorf("cannot get vrf from frr: %w", err)
		}
		routeSummariesV4, routesSummariesV6, err := frr.getDualStackRouteSummaries(vrfName)
		if err != nil {
			return nil, fmt.Errorf("cannot get DualStackRoutes for vrf %s: %w", vrfName, err)
		}
		vrfRoutes[vrfName] = DualStackRouteSummary{IPv4: routeSummariesV4, IPv6: routesSummariesV6, Table: vrfVni.Vrfs[0].Table}
	}
	return vrfRoutes, nil
}
