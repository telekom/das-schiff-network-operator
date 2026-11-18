package main

import (
	"errors"
	"testing"

	"github.com/vishvananda/netlink"

	"github.com/telekom/das-schiff-network-operator/pkg/nl"
)

type fakeBridgeSyncer struct {
	acceptGARP  map[int]bool
	arpRefresh  []int
	suppression []int
}

func (f *fakeBridgeSyncer) EnsureARPRefresh(id int) { f.arpRefresh = append(f.arpRefresh, id) }

func (f *fakeBridgeSyncer) SetAcceptGratuitousARP(id int, accept bool) { f.acceptGARP[id] = accept }

func (f *fakeBridgeSyncer) EnsureNeighborSuppression(bridgeID, _ int) error {
	f.suppression = append(f.suppression, bridgeID)
	return nil
}

func TestReconcileNeighborSyncSetsGARPPolicyOnEveryBridge(t *testing.T) {
	links := map[string]netlink.Link{
		"l2.100":   &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Index: 10}},
		"vlan.100": &netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Index: 11}},
		"l2.200":   &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Index: 20}},
		"l2.300":   &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Index: 30}},
	}
	linkByName := func(name string) (netlink.Link, error) {
		if l, ok := links[name]; ok {
			return l, nil
		}
		return nil, errors.New("not found")
	}
	reject := false
	cfg := &nl.NetlinkConfiguration{Layer2s: []nl.Layer2Information{
		{VlanID: 100, AnycastGateways: []string{"10.0.0.1/24"}, AcceptGratuitousARP: &reject},
		// Lost its anycast gateways while switching to rejection.
		{VlanID: 200, AcceptGratuitousARP: &reject},
		{VlanID: 300},
	}}

	syncer := &fakeBridgeSyncer{acceptGARP: map[int]bool{}}
	reconcileNeighborSync(cfg, syncer, linkByName)

	want := map[int]bool{10: false, 20: false, 30: true}
	for id, accept := range want {
		if got, ok := syncer.acceptGARP[id]; !ok || got != accept {
			t.Errorf("bridge %d: accept gratuitous ARP = %v (set %v), want %v", id, got, ok, accept)
		}
	}
	if len(syncer.arpRefresh) != 1 || syncer.arpRefresh[0] != 10 || len(syncer.suppression) != 1 || syncer.suppression[0] != 10 {
		t.Errorf("ARP refresh %v, suppression %v: want only the anycast bridge 10", syncer.arpRefresh, syncer.suppression)
	}
}
