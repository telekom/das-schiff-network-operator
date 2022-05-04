package bpf

import (
	"errors"
	"log"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/rlimit"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -target amd64 router ../../bpf/router.c

var (
	router                  routerObjects
	trackedInterfaceIndices []int
	qdiscHandle             = netlink.MakeHandle(0xffff, 0)
)

func InitBPFRouter() {
	if err := rlimit.RemoveMemlock(); err != nil {
		log.Fatal(err)
	}
	if err := loadRouterObjects(&router, nil); err != nil {
		log.Fatal(err)
	}
	initMonitoring()
}

func AttachToInterface(intf netlink.Link) error {
	err := attach(intf)
	if err != nil {
		return err
	}
	trackedInterfaceIndices = append(trackedInterfaceIndices, intf.Attrs().Index)
	return nil
}

func AttachInterfaces(intfs []string) error {
	for _, name := range intfs {
		intf, err := netlink.LinkByName(name)
		if err != nil {
			return err
		}
		if err := AttachToInterface(intf); err != nil {
			return err
		}
	}
	return nil
}

// First we ensure the qdisc is there. It is a very basic check, ensuring we have an clsact qdisc with the correct handle
// As no other app should modify the tc options on existing interfaces (other than deleting/adding them alltogether) there shouldn't be a risk
func ensureQdisc(intf netlink.Link) error {
	qdiscs, err := netlink.QdiscList(intf)
	if err != nil {
		return err
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
	return netlink.QdiscAdd(qdisc)
}

// Ensure a Filter is set on the clsact qdisc which
func ensureFilter(intf netlink.Link) error {
	filters, err := netlink.FilterList(intf, netlink.HANDLE_MIN_INGRESS)
	if err != nil {
		return err
	}
	programInfo, err := router.TcRouterFunc.Info()
	if err != nil {
		return err
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
		Fd:           router.TcRouterFunc.FD(),
		Name:         "tc_router",
	}
	return netlink.FilterReplace(filter)
}

func attach(intf netlink.Link) error {
	ifIndex := intf.Attrs().Index

	if intf.Type() == "vxlan" {
		if err := router.LookupPort.Put(int32(ifIndex), int32(intf.Attrs().MasterIndex)); err != nil {
			return err
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
		link, err := netlink.LinkByIndex(idx)

		if _, ok := err.(netlink.LinkNotFoundError); ok {
			trackedInterfaceIndices = append(trackedInterfaceIndices[:i], trackedInterfaceIndices[i+1:]...)
			i--
			log.Printf("Link %d no longer found - removing from all BPF tracked tables\n", idx)
			if err := removeFromBPFMap(idx); err != nil {
				log.Printf("Error removing link %d from BPF Maps: %v\n", idx, err)
			}
			continue
		} else {
			if err != nil {
				log.Printf("Error fetching link %d from Netlink: %v\n", idx, err)
				continue
			}
		}

		if err := attach(link); err != nil {
			log.Printf("Link %d: %s error ensuring qdisc and filter on interface", link.Attrs().Index, link.Attrs().Name)
		}
	}
}

func removeFromBPFMap(idx int) error {
	if err := router.LookupPort.Delete(int32(idx)); err != nil && !errors.Is(err, ebpf.ErrKeyNotExist) {
		return err
	}
	return nil
}

func RunInterfaceCheck() {
	go func() {
		for {
			checkTrackedInterfaces()
			time.Sleep(5 * time.Second)
		}
	}()
}
