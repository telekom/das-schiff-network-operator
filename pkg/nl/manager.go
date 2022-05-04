package nl

import (
	"fmt"
	"net"
	"sort"
)

var (
	VRF_TABLE_START = 50
	VRF_TABLE_END   = 80

	VRF_PREFIX            = "vr."
	BRIDGE_PREFIX         = "br."
	VXLAN_PREFIX          = "vx."
	VRF_TO_DEFAULT_PREFIX = "vd."
	DEFAULT_TO_VRF_PREFIX = "dv."

	UNDERLAY_LOOPBACK = "dum.underlay"
	NODE_LOOPBACK     = "br.cluster"

	VXLAN_PORT  = 4789
	DEFAULT_MTU = 9000

	MAC_PREFIX = []byte("\x02\x54")
)

type NetlinkManager struct {
}

type VRFInformation struct {
	Name string
	VNI  int

	table    int
	bridgeId int
	vrfId    int
}

// Create will create a VRF and all interfaces neccessary to operate the EVPN and leaking
func (n *NetlinkManager) Create(info *VRFInformation) error {
	if len(info.Name) > 12 {
		return fmt.Errorf("name of VRF can not be longer than 12 (15-3 prefix) chars")
	}
	freeTableID, err := n.findFreeTableID()
	if err != nil {
		return err
	}
	info.table = freeTableID

	err = n.createVRF(info)
	if err != nil {
		return err
	}
	err = n.createBridge(info)
	if err != nil {
		return err
	}
	err = n.createLink(info)
	if err != nil {
		return err
	}
	err = n.createVXLAN(info)
	if err != nil {
		return err
	}

	return nil
}

// Cleanup will try to delete all interfaces associated with this VRF and return a list of errors (for logging) as a slice
func (n *NetlinkManager) Cleanup(name string) []error {
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

func (n *NetlinkManager) List() ([]*VRFInformation, error) {
	return n.list()
}

func (n *NetlinkManager) GetRouterIDForVRFs() (net.IP, error) {
	_, ip, err := getInterfaceAndIP(UNDERLAY_LOOPBACK)
	return ip, err
}

func (n *NetlinkManager) GetHostRouterID() (net.IP, error) {
	_, ip, err := getInterfaceAndIP(NODE_LOOPBACK)
	return ip, err
}

func (n *NetlinkManager) findFreeTableID() (int, error) {
	configuredVRFs, err := n.List()
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
