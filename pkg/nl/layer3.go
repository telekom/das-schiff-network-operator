package nl

import (
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/vishvananda/netlink"
)

const (
	// maxVRFnameLen is the maximum length of a VRF name. Bridge/VXLAN interfaces
	// are named by VNI (not by VRF name), so the VRF device name itself is the
	// only remaining constraint: the Linux interface-name limit (IFNAMSIZ-1).
	maxVRFnameLen = 15
)

type VRFInformation struct {
	Name string
	VNI  int
	MTU  int

	table    int
	bridgeID int
	vrfID    int

	MarkForDelete bool
	LocalOnly     bool
}

type Loopback struct {
	Name      string
	Addresses []*net.IPNet
}

// Create will create a VRF and all interfaces necessary to operate the EVPN and leaking.
func (n *Manager) CreateL3(info VRFInformation) error {
	if len(info.Name) > maxVRFnameLen {
		return fmt.Errorf("name of VRF can not be longer than %d chars", maxVRFnameLen)
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

	if info.LocalOnly {
		return nil
	}

	bridge, err := n.createBridge(l3BridgeName(info.VNI), nil, vrf.Attrs().Index, DefaultMtu, true, false)
	if err != nil {
		return err
	}

	if _, err := n.createVXLAN(l3VXLANName(info.VNI), bridge.Attrs().Index, info.VNI, DefaultMtu, true, false); err != nil {
		return err
	}

	return nil
}

// l3BridgeName returns the bridge interface name for an L3VNI VRF. The bridge is
// named by VNI (not by VRF name) so that the VRF name itself is not constrained
// by the Linux interface-name length limit.
func l3BridgeName(vni int) string {
	return fmt.Sprintf("%s%d", bridgePrefix, vni)
}

// l3VXLANName returns the VXLAN interface name for an L3VNI VRF. See l3BridgeName
// for the naming rationale.
func l3VXLANName(vni int) string {
	return fmt.Sprintf("%s%d", vxlanPrefix, vni)
}

// UpL3 will set all interfaces up. This is done after the FRR reload to not have a L2VNI for a short period of time.
func (n *Manager) UpL3(info VRFInformation) error {
	if info.LocalOnly {
		return nil
	}
	if err := n.setUp(l3BridgeName(info.VNI)); err != nil {
		return err
	}
	if err := n.setUp(l3VXLANName(info.VNI)); err != nil {
		return err
	}
	return nil
}

// Cleanup will try to delete all interfaces associated with this VRF and return a list of errors (for logging) as a slice.
func (n *Manager) CleanupL3(info VRFInformation) []error {
	if n.baseConfig.ClusterVRF.Name == info.Name || n.baseConfig.ManagementVRF.Name == info.Name {
		return []error{fmt.Errorf("can not delete cluster or management VRF %s", info.Name)}
	}

	errors := []error{}
	// Delete every bridge/VXLAN enslaved to this VRF by walking the link list
	// rather than reconstructing names. This removes the current VNI-named
	// interfaces (br.<vni>/vx.<vni>) and also any legacy name-based interfaces
	// (br.<vrf>/vx.<vrf>) left behind by an older agent, which would otherwise
	// be orphaned when the VRF device is deleted.
	if info.vrfID != 0 {
		links, err := n.toolkit.LinkList()
		if err != nil {
			errors = append(errors, fmt.Errorf("error listing links: %w", err))
		} else {
			errors = append(errors, n.deleteL3Children(info.vrfID, links)...)
		}
	}
	if err := n.deleteLink(info.Name); err != nil {
		errors = append(errors, err)
	}
	return errors
}

// deleteL3Children deletes the L3VNI bridge(s) enslaved to the given VRF device
// and the VXLAN(s) enslaved to those bridges, regardless of their naming scheme.
func (n *Manager) deleteL3Children(vrfID int, links []netlink.Link) []error {
	errs := []error{}
	bridges := map[int]string{}
	for _, link := range links {
		if link.Type() == linkTypeBridge &&
			strings.HasPrefix(link.Attrs().Name, bridgePrefix) &&
			link.Attrs().MasterIndex == vrfID {
			bridges[link.Attrs().Index] = link.Attrs().Name
		}
	}
	// Delete VXLANs enslaved to those bridges first, then the bridges.
	for _, link := range links {
		if link.Type() != linkTypeVXLAN || !strings.HasPrefix(link.Attrs().Name, vxlanPrefix) {
			continue
		}
		if _, ok := bridges[link.Attrs().MasterIndex]; ok {
			if err := n.deleteLink(link.Attrs().Name); err != nil {
				errs = append(errs, err)
			}
		}
	}
	for _, name := range bridges {
		if err := n.deleteLink(name); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
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
