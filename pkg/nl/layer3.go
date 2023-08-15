package nl

import (
	"fmt"
	"sort"

	"github.com/telekom/das-schiff-network-operator/pkg/bpf"
)

type VRFInformation struct {
	Name string
	VNI  int

	table    int
	bridgeId int
	vrfId    int

	MarkForDelete bool
}

// Create will create a VRF and all interfaces neccessary to operate the EVPN and leaking
func (n *NetlinkManager) CreateL3(info VRFInformation) error {
	if len(info.Name) > 12 {
		return fmt.Errorf("name of VRF can not be longer than 12 (15-3 prefix) chars")
	}
	freeTableID, err := n.findFreeTableID()
	if err != nil {
		return err
	}
	info.table = freeTableID

	vrf, err := n.createVRF(VRF_PREFIX+info.Name, info.table)
	if err != nil {
		return err
	}

	bridge, err := n.createBridge(BRIDGE_PREFIX+info.Name, nil, vrf.Attrs().Index, DEFAULT_MTU)
	if err != nil {
		return err
	}
	if err = bpf.AttachToInterface(bridge); err != nil {
		return err
	}

	veth, err := n.createLink(VRF_TO_DEFAULT_PREFIX+info.Name, DEFAULT_TO_VRF_PREFIX+info.Name, vrf.Attrs().Index, DEFAULT_MTU, true)
	if err != nil {
		return err
	}
	if err := bpf.AttachToInterface(veth); err != nil {
		return err
	}

	vxlan, err := n.createVXLAN(VXLAN_PREFIX+info.Name, bridge.Attrs().Index, info.VNI, DEFAULT_MTU, true, false)
	if err != nil {
		return err
	}
	if err = bpf.AttachToInterface(vxlan); err != nil {
		return err
	}

	return nil
}

// UpL3 will set all interfaces up. This is done after the FRR reload to not have a L2VNI for a short period of time
func (n *NetlinkManager) UpL3(info VRFInformation) error {
	if err := n.setUp(BRIDGE_PREFIX + info.Name); err != nil {
		return err
	}
	if err := n.setUp(VRF_TO_DEFAULT_PREFIX + info.Name); err != nil {
		return err
	}
	if err := n.setUp(DEFAULT_TO_VRF_PREFIX + info.Name); err != nil {
		return err
	}
	if err := n.setUp(VXLAN_PREFIX + info.Name); err != nil {
		return err
	}
	return nil
}

// Cleanup will try to delete all interfaces associated with this VRF and return a list of errors (for logging) as a slice
func (n *NetlinkManager) CleanupL3(name string) []error {
	errors := []error{}
	err := n.deleteLink(VXLAN_PREFIX + name)
	if err != nil {
		errors = append(errors, err)
	}
	err = n.deleteLink(BRIDGE_PREFIX + name)
	if err != nil {
		errors = append(errors, err)
	}
	err = n.deleteLink(VRF_TO_DEFAULT_PREFIX + name)
	if err != nil {
		errors = append(errors, err)
	}
	err = n.deleteLink(VRF_PREFIX + name)
	if err != nil {
		errors = append(errors, err)
	}
	return errors
}

func (n *NetlinkManager) ListL3() ([]VRFInformation, error) {
	return n.listL3()
}

func (n *NetlinkManager) findFreeTableID() (int, error) {
	configuredVRFs, err := n.ListL3()
	if err != nil {
		return -1, err
	}
	// First sort ascending
	sort.Slice(configuredVRFs, func(i, j int) bool {
		return configuredVRFs[i].table < configuredVRFs[j].table
	})
	// first table should be at VRF_TABLE_START
	freeTableID := VRF_TABLE_START
	// iterate over all configured VRFS
	for _, vrf := range configuredVRFs {
		// if VRF table matches, raised table ID by one. Because we've sorted earlier this allows us to find the first free one
		if vrf.table == freeTableID {
			freeTableID++
		}
	}
	// If a free table id equals or is larger than the VRF_TABLE_END no IDs are available
	if freeTableID >= VRF_TABLE_END {
		return -1, fmt.Errorf("no more free tables available in range %d-%d", VRF_TABLE_START, VRF_TABLE_END)
	}
	return freeTableID, nil
}

func (n *NetlinkManager) GetL3ByName(name string) (*VRFInformation, error) {
	list, err := n.listL3()
	if err != nil {
		return nil, err
	}
	for _, info := range list {
		if info.Name == name {
			return &info, nil
		}
	}
	return nil, fmt.Errorf("no VRF with name %s", name)
}
