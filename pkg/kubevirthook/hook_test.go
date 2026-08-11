package kubevirthook

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// domainWithInterface is the shape KubeVirt produces when it does generate an
// interface (a binding plugin with a domainAttachmentType, or a bridge binding).
const domainWithInterface = `<domain type='kvm'>
  <name>default_vhostuser-vm</name>
  <memoryBacking>
    <hugepages></hugepages>
    <source type="memfd"></source>
  </memoryBacking>
  <devices>
    <interface type='ethernet'>
      <alias name='ua-fastnet'/>
      <mac address='02:00:00:11:22:33'/>
      <target dev='tap0' managed='no'/>
      <model type='virtio'/>
    </interface>
    <console type='pty'/>
  </devices>
</domain>`

// domainWithoutInterface is the shape a binding plugin registered with no
// domainAttachmentType produces: no <interface> at all.
const domainWithoutInterface = `<domain type='kvm'>
  <name>default_vhostuser-vm</name>
  <memoryBacking>
    <hugepages></hugepages>
    <source type="memfd"></source>
  </memoryBacking>
  <devices>
    <console type='pty'/>
  </devices>
</domain>`

func vmiJSON(t *testing.T, ifaces ...VMIInterface) []byte {
	t.Helper()
	var vmi VMI
	vmi.Metadata.Name = "vhostuser-vm"
	vmi.Metadata.Namespace = "default"
	vmi.Spec.Domain.Devices.Interfaces = ifaces
	data, err := json.Marshal(vmi)
	if err != nil {
		t.Fatalf("marshalling VMI: %v", err)
	}
	return data
}

func boundInterface(name string) VMIInterface {
	iface := VMIInterface{Name: name}
	iface.Binding = &VMIBinding{Name: HookName}
	return iface
}

func writeNetworkInfo(t *testing.T, info NetworkInfo) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "network-info")
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshalling network-info: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writing network-info: %v", err)
	}
	return path
}

func vhostUserInfo(network, path, mode string) NetworkInfo {
	return NetworkInfo{Interfaces: []NetworkInterface{{
		Network: network,
		DeviceInfo: &DeviceInfo{
			Type:      DeviceInfoTypeVhostUser,
			Version:   "1.0.0",
			VhostUser: &VhostDevice{Mode: mode, Path: path},
		},
	}}}
}

func runHook(t *testing.T, domain, infoPath string, ifaces ...VMIInterface) (string, error) {
	t.Helper()
	hook := &Hook{
		BindingName:     HookName,
		NetworkInfoPath: infoPath,
		Logf:            func(string, ...any) {},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := hook.OnDefineDomain(ctx, []byte(domain), vmiJSON(t, ifaces...))
	return string(out), err
}

func TestInterfaceIsCreatedWhenKubeVirtGeneratedNone(t *testing.T) {
	info := writeNetworkInfo(t, vhostUserInfo("fastnet", "/run/vsr-virtio-user/0/socket", ModeClient))

	out, err := runHook(t, domainWithoutInterface, info, boundInterface("fastnet"))
	if err != nil {
		t.Fatalf("OnDefineDomain: %v", err)
	}
	for _, want := range []string{
		"<interface type='vhostuser'>",
		"<alias name='ua-fastnet'/>",
		"<source type='unix' path='/run/vsr-virtio-user/0/socket' mode='client'/>",
		"<model type='virtio'/>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("domain is missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "<interface") != 1 {
		t.Errorf("expected exactly one interface:\n%s", out)
	}
}

func TestGeneratedInterfaceIsReplacedNotDuplicated(t *testing.T) {
	info := writeNetworkInfo(t, vhostUserInfo("fastnet", "/run/socket", ModeClient))

	out, err := runHook(t, domainWithInterface, info, boundInterface("fastnet"))
	if err != nil {
		t.Fatalf("OnDefineDomain: %v", err)
	}
	if strings.Count(out, "<interface") != 1 {
		t.Errorf("expected exactly one interface:\n%s", out)
	}
	// The tap backend must be gone: leaving it would back one NIC with two
	// contradicting things.
	if strings.Contains(out, "tap0") || strings.Contains(out, "type='ethernet'") {
		t.Errorf("tap backend survived the conversion:\n%s", out)
	}
	// The MAC KubeVirt pinned has to survive, or the guest sees the NIC change
	// identity across a restart.
	if !strings.Contains(out, "<mac address='02:00:00:11:22:33'/>") {
		t.Errorf("generated MAC was dropped:\n%s", out)
	}
}

func TestVMIMACWinsOverTheGeneratedOne(t *testing.T) {
	info := writeNetworkInfo(t, vhostUserInfo("fastnet", "/run/socket", ModeClient))
	iface := boundInterface("fastnet")
	iface.MacAddress = "02:00:00:aa:bb:cc"

	out, err := runHook(t, domainWithInterface, info, iface)
	if err != nil {
		t.Fatalf("OnDefineDomain: %v", err)
	}
	if !strings.Contains(out, "<mac address='02:00:00:aa:bb:cc'/>") {
		t.Errorf("VMI MAC was not pinned:\n%s", out)
	}
}

func TestEverythingOutsideTheInterfaceIsPreservedByteForByte(t *testing.T) {
	info := writeNetworkInfo(t, vhostUserInfo("fastnet", "/run/socket", ModeClient))

	out, err := runHook(t, domainWithoutInterface, info, boundInterface("fastnet"))
	if err != nil {
		t.Fatalf("OnDefineDomain: %v", err)
	}
	// Splicing, not re-encoding: everything the hook does not own has to come
	// back exactly as KubeVirt wrote it, formatting and quoting included. A
	// round trip through encoding/xml would rewrite all of these.
	for _, want := range []string{
		"<domain type='kvm'>\n  <name>default_vhostuser-vm</name>",
		"<hugepages></hugepages>",
		"<source type=\"memfd\"></source>",
		"<console type='pty'/>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%q was rewritten or dropped:\n%s", want, out)
		}
	}
}

func TestInterfacesOfOtherBindingsAreLeftAlone(t *testing.T) {
	info := writeNetworkInfo(t, vhostUserInfo("fastnet", "/run/socket", ModeClient))
	other := VMIInterface{Name: "mgmt"}
	other.Binding = &VMIBinding{Name: "passt"}

	out, err := runHook(t, domainWithoutInterface, info, other)
	if err != nil {
		t.Fatalf("OnDefineDomain: %v", err)
	}
	if out != domainWithoutInterface {
		t.Errorf("domain was modified for a foreign binding:\n%s", out)
	}
}

func TestServerModeIsPassedThrough(t *testing.T) {
	info := writeNetworkInfo(t, vhostUserInfo("fastnet", "/run/socket", ModeServer))

	out, err := runHook(t, domainWithoutInterface, info, boundInterface("fastnet"))
	if err != nil {
		t.Fatalf("OnDefineDomain: %v", err)
	}
	if !strings.Contains(out, "mode='server'") {
		t.Errorf("socket mode was not passed through:\n%s", out)
	}
}

func TestGuestMemoryIsMadeShareable(t *testing.T) {
	// KubeVirt gives a hugepage-backed guest a memfd <memoryBacking> with no
	// access mode, which libvirt defaults to private; it only emits a shared
	// one for virtiofs. Setting it is this hook's job, because without it the
	// socket connects and no packet ever moves.
	info := writeNetworkInfo(t, vhostUserInfo("fastnet", "/run/socket", ModeClient))

	out, err := runHook(t, domainWithoutInterface, info, boundInterface("fastnet"))
	if err != nil {
		t.Fatalf("OnDefineDomain: %v", err)
	}
	if !strings.Contains(out, "<access mode='shared'/>") {
		t.Errorf("guest memory was not made shareable:\n%s", out)
	}
	if strings.Count(out, "<access") != 1 {
		t.Errorf("expected exactly one access element:\n%s", out)
	}
	if !strings.Contains(out, "<source type=\"memfd\"></source>") {
		t.Errorf("the memfd backing was disturbed:\n%s", out)
	}
}

func TestPrivateMemoryAccessIsCorrected(t *testing.T) {
	info := writeNetworkInfo(t, vhostUserInfo("fastnet", "/run/socket", ModeClient))
	domain := strings.ReplaceAll(domainWithoutInterface,
		"<source type=\"memfd\"></source>", "<access mode='private'/>")

	out, err := runHook(t, domain, info, boundInterface("fastnet"))
	if err != nil {
		t.Fatalf("OnDefineDomain: %v", err)
	}
	if strings.Contains(out, "private") {
		t.Errorf("private memory access survived:\n%s", out)
	}
	if strings.Count(out, "<access") != 1 {
		t.Errorf("expected exactly one access element:\n%s", out)
	}
}

func TestSharedMemoryIsLeftAlone(t *testing.T) {
	info := writeNetworkInfo(t, vhostUserInfo("fastnet", "/run/socket", ModeClient))
	domain := strings.ReplaceAll(domainWithoutInterface,
		"<source type=\"memfd\"></source>", "<access mode='shared'/>")

	out, err := runHook(t, domain, info, boundInterface("fastnet"))
	if err != nil {
		t.Fatalf("OnDefineDomain: %v", err)
	}
	if strings.Count(out, "<access") != 1 {
		t.Errorf("the existing access element was duplicated:\n%s", out)
	}
}

func TestMemoryWithoutHugepagesFailsTheVM(t *testing.T) {
	// No <memoryBacking> at all means the guest is not hugepage-backed, so
	// there is nothing shareable to fix up.
	info := writeNetworkInfo(t, vhostUserInfo("fastnet", "/run/socket", ModeClient))
	domain := `<domain type='kvm'><devices><console type='pty'/></devices></domain>`

	_, err := runHook(t, domain, info, boundInterface("fastnet"))
	if err == nil {
		t.Fatal("expected a domain without hugepages to be refused")
	}
	if !strings.Contains(err.Error(), "hugepages") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestUnknownSocketModeFailsTheVM(t *testing.T) {
	info := writeNetworkInfo(t, vhostUserInfo("fastnet", "/run/socket", "peer"))

	_, err := runHook(t, domainWithoutInterface, info, boundInterface("fastnet"))
	if err == nil {
		t.Fatal("expected an unknown socket mode to be refused")
	}
}

func TestMissingDeviceInfoFailsTheVM(t *testing.T) {
	// A device-info file that never names the interface: waiting it out and
	// failing is the point, because the alternative is a VM attached to
	// nothing.
	info := writeNetworkInfo(t, vhostUserInfo("othernet", "/run/socket", ModeClient))

	_, err := runHook(t, domainWithoutInterface, info, boundInterface("fastnet"))
	if err == nil {
		t.Fatal("expected a missing device-info entry to be refused")
	}
	if !strings.Contains(err.Error(), "fastnet") {
		t.Errorf("error does not name the interface: %v", err)
	}
}

func TestSRIOVDeviceInfoIsNotMistakenForVhostUser(t *testing.T) {
	info := writeNetworkInfo(t, NetworkInfo{Interfaces: []NetworkInterface{{
		Network:    "fastnet",
		DeviceInfo: &DeviceInfo{Type: "pci"},
	}}})

	_, err := runHook(t, domainWithoutInterface, info, boundInterface("fastnet"))
	if err == nil {
		t.Fatal("expected a non-vhost-user device-info to be refused")
	}
}

func TestDeviceInfoAppearingLateIsPickedUp(t *testing.T) {
	// The real timing: KubeVirt writes the annotation only once the launcher
	// pod is Ready and kubelet refreshes the projected volume afterwards, so
	// the file routinely shows up after the hook is called.
	dir := t.TempDir()
	path := filepath.Join(dir, "network-info")

	go func() {
		time.Sleep(300 * time.Millisecond)
		data, _ := json.Marshal(vhostUserInfo("fastnet", "/run/late/socket", ModeClient))
		_ = os.WriteFile(path, data, 0o600)
	}()

	out, err := runHook(t, domainWithoutInterface, path, boundInterface("fastnet"))
	if err != nil {
		t.Fatalf("OnDefineDomain: %v", err)
	}
	if !strings.Contains(out, "/run/late/socket") {
		t.Errorf("late device-info was not picked up:\n%s", out)
	}
}

func TestPartiallyWrittenDeviceInfoIsRetriedNotFatal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "network-info")
	if err := os.WriteFile(path, []byte("{\"interf"), 0o600); err != nil {
		t.Fatalf("writing truncated network-info: %v", err)
	}

	go func() {
		time.Sleep(300 * time.Millisecond)
		data, _ := json.Marshal(vhostUserInfo("fastnet", "/run/socket", ModeClient))
		_ = os.WriteFile(path, data, 0o600)
	}()

	if _, err := runHook(t, domainWithoutInterface, path, boundInterface("fastnet")); err != nil {
		t.Fatalf("a truncated projection should be retried, got: %v", err)
	}
}

func TestMalformedDomainFailsTheVM(t *testing.T) {
	info := writeNetworkInfo(t, vhostUserInfo("fastnet", "/run/socket", ModeClient))

	if _, err := runHook(t, "<domain><devices>", info, boundInterface("fastnet")); err == nil {
		t.Fatal("expected malformed domain XML to be refused")
	}
}

func TestDomainWithoutDevicesFailsTheVM(t *testing.T) {
	info := writeNetworkInfo(t, vhostUserInfo("fastnet", "/run/socket", ModeClient))
	domain := `<domain type='kvm'><memoryBacking><hugepages/></memoryBacking></domain>`

	if _, err := runHook(t, domain, info, boundInterface("fastnet")); err == nil {
		t.Fatal("expected a domain without <devices> to be refused")
	}
}

func TestNestedInterfaceIsNotMistakenForADevice(t *testing.T) {
	info := writeNetworkInfo(t, vhostUserInfo("fastnet", "/run/socket", ModeClient))
	domain := `<domain type='kvm'>
  <memoryBacking><hugepages/></memoryBacking>
  <metadata><something><interface><alias name='ua-fastnet'/></interface></something></metadata>
  <devices><console type='pty'/></devices>
</domain>`

	out, err := runHook(t, domain, info, boundInterface("fastnet"))
	if err != nil {
		t.Fatalf("OnDefineDomain: %v", err)
	}
	if !strings.Contains(out, "<metadata><something><interface><alias name='ua-fastnet'/></interface></something></metadata>") {
		t.Errorf("an <interface> outside <devices> was rewritten:\n%s", out)
	}
	if !strings.Contains(out, "<interface type='vhostuser'>") {
		t.Errorf("the device interface was not added:\n%s", out)
	}
}

func TestMultipleAttachmentsAreAllRendered(t *testing.T) {
	info := writeNetworkInfo(t, NetworkInfo{Interfaces: []NetworkInterface{
		{Network: "red", DeviceInfo: &DeviceInfo{Type: DeviceInfoTypeVhostUser,
			VhostUser: &VhostDevice{Mode: ModeClient, Path: "/run/red"}}},
		{Network: "blue", DeviceInfo: &DeviceInfo{Type: DeviceInfoTypeVhostUser,
			VhostUser: &VhostDevice{Mode: ModeClient, Path: "/run/blue"}}},
	}})

	out, err := runHook(t, domainWithoutInterface, info, boundInterface("red"), boundInterface("blue"))
	if err != nil {
		t.Fatalf("OnDefineDomain: %v", err)
	}
	if strings.Count(out, "<interface type='vhostuser'>") != 2 {
		t.Errorf("expected two interfaces:\n%s", out)
	}
	for _, want := range []string{"/run/red", "/run/blue"} {
		if !strings.Contains(out, want) {
			t.Errorf("domain is missing %q:\n%s", want, out)
		}
	}
}

func TestInfoAdvertisesOnlyOnDefineDomain(t *testing.T) {
	srv := NewServer(&Hook{})
	res, err := srv.Info(context.Background(), nil)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if len(res.GetHookPoints()) != 1 || res.GetHookPoints()[0].GetName() != onDefineDomainHookPointName {
		t.Errorf("unexpected hook points: %v", res.GetHookPoints())
	}
	if len(res.GetVersions()) != 1 || res.GetVersions()[0] != hookVersion {
		t.Errorf("unexpected versions: %v", res.GetVersions())
	}
}

func TestShutdownIsIdempotent(t *testing.T) {
	srv := NewServer(&Hook{})
	for range 2 {
		if _, err := srv.Shutdown(context.Background(), nil); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}
	select {
	case <-srv.Stopped():
	default:
		t.Error("Shutdown did not signal the server to stop")
	}
}
