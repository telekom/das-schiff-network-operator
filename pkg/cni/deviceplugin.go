//go:build linux

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

package cni

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// This file implements the 6WIND HNA device-plugin contract for the vhost-user
// transport (see docs/proposals/03-vhost-user/DEVICE-PLUGIN.md).
//
// The plugin's whole input is an opaque deviceID: the NAD carries the resource
// name as the k8s.v1.cni.cncf.io/resourceName annotation, the pod requests that
// resource, and Multus resolves the pair into a device ID which it injects into
// our config as a top-level "deviceID" field (and, with the deviceID capability,
// into runtimeConfig). Every path is derived from it — nothing about the socket
// is chooseable by the NAD author.

const (
	// vhostUserSocketDir is the host tree in which vSR owns (listens on) the
	// vhost-user socket; workloads connect to it.
	vhostUserSocketDir = "/run/vsr-vhost-user"
	// virtioUserSocketDir is the host tree in which a workload owns the socket
	// and vSR connects to it.
	virtioUserSocketDir = "/run/vsr-virtio-user"
	// vhostUserSocketFile is the socket filename inside a per-device directory.
	vhostUserSocketFile = "socket"

	// defaultDeviceResource is the device-plugin resource a vhost-user
	// attachment is assumed to request when the NAD does not say otherwise. It
	// matches the reference manifests: the workload is the virtio-user end, so
	// vSR is the vhost-user backend.
	defaultDeviceResource = "nc-k8s-plugin.6wind.com/virtio-user"

	// deviceInfoDPDir is where a device plugin writes its device-info files, by
	// the k8snetworkplumbingwg convention. Multus copies the matching file to
	// our CNIDeviceInfoFile path before invoking us, but that copy is
	// best-effort, so we read the original as a second source.
	deviceInfoDPDir = "/var/run/k8s.cni.cncf.io/devinfo/dp"

	// deviceInfoType is the device-info "type" for a vhost-user attachment.
	deviceInfoType = "vhost-user"

	// fallbackDeviceIndex is the pod-side ordinal assumed when neither
	// device-info file is readable. The device plugin numbers the indices fresh
	// per Allocate(), so a pod requesting a single socket always sees it at
	// index 0; a pod with several attachments cannot be resolved this way, which
	// is exactly why the device-info files are preferred.
	fallbackDeviceIndex = "0"
)

// vhostUserAttachment is the resolved socket wiring for one vhost-user
// attachment. The host and pod paths are deliberately different: they name the
// same socket from two mount namespaces, and swapping them yields a socket that
// silently never connects.
type vhostUserAttachment struct {
	// DeviceID is the device-plugin handle Multus injected. It is ephemeral (the
	// plugin regenerates it on restart) and must never be persisted.
	DeviceID string
	// HostPath is the socket path in the host mount namespace. It goes to the
	// node-local agent, which renders the vSR fpvhost virtual-port.
	HostPath string
	// PodPath is the socket path in the pod mount namespace. It goes into the
	// device-info file we publish, which the KubeVirt hook turns into the
	// domain XML.
	PodPath string
	// Mode is the vhost-user socket mode from the workload's perspective. The
	// NAD userdata selects it because the current 6WIND device plugin always
	// reports "server" in its device-info file.
	Mode string
}

// deviceID returns the device-plugin-allocated device identifier, preferring the
// capability route (runtimeConfig) over the top-level field Multus injects
// unconditionally. Empty when the pod requested no matching resource, or when
// Multus's per-resource cursor ran out of allocated devices.
func (c *NetConf) deviceID() string {
	if c.RuntimeConfig.DeviceID != "" {
		return c.RuntimeConfig.DeviceID
	}
	return c.DeviceID
}

// deviceResource returns the device-plugin resource name the attachment was
// allocated from, or the 6WIND virtio-user default.
func (c *NetConf) deviceResource() string {
	if c.DeviceResource != "" {
		return c.DeviceResource
	}
	return defaultDeviceResource
}

// socketTrees returns the host-side and pod-side socket directories for the
// configured resource. The two trees are crossed on purpose: each side is named
// after the role of the far end, so a workload requesting virtio-user finds its
// socket in the tree where vSR is the vhost-user backend.
func (c *NetConf) socketTrees() (hostTree, podTree string) {
	resource := c.deviceResource()
	if i := strings.LastIndex(resource, "/"); i >= 0 {
		resource = resource[i+1:]
	}
	resource = strings.TrimSuffix(resource, "-all")
	if resource == "vhost-user" {
		return virtioUserSocketDir, vhostUserSocketDir
	}
	return vhostUserSocketDir, virtioUserSocketDir
}

// resolveVhostUser derives the socket wiring for a vhost-user attachment.
//
// A deviceID is mandatory: it fixes the host-side path, and a device-info file
// (staged for us by Multus, or read straight from the device plugin's own
// directory) supplies the pod-side path. socket_mode is instead selected by NAD
// userdata: the current 6WIND plugin always reports "server" in device info,
// so it is not authoritative for the socket role. Only when the device-info
// files are unreadable is the pod-side path derived positionally, which cannot
// be right for a pod holding several sockets.
//
// An absent deviceID is a hard failure rather than a fallback to a static path.
// It means the pod requested no matching resource, or Multus's per-resource
// cursor ran out of allocated devices — and in both cases every path we could
// invent names a socket that nothing is listening on, so the attachment would
// come up silently dead instead of failing where the mistake was made.
func resolveVhostUser(conf *NetConf) (*vhostUserAttachment, error) {
	deviceID := conf.deviceID()
	if deviceID == "" {
		return nil, fmt.Errorf(
			"no deviceID for the %q transport: the pod must request the %q resource named by the "+
				"NetworkAttachmentDefinition's k8s.v1.cni.cncf.io/resourceName annotation, and must "+
				"request at least as many of them as it has attachments to that resource",
			TransportVhostUser, conf.deviceResource())
	}

	hostTree, podTree := conf.socketTrees()
	att := &vhostUserAttachment{
		DeviceID: deviceID,
		HostPath: filepath.Join(hostTree, deviceID, vhostUserSocketFile),
		PodPath:  filepath.Join(podTree, fallbackDeviceIndex, vhostUserSocketFile),
		Mode:     conf.socketMode(),
	}
	// An explicit socketPath still overrides the host side, so an operator can
	// point vSR at a socket the device plugin did not allocate.
	if conf.SocketPath != "" {
		att.HostPath = conf.SocketPath
	}

	// Device info states the pod-side path allocated by the plugin. Its mode is
	// deliberately ignored: current 6WIND device-plugin versions always report
	// "server", so NAD userdata is the current source of socket-role truth.
	if info := readDeviceInfo(stagedDeviceInfoPaths(conf, deviceID)); info != nil {
		if info.Path != "" {
			att.PodPath = info.Path
		}
	}
	return att, nil
}

// stagedDeviceInfoPaths lists the device-info files to consult, most
// authoritative first: the copy Multus stages for us (when the NAD requests the
// CNIDeviceInfoFile capability) and, because that copy is best-effort and its
// failures are only logged, the device plugin's original file.
func stagedDeviceInfoPaths(conf *NetConf, deviceID string) []string {
	var paths []string
	if p := conf.RuntimeConfig.CNIDeviceInfoFile; p != "" {
		paths = append(paths, p)
	}
	// <resourceName with "/" replaced by "-">-<deviceID>-device.json, a naming
	// fixed by the NPWG client library rather than by 6WIND.
	name := strings.ReplaceAll(conf.deviceResource(), "/", "-") + "-" + deviceID + "-device.json"
	return append(paths, filepath.Join(deviceInfoDPDir, name))
}

// readDeviceInfo returns the vhost-user section of the first readable, parsable
// device-info file. A missing or malformed file is not an error: both are
// expected (Multus's copy is best-effort), and the caller falls back to
// derivation.
func readDeviceInfo(paths []string) *vhostDeviceCfg {
	for _, path := range paths {
		data, err := os.ReadFile(path) //nolint:gosec // the path comes from the runtime's own capability config
		if err != nil {
			continue
		}
		var info deviceInfo
		if err := json.Unmarshal(data, &info); err != nil {
			continue
		}
		if info.VhostUser == nil {
			continue
		}
		return info.VhostUser
	}
	return nil
}
