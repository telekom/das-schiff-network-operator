package frr

import (
	"bytes"
	"io/ioutil"
	"os"
	"strconv"

	"github.com/telekom/das-schiff-network-operator/pkg/nl"
)

var (
	VRF_ASN_CONFIG = 4200065169
)

type FRRTemplateConfig struct {
	VRFs        string
	Neighbors   string
	NeighborsV4 string
	NeighborsV6 string
	BGP         string
	PrefixLists string
	RouteMaps   string

	Hostname         string
	UnderlayRouterID string
	HostRouterID     string
	ECMPMaximumPaths int
}

func (m *FRRManager) Configure(in FRRConfiguration) (bool, error) {
	config, err := m.renderSubtemplates(in)
	if err != nil {
		return false, err
	}

	currentConfig, err := ioutil.ReadFile(m.ConfigPath)
	if err != nil {
		return false, err
	}

	targetConfig, err := render(m.configTemplate, config)
	if err != nil {
		return false, err
	}

	if bytes.Compare(currentConfig, targetConfig) != 0 {
		err = ioutil.WriteFile(m.ConfigPath, targetConfig, FRR_PERMISSIONS)
		if err != nil {
			return false, err
		}

		return true, err
	}
	return false, nil
}

func (f *FRRManager) renderSubtemplates(in FRRConfiguration) (*FRRTemplateConfig, error) {
	vrfRouterId, err := (&nl.NetlinkManager{}).GetRouterIDForVRFs()
	if err != nil {
		return nil, err
	}
	hostRouterId, err := (&nl.NetlinkManager{}).GetHostRouterID()
	if err != nil {
		return nil, err
	}
	hostname, err := os.Hostname()
	if err != nil {
		return nil, err
	}

	vrfs, err := render(VRF_TPL, in.VRFs)
	if err != nil {
		return nil, err
	}
	neighbors, err := render(NEIGHBOR_TPL, in.VRFs)
	if err != nil {
		return nil, err
	}
	neighborsV4, err := render(NEIGHBOR_V4_TPL, in.VRFs)
	if err != nil {
		return nil, err
	}
	neighborsV6, err := render(NEIGHBOR_V6_TPL, in.VRFs)
	if err != nil {
		return nil, err
	}
	prefixlists, err := render(PREFIX_LIST_TPL, in.VRFs)
	if err != nil {
		return nil, err
	}
	routemaps, err := render(ROUTE_MAP_TPL, in.VRFs)
	if err != nil {
		return nil, err
	}
	envECMPMaximumPaths, isSet := os.LookupEnv("NWOP_ECMP_MAXIMUM_PATHS")
	ecmpMaximumPaths := 8 // Default to BCMs preferred target of maximum 8 paths per route.
	if isSet {
		parsedECMPMaximumPaths, err := strconv.Atoi(envECMPMaximumPaths)
		if err != nil {
			ecmpMaximumPaths = parsedECMPMaximumPaths
		} else {
			return nil, err
		}
	}
	asn := in.ASN
	if asn == 0 {
		asn = VRF_ASN_CONFIG
	}
	// Special handling for BGP instance rendering (we need ASN and Router ID)
	bgp, err := render(BGP_INSTANCE_TPL, bgpInstanceConfig{
		VRFs:             in.VRFs,
		RouterID:         vrfRouterId.String(),
		ASN:              asn,
		ECMPMaximumPaths: ecmpMaximumPaths,
	})
	if err != nil {
		return nil, err
	}

	return &FRRTemplateConfig{
		VRFs:             string(vrfs),
		Neighbors:        string(neighbors),
		NeighborsV4:      string(neighborsV4),
		NeighborsV6:      string(neighborsV6),
		BGP:              string(bgp),
		PrefixLists:      string(prefixlists),
		RouteMaps:        string(routemaps),
		UnderlayRouterID: vrfRouterId.String(),
		HostRouterID:     hostRouterId.String(),
		Hostname:         hostname,
	}, nil
}
