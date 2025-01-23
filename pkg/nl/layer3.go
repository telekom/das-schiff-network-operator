package nl

import (
	"fmt"
	"sort"

	"github.com/telekom/das-schiff-network-operator/pkg/bpf"
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

	MarkForDelete bool
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

	vrf, err := n.createVRF(vrfPrefix+info.Name, info.table)
	if err != nil {
		return err
	}

	bridge, err := n.createBridge(bridgePrefix+info.Name, nil, vrf.Attrs().Index, defaultMtu, true, false)
	if err != nil {
		return err
	}
	if err := bpf.AttachToInterface(bridge); err != nil {
		return fmt.Errorf("error attaching BPF: %w", err)
	}

	veth, err := n.createLink(vrfToDefaultPrefix+info.Name, defaultToVrfPrefix+info.Name, vrf.Attrs().Index, info.linkMTU(), true)
	if err != nil {
		return err
	}
	if err := bpf.AttachToInterface(veth); err != nil {
		return fmt.Errorf("error attaching BPF: %w", err)
	}

	vxlan, err := n.createVXLAN(vxlanPrefix+info.Name, bridge.Attrs().Index, info.VNI, defaultMtu, true, false)
	if err != nil {
		return err
	}
	if err := bpf.AttachToInterface(vxlan); err != nil {
		return fmt.Errorf("error attaching BPF: %w", err)
	}

	return nil
}

// UpL3 will set all interfaces up. This is done after the FRR reload to not have a L2VNI for a short period of time.
func (n *Manager) UpL3(info VRFInformation) error {
	if err := n.setUp(bridgePrefix + info.Name); err != nil {
		return err
	}
	if err := n.setUp(vrfToDefaultPrefix + info.Name); err != nil {
		return err
	}
	if err := n.setUp(defaultToVrfPrefix + info.Name); err != nil {
		return err
	}
	if err := n.setUp(vxlanPrefix + info.Name); err != nil {
		return err
	}
	return nil
}

// Cleanup will try to delete all interfaces associated with this VRF and return a list of errors (for logging) as a slice.
func (n *Manager) CleanupL3(name string) []error {
	errors := []error{}
	err := n.deleteLink(vxlanPrefix + name)
	if err != nil {
		errors = append(errors, err)
	}
	err = n.deleteLink(bridgePrefix + name)
	if err != nil {
		errors = append(errors, err)
	}
	err = n.deleteLink(vrfToDefaultPrefix + name)
	if err != nil {
		errors = append(errors, err)
	}
	err = n.deleteLink(vrfPrefix + name)
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

func (n *Manager) GetL3ByName(name string) (*VRFInformation, error) {
	list, err := n.ListL3()
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

func (n *Manager) EnsureBPFProgram(info VRFInformation) error {
	if link, err := n.toolkit.LinkByName(bridgePrefix + info.Name); err != nil {
		return fmt.Errorf("error getting bridge interface of vrf %s: %w", info.Name, err)
	} else if err := bpf.AttachToInterface(link); err != nil {
		return fmt.Errorf("error attaching bpf program to bridge interface of vrf %s: %w", info.Name, err)
	}

	if link, err := n.toolkit.LinkByName(vrfToDefaultPrefix + info.Name); err != nil {
		return fmt.Errorf("error getting vrf2default interface of vrf %s: %w", info.Name, err)
	} else if err := bpf.AttachToInterface(link); err != nil {
		return fmt.Errorf("error attaching bpf program to vrf2default interface of vrf %s: %w", info.Name, err)
	}

	if link, err := n.toolkit.LinkByName(vxlanPrefix + info.Name); err != nil {
		return fmt.Errorf("error getting vxlan interface of vrf %s: %w", info.Name, err)
	} else if err := bpf.AttachToInterface(link); err != nil {
		return fmt.Errorf("error attaching bpf program to vxlan interface of vrf %s: %w", info.Name, err)
	}

	return nil
}

func (info VRFInformation) linkMTU() int {
	if info.MTU == 0 {
		return defaultMtu
	}
	return info.MTU
}

func (n *Manager) EnsureMTU(info VRFInformation) error {
	link, err := n.toolkit.LinkByName(vrfToDefaultPrefix + info.Name)
	if err != nil {
		return fmt.Errorf("error getting vrf2default interface of vrf %s: %w", info.Name, err)
	}
	if link.Attrs().MTU != info.linkMTU() {
		if err := n.toolkit.LinkSetMTU(link, info.MTU); err != nil {
			return fmt.Errorf("error setting MTU of vrf2default interface of vrf %s: %w", info.Name, err)
		}
	}

	link, err = n.toolkit.LinkByName(defaultToVrfPrefix + info.Name)
	if err != nil {
		return fmt.Errorf("error getting default2vrf interface of vrf %s: %w", info.Name, err)
	}
	if link.Attrs().MTU != info.linkMTU() {
		if err := n.toolkit.LinkSetMTU(link, info.linkMTU()); err != nil {
			return fmt.Errorf("error setting MTU of default2vrw interface of vrf %s: %w", info.Name, err)
		}
	}
	return nil
}
