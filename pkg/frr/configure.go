package frr

import (
	"bytes"
	"io/ioutil"
	"os"

	"github.com/telekom/das-schiff-network-operator/pkg/nl"
)

var (
	VRF_ASN_CONFIG = 4200065169
)

type FRRTemplateConfig struct {
	VRFs        string
	Neighbors   string
	NeighborsV4 string
	BGP         string
	PrefixLists string
	RouteMaps   string

	Hostname         string
	UnderlayRouterID string
	HostRouterID     string
}

func (m *FRRManager) Configure(in []VRFConfiguration) (bool, error) {
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

func (f *FRRManager) renderSubtemplates(in []VRFConfiguration) (*FRRTemplateConfig, error) {
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

	vrfs, err := render(VRF_TPL, in)
	if err != nil {
		return nil, err
	}
	neighbors, err := render(NEIGHBOR_TPL, in)
	if err != nil {
		return nil, err
	}
	neighborsV4, err := render(NEIGHBOR_V4_TPL, in)
	if err != nil {
		return nil, err
	}
	prefixlists, err := render(PREFIX_LIST_TPL, in)
	if err != nil {
		return nil, err
	}
	routemaps, err := render(ROUTE_MAP_TPL, in)
	if err != nil {
		return nil, err
	}
	// Special handling for BGP instance rendering (we need ASN and Router ID)
	bgp, err := render(BGP_INSTANCE_TPL, bgpInstanceConfig{
		VRFs:     in,
		RouterID: vrfRouterId.String(),
		ASN:      VRF_ASN_CONFIG,
	})
	if err != nil {
		return nil, err
	}

	return &FRRTemplateConfig{
		VRFs:             string(vrfs),
		Neighbors:        string(neighbors),
		NeighborsV4:      string(neighborsV4),
		BGP:              string(bgp),
		PrefixLists:      string(prefixlists),
		RouteMaps:        string(routemaps),
		UnderlayRouterID: vrfRouterId.String(),
		HostRouterID:     hostRouterId.String(),
		Hostname:         hostname,
	}, nil
}
