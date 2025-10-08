package bpf

import (
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/rlimit"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -target amd64 nwopbpf ../../bpf/nwop-bpf.c

const (
	majorNumber            = 0xffff
	minorNumebr            = 0
	interfaceCheckInterval = 5 * time.Second
)

var (
	nwopbpf                 nwopbpfObjects
	trackedInterfaceIndices []int
	qdiscHandle             = netlink.MakeHandle(majorNumber, minorNumebr)
)

// NeighborEvent mirrors the C struct neighbor_event in neighbor-bpf.c.
// Keep the field order and sizes in sync with the C program.
type NeighborEvent struct {
	Ifindex uint32
	Family  AddressFamily // 4 or 6
	Mac     [6]byte
	IP      [16]byte
}

type AddressFamily uint8

const (
	AddressFamilyIPv4 AddressFamily = 4
	AddressFamilyIPv6 AddressFamily = 6
)

const NeighborEventSize = 27 // 4 + 1 + 6 + 16

func InitBPFRouter() error {
	if err := rlimit.RemoveMemlock(); err != nil {
		return fmt.Errorf("error removing memlock: %w", err)
	}
	if err := loadNwopbpfObjects(&nwopbpf, nil); err != nil {
		return err
	}
	return nil
}

func AttachRouterToInterface(intf netlink.Link) error {
	err := attach(intf)
	if err != nil {
		return err
	}
	trackedInterfaceIndices = append(trackedInterfaceIndices, intf.Attrs().Index)
	return nil
}

func AttachRouterInterfaces(intfs []string) error {
	for _, name := range intfs {
		intf, err := netlink.LinkByName(name)
		if err != nil {
			return fmt.Errorf("error getting link %s by name: %w", name, err)
		}
		if err := AttachRouterToInterface(intf); err != nil {
			return err
		}
	}
	return nil
}

func AttachNeighborHandlerToInterface(intf netlink.Link) error {
	// Attach XDP program.
	if intf.Attrs().Xdp != nil && intf.Attrs().Xdp.Attached && intf.Attrs().Xdp.Fd == nwopbpf.HandleNeighborReplyXdp.FD() {
		// Already attached
		return nil
	}
	err := netlink.LinkSetXdpFdWithFlags(intf, nwopbpf.HandleNeighborReplyXdp.FD(), unix.BPF_F_XDP_HAS_FRAGS)
	if err != nil {
		return fmt.Errorf("error attaching XDP program: %w", err)
	}
	return nil
}

func EbpfRetStatsMap() *ebpf.Map {
	return nwopbpf.nwopbpfMaps.EbpfRetStatsMap
}

func EbpfFibLkupStatsMap() *ebpf.Map {
	return nwopbpf.nwopbpfMaps.EbpfFibLkupStatsMap
}

func EbpfNeighborRingbuf() *ebpf.Map {
	return nwopbpf.nwopbpfMaps.NeighborRingbuf
}

// First we ensure the qdisc is there. It is a very basic check, ensuring we have an clsact qdisc with the correct handle
// as no other app should modify the tc options on existing interfaces (other than deleting/adding them altogether) there shouldn't be a risk.
func ensureQdisc(intf netlink.Link) error {
	qdiscs, err := netlink.QdiscList(intf)
	if err != nil {
		return fmt.Errorf("error listing Qdisc for interface %s: %w", intf.Attrs().Name, err)
	}

	for _, qdisc := range qdiscs {
		if qdisc.Type() == "clsact" && qdisc.Attrs().Handle == qdiscHandle {
			return nil
		}
	}
	qdisc := &netlink.GenericQdisc{
		QdiscAttrs: netlink.QdiscAttrs{
			LinkIndex: intf.Attrs().Index,
			Handle:    qdiscHandle,
			Parent:    netlink.HANDLE_CLSACT,
		},
		QdiscType: "clsact",
	}

	if err := netlink.QdiscAdd(qdisc); err != nil {
		return fmt.Errorf("error adding Qdisc: %w", err)
	}

	return nil
}

// Ensure a Filter is set on the clsact qdisc which.
func ensureFilter(intf netlink.Link) error {
	filters, err := netlink.FilterList(intf, netlink.HANDLE_MIN_INGRESS)
	if err != nil {
		return fmt.Errorf("error getting list of filters for interface %s: %w", intf.Attrs().Name, err)
	}
	programInfo, err := nwopbpf.TcRouterFunc.Info()
	if err != nil {
		return fmt.Errorf("error getting program info: %w", err)
	}
	programID, _ := programInfo.ID()
	for _, filter := range filters {
		// We just do a basic check here because the netlink library lacks capabilities for skbmod or pedit actions
		if filter.Type() == "bpf" && filter.(*netlink.BpfFilter).Id == int(programID) {
			return nil
		}
	}
	filter := &netlink.BpfFilter{
		FilterAttrs: netlink.FilterAttrs{
			LinkIndex: intf.Attrs().Index,
			Priority:  1,
			Handle:    netlink.MakeHandle(0, 1),
			Parent:    netlink.HANDLE_MIN_INGRESS,
			Protocol:  unix.ETH_P_ALL,
		},
		DirectAction: true,
		Fd:           nwopbpf.TcRouterFunc.FD(),
		Name:         "tc_router",
	}

	if err := netlink.FilterReplace(filter); err != nil {
		return fmt.Errorf("error replacing filter: %w", err)
	}

	return nil
}

func attach(intf netlink.Link) error {
	ifIndex := intf.Attrs().Index

	if intf.Type() == "vxlan" {
		if err := nwopbpf.LookupPort.Put(int32(ifIndex), int32(intf.Attrs().MasterIndex)); err != nil {
			return fmt.Errorf("error attaching eBPF map element: %w", err)
		}
	}
	if err := ensureQdisc(intf); err != nil {
		return err
	}
	if err := ensureFilter(intf); err != nil {
		return err
	}
	return nil
}

func checkTrackedInterfaces() {
	for i := 0; i < len(trackedInterfaceIndices); i++ {
		idx := trackedInterfaceIndices[i]
		nlLink, err := netlink.LinkByIndex(idx)

		if errors.As(err, &netlink.LinkNotFoundError{}) {
			trackedInterfaceIndices = append(trackedInterfaceIndices[:i], trackedInterfaceIndices[i+1:]...)
			i--
			log.Printf("Link %d no longer found - removing from all BPF tracked tables\n", idx)
			if err := removeFromBPFMap(idx); err != nil {
				log.Printf("Error removing link %d from BPF Maps: %v\n", idx, err)
			}
			continue
		} else if err != nil {
			log.Printf("Error fetching link %d from Netlink: %v\n", idx, err)
			continue
		}

		if err := attach(nlLink); err != nil {
			log.Printf("Link %d: %s error ensuring qdisc and filter on interface", nlLink.Attrs().Index, nlLink.Attrs().Name)
		}
	}
}

func removeFromBPFMap(idx int) error {
	if err := nwopbpf.LookupPort.Delete(int32(idx)); err != nil && !errors.Is(err, ebpf.ErrKeyNotExist) {
		return fmt.Errorf("error deleting eBPF map element: %w", err)
	}
	return nil
}

func RunInterfaceCheck() {
	go func() {
		for {
			checkTrackedInterfaces()
			time.Sleep(interfaceCheckInterval)
		}
	}()
}
