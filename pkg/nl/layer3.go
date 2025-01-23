package nl

import (
	"fmt"
	"net"
	"sort"
)

const (
	maxVRFnameLen = 12
)

type VRFInformation struct {
	Name string
	VNI  int
	MTU  int

	table    int
	bridgeID int
	vrfID    int

	markForDelete bool
}

type Loopback struct {
	Name      string
	Addresses []*net.IPNet
}

// Create will create a VRF and all interfaces necessary to operate the EVPN and leaking.
func (n *Manager) CreateL3(info VRFInformation) error {
	if len(info.Name) > maxVRFnameLen {
		return fmt.Errorf("name of VRF can not be longer than 12 (15-3 prefix) chars")
	}
	freeTableID, err := n.findFreeTableID()
	if err != nil {
		return err
	}
	info.table = freeTableID

	vrf, err := n.createVRF(info.Name, info.table)
	if err != nil {
		return err
	}

	bridge, err := n.createBridge(bridgePrefix+info.Name, nil, vrf.Attrs().Index, defaultMtu, true, false)
	if err != nil {
		return err
	}

	if _, err := n.createVXLAN(vxlanPrefix+info.Name, bridge.Attrs().Index, info.VNI, defaultMtu, true, false); err != nil {
		return err
	}

	return nil
}

// UpL3 will set all interfaces up. This is done after the FRR reload to not have a L2VNI for a short period of time.
func (n *Manager) UpL3(info VRFInformation) error {
	if err := n.setUp(bridgePrefix + info.Name); err != nil {
		return err
	}
	if err := n.setUp(vxlanPrefix + info.Name); err != nil {
		return err
	}
	return nil
}

// Cleanup will try to delete all interfaces associated with this VRF and return a list of errors (for logging) as a slice.
func (n *Manager) CleanupL3(name string) []error {
	if n.baseConfig.ClusterVRF.Name == name || n.baseConfig.ManagementVRF.Name == name {
		return []error{fmt.Errorf("can not delete cluster or management VRF %s", name)}
	}

	errors := []error{}
	err := n.deleteLink(vxlanPrefix + name)
	if err != nil {
		errors = append(errors, err)
	}
	err = n.deleteLink(bridgePrefix + name)
	if err != nil {
		errors = append(errors, err)
	}
	err = n.deleteLink(name)
	if err != nil {
		errors = append(errors, err)
	}
	return errors
}

func (n *Manager) findFreeTableID() (int, error) {
	configuredVRFs, err := n.ListL3()
	if err != nil {
		return -1, err
	}
	// First sort ascending
	sort.Slice(configuredVRFs, func(i, j int) bool {
		return configuredVRFs[i].table < configuredVRFs[j].table
	})
	// first table should be at VRF_TABLE_START
	freeTableID := vrfTableStart
	// iterate over all configured VRFS
	for _, vrf := range configuredVRFs {
		// if VRF table matches, raised table ID by one. Because we've sorted earlier this allows us to find the first free one
		if vrf.table == freeTableID {
			freeTableID++
		}
	}
	// If a free table id equals or is larger than the VRF_TABLE_END no IDs are available
	if freeTableID >= vrfTableEnd {
		return -1, fmt.Errorf("no more free tables available in range %d-%d", vrfTableStart, vrfTableEnd)
	}
	return freeTableID, nil
}

func (n *Manager) GetVRFInterfaceIdxByName(name string) (int, error) {
	if n.baseConfig.ClusterVRF.Name == name || n.baseConfig.ManagementVRF.Name == name {
		intf, err := n.toolkit.LinkByName(name)
		if err != nil {
			return -1, fmt.Errorf("cluster or mangement VRF %s not found", name)
		}
		return intf.Attrs().Index, nil
	}

	list, err := n.ListL3()
	if err != nil {
		return -1, err
	}
	for _, info := range list {
		if info.Name == name {
			return info.vrfID, nil
		}
	}
	return -1, fmt.Errorf("no VRF with name %s", name)
}

func (info VRFInformation) linkMTU() int {
	if info.MTU == 0 {
		return defaultMtu
	}
	return info.MTU
}
