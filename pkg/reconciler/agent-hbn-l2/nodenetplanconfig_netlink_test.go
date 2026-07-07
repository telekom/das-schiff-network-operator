//go:build linux

package agent_hbn_l2

import (
	"encoding/json"
	"os"
	"runtime"
	"testing"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"

	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
)

// vlanDevice builds a minimal netplan VLAN device with the given id.
func vlanDevice(t *testing.T, id int) netplan.Device {
	t.Helper()
	raw, err := json.Marshal(map[string]interface{}{"id": id, "mtu": 1500})
	if err != nil {
		t.Fatalf("marshalling vlan device: %v", err)
	}
	return netplan.Device{Raw: raw}
}

// withHBNNetns runs fn inside a fresh network namespace that already contains an
// "hbn" master interface, so the netlink-driven VLAN reconciliation can be
// exercised hermetically. It skips when not root (netlink/netns need CAP_NET_ADMIN).
func withHBNNetns(t *testing.T, fn func(hbn netlink.Link)) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("requires root for netlink/netns operations")
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	orig, err := netns.Get()
	if err != nil {
		t.Fatalf("getting current netns: %v", err)
	}
	defer orig.Close()

	ns, err := netns.New() // creates and switches into a fresh namespace
	if err != nil {
		t.Fatalf("creating netns: %v", err)
	}
	defer func() {
		_ = netns.Set(orig)
		_ = ns.Close()
	}()

	hbn := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: hbnMasterName}}
	if err := netlink.LinkAdd(hbn); err != nil {
		t.Fatalf("creating hbn master: %v", err)
	}
	if err := netlink.LinkSetUp(hbn); err != nil {
		t.Fatalf("bringing hbn up: %v", err)
	}
	master, err := netlink.LinkByName(hbnMasterName)
	if err != nil {
		t.Fatalf("fetching hbn master: %v", err)
	}
	fn(master)
}

func assertVLAN(t *testing.T, name string, wantID int, wantAlias string) {
	t.Helper()
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatalf("expected interface %q to exist: %v", name, err)
	}
	vlan, ok := link.(*netlink.Vlan)
	if !ok {
		t.Fatalf("interface %q is %s, want vlan", name, link.Type())
	}
	if vlan.VlanId != wantID {
		t.Errorf("interface %q vlan id = %d, want %d", name, vlan.VlanId, wantID)
	}
	if got := link.Attrs().Alias; got != wantAlias {
		t.Errorf("interface %q alias = %q, want %q", name, got, wantAlias)
	}
}

func assertNoInterface(t *testing.T, name string) {
	t.Helper()
	links, err := netlink.LinkList()
	if err != nil {
		t.Fatalf("listing links: %v", err)
	}
	for _, link := range links {
		if link.Attrs().Name == name {
			t.Errorf("unexpected interface %q still present", name)
		}
	}
}

// TestReconcileVLANsHonorsInterfaceName verifies that the kernel interface is
// named after the netplan map key (spec.interfaceName), not vlan.<id>.
func TestReconcileVLANsHonorsInterfaceName(t *testing.T) {
	withHBNNetns(t, func(_ netlink.Link) {
		devices := map[string]netplan.Device{"mac-be": vlanDevice(t, 100)}
		if err := reconcileVLANs(devices); err != nil {
			t.Fatalf("reconcileVLANs: %v", err)
		}
		assertVLAN(t, "mac-be", 100, "hbn::vlan::mac-be")
		assertNoInterface(t, "vlan.100")
	})
}

// TestReconcileVLANsDefaultName verifies that the default "vlan.<id>" key still
// yields a vlan.<id> interface (backward compatibility).
func TestReconcileVLANsDefaultName(t *testing.T) {
	withHBNNetns(t, func(_ netlink.Link) {
		devices := map[string]netplan.Device{"vlan.100": vlanDevice(t, 100)}
		if err := reconcileVLANs(devices); err != nil {
			t.Fatalf("reconcileVLANs: %v", err)
		}
		assertVLAN(t, "vlan.100", 100, "hbn::vlan::vlan.100")
	})
}

// TestReconcileVLANsRenamesStaleInterface verifies convergence: an interface
// created before interfaceName was honored (named vlan.<id> but carrying the
// desired-name alias) is renamed to the desired name.
func TestReconcileVLANsRenamesStaleInterface(t *testing.T) {
	withHBNNetns(t, func(master netlink.Link) {
		stale := &netlink.Vlan{
			LinkAttrs: netlink.LinkAttrs{Name: "vlan.100", ParentIndex: master.Attrs().Index},
			VlanId:    100,
		}
		if err := netlink.LinkAdd(stale); err != nil {
			t.Fatalf("creating stale vlan: %v", err)
		}
		if err := netlink.LinkSetAlias(stale, "hbn::vlan::mac-be"); err != nil {
			t.Fatalf("setting stale alias: %v", err)
		}
		if err := netlink.LinkSetUp(stale); err != nil {
			t.Fatalf("bringing stale vlan up: %v", err)
		}

		devices := map[string]netplan.Device{"mac-be": vlanDevice(t, 100)}
		if err := reconcileVLANs(devices); err != nil {
			t.Fatalf("reconcileVLANs: %v", err)
		}

		assertVLAN(t, "mac-be", 100, "hbn::vlan::mac-be")
		assertNoInterface(t, "vlan.100")
	})
}
