package bpf

import (
	"fmt"
	"log"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/rlimit"
	"github.com/vishvananda/netlink"
)

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -target amd64 nwopbpf ../../bpf/nwop-bpf.c
//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -target arm64 nwopbpf ../../bpf/nwop-bpf.c

var (
	nwopbpf    nwopbpfObjects
	tcxLinkFds = map[int]*link.Link{}
)

// NeighborEvent mirrors the C struct neighbor_event in nwop-bpf.c.
type NeighborEvent struct {
	Ifindex uint32
	Family  AddressFamily // 4 or 6
	Mac     [6]byte
	IP      [16]byte
}

// AddressFamily represents IPv4 or IPv6.
type AddressFamily uint8

const (
	AddressFamilyIPv4 AddressFamily = 4
	AddressFamilyIPv6 AddressFamily = 6
)

// NeighborEventSize is the byte size of a serialized NeighborEvent (4+1+6+16).
const NeighborEventSize = 27

// InitBPF loads the BPF programs and maps into the kernel.
func InitBPF() error {
	if err := rlimit.RemoveMemlock(); err != nil {
		return fmt.Errorf("error removing memlock: %w", err)
	}
	if err := loadNwopbpfObjects(&nwopbpf, nil); err != nil {
		return fmt.Errorf("error loading BPF objects: %w", err)
	}
	return nil
}

func cleanupXDP(intf netlink.Link) {
	if intf.Attrs().Xdp != nil && intf.Attrs().Xdp.Attached {
		log.Printf("Detaching XDP program from interface %s (index %d) to avoid conflicts with TCX program\n", intf.Attrs().Name, intf.Attrs().Index)
		if err := netlink.LinkSetXdpFd(intf, -1); err != nil {
			log.Printf("Error detaching XDP program from interface %s (index %d): %v\n", intf.Attrs().Name, intf.Attrs().Index, err)
		}
	}
}

// AttachNeighborHandlerToInterface attaches the neighbor reply BPF program via TCX ingress.
func AttachNeighborHandlerToInterface(intf netlink.Link) error {
	cleanupXDP(intf)
	if _, ok := tcxLinkFds[intf.Attrs().Index]; ok {
		log.Printf("BPF: TCX already attached to %s (index %d), skipping", intf.Attrs().Name, intf.Attrs().Index)
		return nil
	}

	log.Printf("BPF: attaching TCX ingress to %s (index %d)...", intf.Attrs().Name, intf.Attrs().Index)
	tcxLink, err := link.AttachTCX(link.TCXOptions{
		Interface: intf.Attrs().Index,
		Program:   nwopbpf.HandleNeighborReplyTc,
		Attach:    ebpf.AttachTCXIngress,
	})
	if err != nil {
		return fmt.Errorf("error attaching TCX program to %s (index %d): %w", intf.Attrs().Name, intf.Attrs().Index, err)
	}
	tcxLinkFds[intf.Attrs().Index] = &tcxLink
	log.Printf("BPF: TCX ingress attached successfully to %s (index %d)", intf.Attrs().Name, intf.Attrs().Index)
	return nil
}

// DetachNeighborHandlerFromInterface removes the neighbor reply BPF program from an interface.
func DetachNeighborHandlerFromInterface(intf netlink.Link) error {
	cleanupXDP(intf)

	tcxLink, ok := tcxLinkFds[intf.Attrs().Index]
	if !ok {
		return nil
	}
	if err := (*tcxLink).Close(); err != nil {
		return fmt.Errorf("error detaching TCX program: %w", err)
	}
	delete(tcxLinkFds, intf.Attrs().Index)
	return nil
}

// CleanupTCX closes all open TCX link file descriptors.
func CleanupTCX() {
	for _, tcxLink := range tcxLinkFds {
		_ = (*tcxLink).Close()
	}
}

// EbpfNeighborRingbuf returns the BPF neighbor event ring buffer map.
func EbpfNeighborRingbuf() *ebpf.Map {
	return nwopbpf.NeighborRingbuf
}
