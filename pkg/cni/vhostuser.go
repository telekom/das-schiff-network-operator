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
	"errors"
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
// the other end as an fpvhost virtual-port.
//
// The socket itself is neither created nor owned by us: the 6WIND HNA device
// plugin allocates it and bind-mounts it into the pod, and vSR binds or connects
// to it. This plugin's job for vhost-user is therefore limited to:
//   - resolving the allocation (see deviceplugin.go) into a host-side and a
//     pod-side socket path,
//   - publishing the pod-side path in a CNIDeviceInfoFile so the downstream
//     KubeVirt hook can attach the vhost-user device to the domain,
//   - handing the host-side path (+ mode + routed/L2 intent) to the node-local
//     agent, which renders the VSR fpvhost virtual-port via NETCONF.
//
// See docs/proposals/03-vhost-user/DEVICE-PLUGIN.md for the device-plugin
// contract this implements.

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
	// Resolve the device-plugin allocation first: without a socket there is
	// nothing to attach, and failing before IPAM keeps the error unambiguous.
	att, err := resolveVhostUser(conf)
	if err != nil {
		return err
	}

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
	port := vhostPortName(args.ContainerID, args.IfName, len(conf.Layer2Trunk) > 0)

	// Publish the POD-side path so the KubeVirt hook can attach the socket: the
	// host path does not resolve inside the guest's mount namespace.
	if err := writeDeviceInfo(conf, att); err != nil {
		return err
	}

	// Hand the attachment to the node-local agent (VSR renders fpvhost) with the
	// HOST-side path, which is the one vSR itself must open. The request carries
	// transport=vhostuser, so the agent creates an fpvhost virtual-port rather
	// than a veth port.
	if err := notifyAgentAdd(conf, args, port, gwV4, gwV6, result, att); err != nil {
		_ = removeDeviceInfo(conf)
		return err
	}

	result.Interfaces = []*current.Interface{{Name: args.IfName, Sandbox: args.Netns}}
	for i := range result.IPs {
		idx := 0
		result.IPs[i].Interface = &idx
	}

	// Only commit once the result has been handed back to the runtime: if
	// printing fails the ADD has failed, so the deferred rollbacks must run.
	if err := types.PrintResult(result, conf.CNIVersion); err != nil {
		return fmt.Errorf("printing CNI result: %w", err)
	}
	success = true
	return nil
}

// cmdDelVhostUser implements the CNI DEL command for the vhost-user transport.
//
// The device plugin owns the socket and its directory, so teardown is limited to
// withdrawing the fpvhost port via the agent and removing the device-info file
// we published. Like the veth DEL, every step runs even when an earlier one
// fails and the failures are aggregated: a stale NodeWorkloadPorts entry keeps
// the agent programming the fpvhost port, so the runtime must be told to retry.
func cmdDelVhostUser(conf *NetConf, args *skel.CmdArgs) error {
	var errs []error

	if len(conf.IPAM) != 0 {
		if err := ipam.ExecDel(ipamTypeOrEmpty(conf), args.StdinData); err != nil {
			errs = append(errs, fmt.Errorf("failed to release IPAM allocation: %w", err))
		}
	}

	if err := notifyAgentDel(conf, args, vhostPortName(args.ContainerID, args.IfName, len(conf.Layer2Trunk) > 0)); err != nil {
		errs = append(errs, err)
	}

	if err := removeDeviceInfo(conf); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// writeDeviceInfo publishes the vhost-user socket coordinates to the
// CNIDeviceInfoFile so downstream consumers (KubeVirt hook sidecar) can attach
// the device to the guest domain. The path written is the POD-side one: writing
// the host path here yields a domain XML pointing at a socket the VM cannot open.
func writeDeviceInfo(conf *NetConf, att *vhostUserAttachment) error {
	path := conf.RuntimeConfig.CNIDeviceInfoFile
	if path == "" {
		// No consumer requested a device info file; nothing to publish.
		return nil
	}
	info := &deviceInfo{
		Type:    deviceInfoType,
		Version: deviceInfoVersion,
		VhostUser: &vhostDeviceCfg{
			Mode: att.Mode,
			Path: att.PodPath,
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
