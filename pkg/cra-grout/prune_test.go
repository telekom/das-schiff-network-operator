package cra

import (
	"strings"
	"testing"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
)

func TestDesiredInterfaceNames(t *testing.T) {
	batch := strings.Join([]string{
		"# VRFs",
		"interface add vrf cluster",
		"interface add bridge br2000 vrf tenant-a",
		"interface add vxlan l2vni2000 vni 2000 local 10.50.0.10 domain br2000",
		"interface add port cra0123 devargs net_tap_cra0123,iface=cra0123_dp mtu 1500 vrf cluster",
		"interface add port v0abcde devargs net_vhost_v0abcde,iface=/run/s.sock,client=0 mtu 1500 domain br2000",
		// A trunk port and one of its tagged members.
		"interface add port cra0777 devargs net_tap_cra0777,iface=cra0777_dp mtu 9000",
		"interface add vlan cra0777.501 parent cra0777 vlan_id 501 mtu 9000 domain br2000",
		"address add 169.254.1.1/32 iface cra0123",
	}, "\n")

	got := DesiredInterfaceNames(batch)
	want := map[string]bool{"cra0123": true, "v0abcde": true, "cra0777": true, "cra0777.501": true}
	if len(got) != len(want) {
		t.Fatalf("DesiredInterfaceNames() = %v, want %v", got, want)
	}
	for name := range want {
		if !got[name] {
			t.Errorf("DesiredInterfaceNames() missing %q; got %v", name, got)
		}
	}
}

func TestStaleInterfacesPrunesOnlyRemovedWorkloadPorts(t *testing.T) {
	live := []Interface{
		{Name: "cra012345", Type: "port"}, // still wanted
		{Name: "cra999999", Type: "port"}, // pod deleted -> prune
		{Name: "v0abcde", Type: "port"},   // pod deleted -> prune
		// Everything below must survive: the trunk is the node's uplink, and
		// the bridges/vxlans are not ports at all.
		{Name: "hbn", Type: "port"},
		{Name: "br2000", Type: "bridge"},
		{Name: "l2vni2000", Type: "vxlan"},
		{Name: "hbn.501", Type: "vlan"},
		// A port whose name is not the CNI's scheme is not ours to delete.
		{Name: "eth0", Type: "port"},
		{Name: "craXYZ12", Type: "port"},
	}

	got := StaleInterfaces(map[string]bool{"cra012345": true}, live)
	want := []string{"cra999999", "v0abcde"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("StaleInterfaces() = %v, want %v", got, want)
	}
}

// TestStaleInterfacesDeletesTrunkMembersBeforeTheirPort pins the ordering grout
// forces on us: iface_destroy refuses an interface that still has
// sub-interfaces (EBUSY) and does not cascade. Deleting the port first fails,
// and EBUSY is not "already gone", so the reconcile would error out on every
// pass and the deleted pod's port would stay live for good.
func TestStaleInterfacesDeletesTrunkMembersBeforeTheirPort(t *testing.T) {
	live := []Interface{
		{Name: "cra0777", Type: "port"},
		{Name: "cra0777.501", Type: "vlan"},
		{Name: "cra0777.502", Type: "vlan"},
	}

	got := StaleInterfaces(map[string]bool{}, live)
	want := []string{"cra0777.501", "cra0777.502", "cra0777"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("StaleInterfaces() = %v, want %v", got, want)
	}
}

// TestStaleInterfacesPrunesOneTrunkMember covers a pod dropping a single VLAN
// from a trunk it keeps: only that sub-interface goes, and the port -- still
// carrying the other member -- stays.
func TestStaleInterfacesPrunesOneTrunkMember(t *testing.T) {
	live := []Interface{
		{Name: "cra0777", Type: "port"},
		{Name: "cra0777.501", Type: "vlan"},
		{Name: "cra0777.502", Type: "vlan"},
	}

	desired := map[string]bool{"cra0777": true, "cra0777.501": true}
	got := StaleInterfaces(desired, live)
	if strings.Join(got, ",") != "cra0777.502" {
		t.Errorf("StaleInterfaces() = %v, want [cra0777.502]", got)
	}
}

// TestStaleInterfacesNeverPrunesFabricTrunkVLANs is the sub-interface half of
// the trunk guard: the fabric trunk's own VLAN sub-interfaces are created by the
// CRA's network setup and, like the trunk port, never appear in a rendered
// batch.
func TestStaleInterfacesNeverPrunesFabricTrunkVLANs(t *testing.T) {
	for _, name := range []string{"hbn.501", "eth0.100", "uplink.4094"} {
		got := StaleInterfaces(map[string]bool{}, []Interface{{Name: name, Type: "vlan"}})
		if len(got) != 0 {
			t.Errorf("StaleInterfaces() would delete infrastructure vlan %q", name)
		}
	}
}

// TestStaleInterfacesNeverPrunesTheTrunk is the assertion with real
// consequences: the fabric trunk is created by the CRA's own setup and never
// appears in a rendered batch, so a prune that judged staleness by absence alone
// would delete the node's uplink on the first reconcile.
func TestStaleInterfacesNeverPrunesTheTrunk(t *testing.T) {
	for _, name := range []string{"hbn", "eth0", "uplink", "vrf-cluster"} {
		got := StaleInterfaces(map[string]bool{}, []Interface{{Name: name, Type: "port"}})
		if len(got) != 0 {
			t.Errorf("StaleInterfaces() would delete infrastructure port %q", name)
		}
	}
}

func TestParseInterfaces(t *testing.T) {
	out := []byte(`[
	  {"name": "cra0123", "id": 2, "type": "port", "mode": "vrf", "domain": "cluster"},
	  {"name": "br2000", "id": 3, "type": "bridge"}
	]`)
	ifaces, err := ParseInterfaces(out)
	if err != nil {
		t.Fatalf("ParseInterfaces: %v", err)
	}
	if len(ifaces) != 2 || ifaces[0].Name != "cra0123" || ifaces[0].Type != "port" {
		t.Errorf("ParseInterfaces() = %+v", ifaces)
	}
}

func TestParseInterfacesEmptyOutput(t *testing.T) {
	// grcli prints nothing when there are no interfaces; that is not an error,
	// and it must not be reported as one or every reconcile on a fresh node
	// would fail.
	ifaces, err := ParseInterfaces([]byte("  \n"))
	if err != nil {
		t.Fatalf("ParseInterfaces: %v", err)
	}
	if len(ifaces) != 0 {
		t.Errorf("ParseInterfaces() = %+v, want empty", ifaces)
	}
}

func TestParseInterfacesRejectsGarbage(t *testing.T) {
	// A parse failure must not read as "no interfaces are live", which the
	// prune would take as licence to delete nothing -- or, if it ever grew the
	// opposite default, everything.
	if _, err := ParseInterfaces([]byte("command failed: Connection refused")); err == nil {
		t.Error("ParseInterfaces() accepted non-JSON output")
	}
}

// TestWorkloadPortNameMatchesTheCNINamingScheme ties the ownership pattern to
// the names the workload CNI actually derives. The two live in different
// packages, and if they drift apart nothing breaks loudly: the prune just stops
// recognising its own ports and silently resumes leaking every deleted pod's
// port. These are real portName()/vhostPortName() outputs (prefix + hex hash).
func TestWorkloadPortNameMatchesTheCNINamingScheme(t *testing.T) {
	for _, name := range []string{"cra0123ab", "craffffff", "v0123ab", "vffffff"} {
		if !workloadPortName.MatchString(name) {
			t.Errorf("workloadPortName does not match CNI-derived port %q", name)
		}
	}
	for _, name := range []string{"hbn", "hbn.501", "br2000", "l2vni2000", "eth0", "cra", "v", "craZZZZZZ"} {
		if workloadPortName.MatchString(name) {
			t.Errorf("workloadPortName wrongly claims ownership of %q", name)
		}
	}
}

func TestWorkloadTapPortAddMatchesOnlyWorkloadTaps(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want TapPortAdd
	}{
		{
			"workload tap in a vrf",
			"interface add port craeadbd8 devargs net_tap_craeadbd8,iface=craeadbd8_dp mtu 1500 vrf m2m",
			TapPortAdd{Name: "craeadbd8", MTU: 1500, BindKind: "vrf", BindName: "m2m"},
		},
		{
			"workload tap on a bridge",
			"interface add port craeadbd8 devargs net_tap_craeadbd8,iface=craeadbd8_dp mtu 9000 domain br2000",
			TapPortAdd{Name: "craeadbd8", MTU: 9000, BindKind: "domain", BindName: "br2000"},
		},
		{
			"workload tap with no binding",
			"interface add port craeadbd8 devargs net_tap_craeadbd8,iface=craeadbd8_dp mtu 1500",
			TapPortAdd{Name: "craeadbd8", MTU: 1500},
		},
		{
			"workload tap with no mtu",
			"interface add port craeadbd8 devargs net_tap_craeadbd8,iface=craeadbd8_dp vrf m2m",
			TapPortAdd{Name: "craeadbd8", BindKind: "vrf", BindName: "m2m"},
		},
		{"vhost-user workload port", "interface add port v1a2b3c4d devargs net_vhost0,iface=/var/run/v1a2b3c4d.sock mtu 1500", TapPortAdd{}},
		{"fabric uplink", "interface add port hbn devargs 0000:00:05.0 mtu 9100", TapPortAdd{}},
		{"trunk vlan member", "interface add vlan craeadbd8.100 parent craeadbd8 vlan_id 100", TapPortAdd{}},
		{"unrelated tap", "interface add port mgmt0 devargs net_tap_mgmt0,iface=mgmt0_dp mtu 1500", TapPortAdd{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := WorkloadTapPortAdd(tc.line)
			if tc.want.Name == "" {
				if ok {
					t.Fatalf("WorkloadTapPortAdd(%q) matched as %+v, want no match", tc.line, got)
				}
				return
			}
			if !ok || got != tc.want {
				t.Fatalf("WorkloadTapPortAdd(%q) = %+v, %v, want %+v, true", tc.line, got, ok, tc.want)
			}
		})
	}
}

// TestWorkloadTapPortAddReadsTheRenderedLine ties the pattern to what the
// renderer actually emits. The two are matched by hand, and a drift would be
// silent: the replay would go back to re-adding an adopted tap, which is the
// failure this recognises in the first place.
func TestWorkloadTapPortAddReadsTheRenderedLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		emit func(b *Batch)
		want TapPortAdd
	}{
		{
			"vrf port",
			func(b *Batch) { b.AddTapPort("craeadbd8", "m2m", 1500) },
			TapPortAdd{Name: "craeadbd8", MTU: 1500, BindKind: "vrf", BindName: "m2m"},
		},
		{
			"bridged port",
			func(b *Batch) { b.AddTapPortToBridge("craeadbd8", "br2000", 9000) },
			TapPortAdd{Name: "craeadbd8", MTU: 9000, BindKind: "domain", BindName: "br2000"},
		},
		{
			"trunk port, which renders no binding at all",
			func(b *Batch) { b.AddTrunkPort("craeadbd8", v1alpha1.PortTransportVeth, "", false, 9100) },
			TapPortAdd{Name: "craeadbd8", MTU: 9100},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &Batch{}
			tc.emit(b)
			got, ok := WorkloadTapPortAdd(strings.TrimSpace(b.String()))
			if !ok || got != tc.want {
				t.Fatalf("WorkloadTapPortAdd(%q) = %+v, %v, want %+v", b.String(), got, ok, tc.want)
			}
		})
	}
}

// TestParseInterfaceDetailReadsTheSingleInterfaceView pins the shape of
// `grcli -j interface show NAME`, which is an object and not a row of the list:
// it is the only view that carries the MTU, and the only one that separates a
// VRF binding from a bridge domain.
func TestParseInterfaceDetailReadsTheSingleInterfaceView(t *testing.T) {
	detail, err := ParseInterfaceDetail([]byte(
		`{"name":"craeadbd8","type":"port","id":7,"mode":"vrf","vrf":"m2m","mtu":1500,"speed":10000}`))
	if err != nil {
		t.Fatalf("ParseInterfaceDetail: %v", err)
	}
	if detail.MTU != 1500 || detail.VRF != "m2m" || detail.Domain != "" {
		t.Fatalf("ParseInterfaceDetail() = %+v", detail)
	}
}

func TestTapPortAddMismatch(t *testing.T) {
	inVRF := TapPortAdd{Name: "craeadbd8", MTU: 1500, BindKind: "vrf", BindName: "m2m"}
	onBridge := TapPortAdd{Name: "craeadbd8", MTU: 1500, BindKind: "domain", BindName: "br2000"}
	unbound := TapPortAdd{Name: "craeadbd8", MTU: 1500}

	for _, tc := range []struct {
		name    string
		add     TapPortAdd
		live    *InterfaceDetail
		wantBad bool
	}{
		{"vrf agrees", inVRF, &InterfaceDetail{MTU: 1500, Mode: "VRF", VRF: "m2m"}, false},
		{"bridge agrees", onBridge, &InterfaceDetail{MTU: 1500, Mode: "bridge", Domain: "br2000"}, false},
		{"different mtu", inVRF, &InterfaceDetail{MTU: 9000, Mode: "VRF", VRF: "m2m"}, true},
		{"different vrf", inVRF, &InterfaceDetail{MTU: 1500, Mode: "VRF", VRF: "c2m"}, true},
		{"different domain", onBridge, &InterfaceDetail{MTU: 1500, Mode: "bridge", Domain: "br3000"}, true},
		// The mode has to be checked on its own: a port that moved between a
		// VRF and a bridge reports the binding it no longer has as empty, so
		// the name comparison alone would wave it through.
		{"vrf port became bridged", inVRF, &InterfaceDetail{MTU: 1500, Mode: "bridge", Domain: "br2000"}, true},
		{"bridged port moved to a vrf", onBridge, &InterfaceDetail{MTU: 1500, Mode: "VRF", VRF: "m2m"}, true},
		// No trailing binding means the default VRF, not "anything goes".
		{"unbound stayed in the default vrf", unbound, &InterfaceDetail{MTU: 1500, Mode: "VRF", VRF: "main"}, false},
		{"unbound moved into a tenant vrf", unbound, &InterfaceDetail{MTU: 1500, Mode: "VRF", VRF: "m2m"}, true},
		{"unbound became bridged", unbound, &InterfaceDetail{MTU: 1500, Mode: "bridge", Domain: "br2000"}, true},
		// Neither of these is a bridge domain, and a port that ended up in one
		// of them reports no domain at all -- so only the mode catches it.
		{"bridged port became cross-connected", onBridge, &InterfaceDetail{MTU: 1500, Mode: "XC"}, true},
		{"bridged port became a bond member", onBridge, &InterfaceDetail{MTU: 1500, Mode: "bond"}, true},
		{"grout reported neither", inVRF, &InterfaceDetail{}, false},
		{"nothing to compare against", inVRF, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.add.Mismatch(tc.live); (got != "") != tc.wantBad {
				t.Fatalf("Mismatch(%+v) = %q, want bad=%v", tc.live, got, tc.wantBad)
			}
		})
	}
}

func TestLiveWorkloadPortsIgnoresEverythingButWorkloadPorts(t *testing.T) {
	live := []Interface{
		{Name: "craeadbd8", Type: "port"},
		{Name: "v1a2b3c4d", Type: "port"},
		{Name: "hbn", Type: "port"},
		{Name: "craeadbd8.100", Type: "vlan"},
		{Name: "br-overlay", Type: "bridge"},
	}

	got := LiveWorkloadPorts(live)
	if len(got) != 2 || !got["craeadbd8"] || !got["v1a2b3c4d"] {
		t.Fatalf("LiveWorkloadPorts() = %+v", got)
	}
}
