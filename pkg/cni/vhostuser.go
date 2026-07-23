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

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/plugins/pkg/ipam"
)

// The vhost-user transport is a DPDK/virtio-user fast-path attachment and is
// VSR-only: there is no veth pair and no CRA-side netns port move. The workload
// (a KubeVirt VM presented an "sriov" interface, wired by the vhost-user hook
// sidecar) connects to a shared unix socket, and the VSR fast-path terminates
// the other end as an fpvhost virtual-port. This plugin's job for vhost-user is
// therefore limited to:
//   - publishing a CNIDeviceInfoFile so the downstream KubeVirt hook can attach
//     the vhost-user device to the domain,
//   - handing the attachment (socket path + mode + routed/L2 intent) to the
//     node-local agent, which renders the VSR fpvhost virtual-port via NETCONF.
//
// It cannot be exercised without the 6WIND device plugin and a real VSR, so it
// is implemented against the reference NAD/VM manifests and validated blind.

// deviceInfoVersion is the network-device-info schema version written to the
// CNIDeviceInfoFile.
const deviceInfoVersion = "1.1.0"

// File permissions for the published device info. The directory is traversable
// and the file world-readable so downstream consumers (the KubeVirt hook) can
// read it.
const (
	deviceInfoDirMode  = 0o755
	deviceInfoFileMode = 0o644
)

// deviceInfo is the minimal subset of the k8snetworkplumbingwg network device
// info schema needed to describe a vhost-user attachment to downstream
// consumers (the KubeVirt vhost-user hook sidecar).
type deviceInfo struct {
	Type      string          `json:"type"`
	Version   string          `json:"version"`
	VhostUser *vhostDeviceCfg `json:"vhost-user,omitempty"`
}

// vhostDeviceCfg carries the vhost-user socket coordinates.
type vhostDeviceCfg struct {
	Mode string `json:"mode"`
	Path string `json:"path"`
}

// cmdAddVhostUser implements the CNI ADD command for the vhost-user transport.
func cmdAddVhostUser(conf *NetConf, args *skel.CmdArgs) error {
	result, cleanupIPAM, err := runOptionalIPAM(conf, args)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		if !success {
			cleanupIPAM()
		}
	}()

	gwV4, err := conf.gatewayV4()
	if err != nil {
		return err
	}
	gwV6, err := conf.gatewayV6()
	if err != nil {
		return err
	}

	// The interface name VSR assigns to the fpvhost port, derived
	// deterministically from the container ID (like the veth CRA-side port).
	port := portName(args.ContainerID)

	// Publish the device info so the KubeVirt hook can attach the socket.
	if err := writeDeviceInfo(conf); err != nil {
		return err
	}

	// Hand the attachment to the node-local agent (VSR/grout render the vhost
	// port). The request carries transport=vhostuser plus the socket path/mode,
	// so the agent knows to create an fpvhost / net_vhost virtual-port rather
	// than a veth/tap port.
	if _, err := notifyAgentAdd(conf, args, port, gwV4, gwV6, result); err != nil {
		_ = removeDeviceInfo(conf)
		return err
	}

	result.Interfaces = []*current.Interface{{Name: args.IfName, Sandbox: args.Netns}}
	for i := range result.IPs {
		idx := 0
		result.IPs[i].Interface = &idx
	}

	success = true
	if err := types.PrintResult(result, conf.CNIVersion); err != nil {
		return fmt.Errorf("printing CNI result: %w", err)
	}
	return nil
}

// cmdDelVhostUser implements the CNI DEL command for the vhost-user transport.
func cmdDelVhostUser(conf *NetConf, args *skel.CmdArgs) error {
	if len(conf.IPAM) != 0 {
		if err := ipam.ExecDel(ipamTypeOrEmpty(conf), args.StdinData); err != nil {
			return fmt.Errorf("failed to release IPAM allocation: %w", err)
		}
	}
	_ = notifyAgentDel(conf, args, portName(args.ContainerID))
	_ = removeDeviceInfo(conf)
	return nil
}

// runOptionalIPAM runs the delegated IPAM plugin if one is configured, returning
// the CNI result and a cleanup function that releases the allocation. vhost-user
// attachments may run without IPAM (guest-side addressing), so a missing IPAM
// block yields an empty result and a no-op cleanup.
func runOptionalIPAM(conf *NetConf, args *skel.CmdArgs) (*current.Result, func(), error) {
	if len(conf.IPAM) == 0 {
		return &current.Result{}, func() {}, nil
	}
	ipamResult, err := runIPAM(conf, args)
	if err != nil {
		return nil, nil, err
	}
	result, err := current.NewResultFromResult(ipamResult)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to convert IPAM result: %w", err)
	}
	cleanup := func() { _ = ipam.ExecDel(ipamTypeOrEmpty(conf), args.StdinData) }
	return result, cleanup, nil
}

// writeDeviceInfo publishes the vhost-user socket coordinates to the
// CNIDeviceInfoFile so downstream consumers (KubeVirt hook sidecar) can attach
// the device to the guest domain.
func writeDeviceInfo(conf *NetConf) error {
	path := conf.RuntimeConfig.CNIDeviceInfoFile
	if path == "" {
		// No consumer requested a device info file; nothing to publish.
		return nil
	}
	info := &deviceInfo{
		Type:    "vhost-user",
		Version: deviceInfoVersion,
		VhostUser: &vhostDeviceCfg{
			Mode: conf.SocketMode,
			Path: conf.SocketPath,
		},
	}
	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("marshalling device info: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), deviceInfoDirMode); err != nil {
		return fmt.Errorf("creating device info dir: %w", err)
	}
	if err := os.WriteFile(path, data, deviceInfoFileMode); err != nil { //nolint:gosec // device info is world-readable by design
		return fmt.Errorf("writing device info file %q: %w", path, err)
	}
	return nil
}

// removeDeviceInfo deletes the CNIDeviceInfoFile (best effort).
func removeDeviceInfo(conf *NetConf) error {
	path := conf.RuntimeConfig.CNIDeviceInfoFile
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing device info file %q: %w", path, err)
	}
	return nil
}
