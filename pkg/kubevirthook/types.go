// Package kubevirthook implements the KubeVirt onDefineDomain hook that turns a
// vhost-user allocation into a guest NIC.
//
// KubeVirt has no first-class vhost-user interface binding. What it does have
// is the hook sidecar interface: a container that is handed the VMI object and
// the libvirt domain XML KubeVirt just generated, and returns a modified
// domain. This hook writes the <interface type='vhostuser'> that connects the
// guest's virtio NIC to the CRA fast path (grout net_vhost, 6WIND fpvhost).
//
// # Why the hook has to create the interface, not just rewrite one
//
// A vhost-user socket has no kernel netdev in the pod, so none of KubeVirt's
// own bindings can be used: bridge and masquerade both abort the launcher with
// "pod link (podXXXX) is missing" before the domain is ever generated. The VMI
// therefore points its interface at a network binding plugin registered with no
// domainAttachmentType, which makes KubeVirt skip pod-link discovery entirely
// -- and generate no <interface> element either. This hook handles both shapes:
// it replaces a generated element when there is one and appends a complete one
// when there is not.
//
// # Where the socket path comes from
//
// From the device-info downward API, which is the mechanism KubeVirt provides
// for exactly this case.
//
// The chain is: the device plugin publishes a CNCF device-info file naming the
// pod-side socket; Multus merges it into the pod's k8s.v1.cni.cncf.io/network-status
// annotation; KubeVirt distils that into its own kubevirt.io/network-info
// annotation and projects it into this sidecar as a downward-API volume, keyed
// by VMI interface name. Nothing has to be restated by hand on the VM, and the
// path this hook writes is the one the device plugin actually allocated.
//
// Reading the pod's network-status directly would not work: KubeVirt hands the
// hook the VMI, and pod annotations are not copied onto the VMI. That is what
// the downward-API volume is for. It has to be requested per plugin:
//
//	configuration:
//	  network:
//	    binding:
//	      vhostuser:
//	        sidecarImage: <this image>
//	        downwardAPI: device-info
//
// # Requirements the VM manifest must satisfy
//
// vhost-user works by letting the CRA map the guest's virtio rings, so guest
// memory must be shareable. KubeVirt emits <memoryBacking><access mode='shared'/>
// only when hugepages are configured. Without that the socket connects and no
// packet ever moves, so this hook refuses the domain instead.
package kubevirthook

// NetworkInfo is the KubeVirt device-info downward-API file
// (/etc/podinfo/network-info). Only the fields this hook needs are declared;
// the shapes mirror kubevirt.io/kubevirt/pkg/network/downwardapi and the CNCF
// network-attachment-definition-client types, which are deliberately not
// imported: the first lives in KubeVirt's main module and the second drags in
// the Kubernetes API machinery, for four structs that are wire-stable.
type NetworkInfo struct {
	Interfaces []NetworkInterface `json:"interfaces"`
}

// NetworkInterface binds a device-info block to a VMI interface. Network is the
// VMI's own interface/network name (spec.domain.devices.interfaces[].name), not
// the NAD name and not the Multus "netN" name.
type NetworkInterface struct {
	Network    string      `json:"network"`
	DeviceInfo *DeviceInfo `json:"deviceInfo,omitempty"`
}

// DeviceInfo is the CNCF device-info block. Type discriminates the union; only
// vhost-user is of interest here (an SR-IOV attachment would say "pci").
type DeviceInfo struct {
	Type      string       `json:"type,omitempty"`
	Version   string       `json:"version,omitempty"`
	VhostUser *VhostDevice `json:"vhost-user,omitempty"`
}

// VhostDevice names a vhost-user socket. Mode is stated from the WORKLOAD's
// perspective and maps directly onto libvirt's <source mode='...'>, so a VM
// holding a virtio-user allocation is the client and the CRA serves the socket.
type VhostDevice struct {
	Mode string `json:"mode,omitempty"`
	Path string `json:"path,omitempty"`
}

// DeviceInfoTypeVhostUser is the CNCF device-info type for a vhost-user socket.
const DeviceInfoTypeVhostUser = "vhost-user"

// Socket modes, as used by both the CNCF device-info block and libvirt.
const (
	ModeClient = "client"
	ModeServer = "server"
)

// VMI is the sliver of the VirtualMachineInstance JSON this hook reads.
type VMI struct {
	Metadata VMIMetadata `json:"metadata"`
	Spec     VMISpec     `json:"spec"`
}

// VMIMetadata carries the identity used in log lines and error messages.
type VMIMetadata struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// VMISpec is the part of spec this hook reads.
type VMISpec struct {
	Domain VMIDomain `json:"domain"`
}

// VMIDomain is the part of spec.domain this hook reads.
type VMIDomain struct {
	Devices VMIDevices `json:"devices"`
}

// VMIDevices is the part of spec.domain.devices this hook reads.
type VMIDevices struct {
	Interfaces []VMIInterface `json:"interfaces"`
}

// VMIInterface is one entry of spec.domain.devices.interfaces.
type VMIInterface struct {
	Name       string      `json:"name"`
	MacAddress string      `json:"macAddress,omitempty"`
	Binding    *VMIBinding `json:"binding,omitempty"`
}

// VMIBinding names the network binding plugin an interface is bound to.
type VMIBinding struct {
	Name string `json:"name"`
}

// Attachment is one interface this hook has to render into the domain.
type Attachment struct {
	// Name is the VMI interface name; the libvirt alias is "ua-" + Name.
	Name string
	// SocketPath is the POD-side path: the domain XML is resolved in the
	// launcher's mount namespace, so a host path would name a socket the VM
	// cannot open.
	SocketPath string
	// Mode is the libvirt <source mode>: "client" or "server".
	Mode string
	// MAC is optional; empty lets libvirt assign one.
	MAC string
}
