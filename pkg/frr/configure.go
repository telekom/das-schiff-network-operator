package frr

import (
	"bytes"
	"fmt"
	"os"
	"regexp"

	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
)

var (
	vrfAsnConfig = 4200065169

	// Regular expressions for parsing route-target lines.
	rtLinesRe = regexp.MustCompile(`(?m)^\s*route-target.*`)
	rtPartsRe = regexp.MustCompile(`(?m)^(\s*route-target\s*(?:import|export)\s*)(.*)`)
	rtRe      = regexp.MustCompile(`(?m)(\S+)`)

	tagInOriginal = []byte(`route-map TAG-FABRIC-IN permit 10
  set local-preference 90
  set tag 20000
exit`)
	tagInReplacement = []byte(`route-map TAG-FABRIC-IN permit 10
  set local-preference 90
  set community 65169:200 additive
exit`)
	denyOutOriginal = []byte(`route-map DENY-TAG-FABRIC-OUT deny 10
  match tag 20000
exit`)
	denyOutReplacement = []byte(`bgp community-list standard cm-received-fabric permit 65169:200
route-map DENY-TAG-FABRIC-OUT deny 10
  match community cm-received-fabric
exit`)
)

type templateConfig struct {
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
}

func (m *Manager) Configure(in Configuration, nm *nl.Manager) (bool, error) {
	// Remove permit from VRF and only allow deny rules for mgmt VRFs
	for i := range in.VRFs {
		if in.VRFs[i].Name != m.mgmtVrf {
			continue
		}
		for j := range in.VRFs[i].Import {
			for k := range in.VRFs[i].Import[j].Items {
				if in.VRFs[i].Import[j].Items[k].Action != "deny" {
					return false, fmt.Errorf("only deny rules are allowed in import prefix-lists of mgmt VRFs")
				}
				// Swap deny to permit, this will be a prefix-list called from a deny route-map
				in.VRFs[i].Import[j].Items[k].Action = "permit"
			}
		}
	}

	config, err := m.renderSubtemplates(in, nm)
	if err != nil {
		return false, err
	}

	currentConfig, err := os.ReadFile(m.ConfigPath)
	if err != nil {
		return false, fmt.Errorf("error reading configuration file: %w", err)
	}

	targetConfig, err := render(m.configTemplate, config)
	if err != nil {
		return false, err
	}

	targetConfig = fixRouteTargetReload(targetConfig)
	targetConfig = fixTagFabric(targetConfig)

	if !bytes.Equal(currentConfig, targetConfig) {
		err = os.WriteFile(m.ConfigPath, targetConfig, frrPermissions)
		if err != nil {
			return false, fmt.Errorf("error writing configuration file: %w", err)
		}

		return true, nil
	}
	return false, nil
}

func (m *Manager) renderRouteMapMgmtIn() ([]byte, error) {
	return render(routeMapMgmtInTpl, mgmtImportConfig{
		IPv4MgmtRouteMapIn: m.ipv4MgmtRouteMapIn,
		IPv6MgmtRouteMapIn: m.ipv6MgmtRouteMapIn,
		MgmtVrfName:        m.mgmtVrf,
	})
}

func (m *Manager) renderSubtemplates(in Configuration, nlManager *nl.Manager) (*templateConfig, error) {
	vrfRouterID, err := nlManager.GetUnderlayIP()
	if err != nil {
		return nil, fmt.Errorf("error getting underlay IP: %w", err)
	}

	hostname := os.Getenv(healthcheck.NodenameEnv)
	if hostname == "" {
		return nil, fmt.Errorf("error getting node's name")
	}

	vrfs, err := render(vrfTpl, in.VRFs)
	if err != nil {
		return nil, err
	}
	neighbors, err := render(neighborTpl, in.VRFs)
	if err != nil {
		return nil, err
	}
	neighborsV4, err := render(neighborV4Tpl, in.VRFs)
	if err != nil {
		return nil, err
	}
	neighborsV6, err := render(neighborV6Tpl, in.VRFs)
	if err != nil {
		return nil, err
	}
	prefixlists, err := render(prefixListTpl, in.VRFs)
	if err != nil {
		return nil, err
	}
	routemaps, err := render(routeMapTpl, in.VRFs)
	if err != nil {
		return nil, err
	}
	routemapMgmtIn, err := m.renderRouteMapMgmtIn()
	if err != nil {
		return nil, err
	}
	asn := in.ASN
	if asn == 0 {
		asn = vrfAsnConfig
	}
	// Special handling for BGP instance rendering (we need ASN and Router ID)
	bgp, err := render(bgpInstanceTpl, bgpInstanceConfig{
		VRFs:     in.VRFs,
		RouterID: vrfRouterID.String(),
		ASN:      asn,
	})
	if err != nil {
		return nil, err
	}

	return &templateConfig{
		VRFs:             string(vrfs),
		Neighbors:        string(neighbors),
		NeighborsV4:      string(neighborsV4),
		NeighborsV6:      string(neighborsV6),
		BGP:              string(bgp),
		PrefixLists:      string(prefixlists),
		RouteMaps:        string(routemaps) + "\n" + string(routemapMgmtIn),
		UnderlayRouterID: vrfRouterID.String(),
		Hostname:         hostname,
	}, nil
}

// fixRouteTargetReload is a workaround for FRR's inability to reload route-targets if they are configured in a single line.
// This function splits such lines into multiple lines, each containing a single route-target.
func fixRouteTargetReload(config []byte) []byte {
	return rtLinesRe.ReplaceAllFunc(config, func(s []byte) []byte {
		parts := rtPartsRe.FindSubmatch(s)
		if parts == nil {
			return s
		}
		rtLine, targets := string(parts[1]), string(parts[2])
		routeTargets := rtRe.FindAllString(targets, -1)
		if len(routeTargets) <= 1 {
			return s
		}
		lines := ""
		for _, rt := range routeTargets {
			lines += rtLine + rt + "\n"
		}
		return []byte(lines[:len(lines)-1])
	})
}

func fixTagFabric(config []byte) []byte {
	config = bytes.ReplaceAll(config, tagInOriginal, tagInReplacement)
	config = bytes.ReplaceAll(config, denyOutOriginal, denyOutReplacement)
	return config
}
