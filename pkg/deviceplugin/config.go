/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package deviceplugin implements the node-local Kubernetes device plugin that
// hands vhost-user socket rendezvous points to workloads.
//
// It is our open-source replacement for the 6WIND HNA device plugin whose
// contract is documented in docs/proposals/03-vhost-user/DEVICE-PLUGIN.md, and
// it keeps that contract deliberately: the same crossed socket trees, the same
// per-device host directory keyed by an opaque device id, the same pod-side
// ordinal directory, the same k8snetworkplumbingwg device-info file. What it
// adds is that every path is configurable, so the same plugin serves the 6WIND
// vSR layout (/run/vsr-{vhost,virtio}-user) and the grout layout without a
// rebuild, and so an e2e lab can put the whole tree under a scratch directory.
//
// Like the 6WIND plugin it creates no network state whatsoever: it makes a
// directory, bind-mounts it into the container, exports an environment variable
// and writes a device-info file. Everything that turns the socket into a network
// -- the fast-path vhost port, the addressing, the routes -- is the workload CNI
// plugin's and the CRA agent's job.
package deviceplugin

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const (
	// DefaultResourceDomain is the device-plugin resource domain we advertise
	// under. It is deliberately ours rather than 6WIND's: a node may run both
	// plugins, and two plugins registering the same resource name fight over
	// the same kubelet endpoint.
	DefaultResourceDomain = "network.t-caas.telekom.com"

	// ResourceVhostUser names the resource whose holder is the vhost-user end
	// of the socket, i.e. the workload owns (listens on) it and the fast path
	// connects.
	ResourceVhostUser = "vhost-user"
	// ResourceVirtioUser names the resource whose holder is the virtio-user end
	// of the socket, i.e. the fast path owns (listens on) it and the workload
	// connects. This is the usual one for a KubeVirt VM.
	ResourceVirtioUser = "virtio-user"

	// DefaultVhostUserDir is the host tree in which the FAST PATH owns
	// (listens on) the socket. Matches the 6WIND vSR layout.
	DefaultVhostUserDir = "/run/vsr-vhost-user"
	// DefaultVirtioUserDir is the host tree in which the WORKLOAD owns
	// (listens on) the socket. Matches the 6WIND vSR layout.
	DefaultVirtioUserDir = "/run/vsr-virtio-user"

	// DefaultKubeletSocketDir is the directory kubelet watches for device-plugin
	// registration sockets.
	DefaultKubeletSocketDir = "/var/lib/kubelet/device-plugins"

	// DefaultDeviceInfoDir is where a device plugin writes its device-info
	// files, by the k8snetworkplumbingwg convention. Multus copies the matching
	// file to the CNI's own device-info path before invoking it.
	DefaultDeviceInfoDir = "/var/run/k8s.cni.cncf.io/devinfo/dp"

	// SocketFile is the socket filename inside a per-device directory. Fixed by
	// the 6WIND contract the CNI and the KubeVirt hook already implement.
	SocketFile = "socket"

	// DefaultDeviceCount is how many sockets of each resource are advertised.
	// Devices are pure bookkeeping (a directory each), so the count is only an
	// upper bound on concurrent attachments per node.
	DefaultDeviceCount = 64

	// DefaultSocketOwner is the uid/gid the per-device socket directory is
	// chowned to. 107 is the qemu user in the KubeVirt launcher image, which
	// must be able to create or open the socket inside it.
	DefaultSocketOwner = 107

	// dirMode is the mode of a per-device socket directory. It is group- and
	// other-traversable because the two ends of the socket run as different
	// users (the fast path as root, the VM as qemu).
	dirMode = 0o775

	// hostDirMode is the mode of a directory we create on the host that is not
	// itself a socket directory (the kubelet socket dir, the device-info dir).
	hostDirMode = 0o755

	// deviceInfoMode is the mode of a device-info file. It is world-readable
	// because Multus reads it as a different user than we write it.
	deviceInfoMode = 0o644

	// registerTimeout bounds the registration RPC to kubelet.
	registerTimeout = 10 * time.Second
)

// Config is the plugin's runtime configuration. Every path is settable so the
// plugin can serve a vSR node, a grout node and an e2e lab unchanged.
type Config struct {
	// ResourceDomain prefixes the advertised resource names
	// (<domain>/vhost-user, <domain>/virtio-user).
	ResourceDomain string
	// VhostUserDir is the host tree in which the FAST PATH owns the socket.
	VhostUserDir string
	// VirtioUserDir is the host tree in which the WORKLOAD owns the socket.
	VirtioUserDir string
	// KubeletSocketDir is the kubelet device-plugin socket directory.
	KubeletSocketDir string
	// DeviceInfoDir is where the device-info JSON files are written.
	DeviceInfoDir string
	// DeviceCount is how many devices of each resource to advertise.
	DeviceCount int
	// SocketOwnerUID / SocketOwnerGID own the per-device socket directory.
	SocketOwnerUID int
	SocketOwnerGID int
	// Resources restricts which of the two resources are advertised. Empty
	// means both.
	Resources []string
}

// DefaultConfig returns the 6WIND-compatible defaults.
func DefaultConfig() Config {
	return Config{
		ResourceDomain:   DefaultResourceDomain,
		VhostUserDir:     DefaultVhostUserDir,
		VirtioUserDir:    DefaultVirtioUserDir,
		KubeletSocketDir: DefaultKubeletSocketDir,
		DeviceInfoDir:    DefaultDeviceInfoDir,
		DeviceCount:      DefaultDeviceCount,
		SocketOwnerUID:   DefaultSocketOwner,
		SocketOwnerGID:   DefaultSocketOwner,
	}
}

// Validate fills in defaults for unset fields and rejects a configuration that
// could only produce sockets nothing connects to.
func (c *Config) Validate() error {
	if c.ResourceDomain == "" {
		c.ResourceDomain = DefaultResourceDomain
	}
	if c.VhostUserDir == "" {
		c.VhostUserDir = DefaultVhostUserDir
	}
	if c.VirtioUserDir == "" {
		c.VirtioUserDir = DefaultVirtioUserDir
	}
	if c.KubeletSocketDir == "" {
		c.KubeletSocketDir = DefaultKubeletSocketDir
	}
	if c.DeviceInfoDir == "" {
		c.DeviceInfoDir = DefaultDeviceInfoDir
	}
	if c.DeviceCount == 0 {
		c.DeviceCount = DefaultDeviceCount
	}
	if len(c.Resources) == 0 {
		c.Resources = []string{ResourceVhostUser, ResourceVirtioUser}
	}

	if c.DeviceCount < 0 {
		return fmt.Errorf("device count %d must be positive", c.DeviceCount)
	}
	for _, dir := range []string{c.VhostUserDir, c.VirtioUserDir, c.KubeletSocketDir, c.DeviceInfoDir} {
		if !filepath.IsAbs(dir) {
			return fmt.Errorf("path %q must be absolute", dir)
		}
	}
	// The two trees name the same socket from the two ends. Collapsing them
	// would make the host and pod paths identical, which happens to work only
	// as long as nothing is bind-mounted -- and silently breaks the moment a
	// container gets the crossed mount.
	if filepath.Clean(c.VhostUserDir) == filepath.Clean(c.VirtioUserDir) {
		return fmt.Errorf("vhost-user and virtio-user directories must differ (both %q)", c.VhostUserDir)
	}
	for _, r := range c.Resources {
		if r != ResourceVhostUser && r != ResourceVirtioUser {
			return fmt.Errorf("unknown resource %q (want %q or %q)", r, ResourceVhostUser, ResourceVirtioUser)
		}
	}
	return nil
}

// ResourceName returns the fully-qualified resource name for a short resource.
func (c *Config) ResourceName(resource string) string {
	return c.ResourceDomain + "/" + resource
}

// socketTrees returns the host-side and pod-side socket trees for a resource.
//
// The two are crossed on purpose, exactly as in the 6WIND plugin: each side is
// named after the ROLE OF THE FAR END. A workload requesting virtio-user is
// itself the virtio-user client, so the socket it connects to lives in the tree
// where the fast path is the vhost-user backend -- and it sees that socket, in
// its own mount namespace, under the virtio-user tree.
func (c *Config) socketTrees(resource string) (hostTree, podTree string) {
	if resource == ResourceVhostUser {
		return c.VirtioUserDir, c.VhostUserDir
	}
	return c.VhostUserDir, c.VirtioUserDir
}

// deviceEnvVar is the environment variable listing the device ids allocated for
// a resource, mirroring the 6WIND HNA_<RESOURCE>_DEVICES convention under our
// own prefix.
func deviceEnvVar(resource string) string {
	return "DSNO_" + strings.ToUpper(strings.ReplaceAll(resource, "-", "_")) + "_DEVICES"
}

// deviceInfoFileName is the device-info filename fixed by the NPWG client
// library: <resourceName with "/" replaced by "-">-<deviceID>-device.json.
func deviceInfoFileName(resourceName, deviceID string) string {
	return strings.ReplaceAll(resourceName, "/", "-") + "-" + deviceID + "-device.json"
}
