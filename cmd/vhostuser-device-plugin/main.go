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

// Command vhostuser-device-plugin is the node-local Kubernetes device plugin
// that hands vhost-user socket rendezvous points to workloads, so a KubeVirt VM
// (or any DPDK workload) can be wired to a CRA fast path -- 6WIND vSR fpvhost
// or grout net_vhost -- by the cni-workload plugin.
//
// It replaces the proprietary 6WIND HNA device plugin with a configurable one:
// every path (both socket trees, the kubelet socket directory, the device-info
// directory) is a flag, so the same binary serves a vSR node, a grout node and
// an e2e lab. See docs/proposals/03-vhost-user/DEVICE-PLUGIN.md for the contract
// it implements and pkg/deviceplugin for the implementation.
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"strings"
	"syscall"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/telekom/das-schiff-network-operator/pkg/deviceplugin"
)

func main() {
	// run() rather than inlining, so the signal-context teardown actually runs:
	// os.Exit skips defers.
	os.Exit(run())
}

func run() int {
	cfg := deviceplugin.DefaultConfig()
	var resources string

	flag.StringVar(&cfg.ResourceDomain, "resource-domain", cfg.ResourceDomain,
		"device-plugin resource domain to advertise under (<domain>/vhost-user, <domain>/virtio-user)")
	flag.StringVar(&resources, "resources",
		deviceplugin.ResourceVhostUser+","+deviceplugin.ResourceVirtioUser,
		"comma-separated resources to advertise (vhost-user, virtio-user)")
	flag.StringVar(&cfg.VhostUserDir, "vhost-user-dir", cfg.VhostUserDir,
		"host directory in which the CRA fast path owns (listens on) the vhost-user socket")
	flag.StringVar(&cfg.VirtioUserDir, "virtio-user-dir", cfg.VirtioUserDir,
		"host directory in which the workload owns (listens on) the vhost-user socket")
	flag.StringVar(&cfg.KubeletSocketDir, "kubelet-socket-dir", cfg.KubeletSocketDir,
		"kubelet device-plugin socket directory")
	flag.StringVar(&cfg.DeviceInfoDir, "device-info-dir", cfg.DeviceInfoDir,
		"directory the k8snetworkplumbingwg device-info files are written to")
	flag.IntVar(&cfg.DeviceCount, "device-count", cfg.DeviceCount,
		"number of sockets advertised per resource (the per-node cap on concurrent attachments)")
	flag.IntVar(&cfg.SocketOwnerUID, "socket-owner-uid", cfg.SocketOwnerUID,
		"uid owning each per-device socket directory (107 = qemu in the KubeVirt launcher)")
	flag.IntVar(&cfg.SocketOwnerGID, "socket-owner-gid", cfg.SocketOwnerGID,
		"gid owning each per-device socket directory")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("vhostuser-device-plugin")

	for _, r := range strings.Split(resources, ",") {
		if r = strings.TrimSpace(r); r != "" {
			cfg.Resources = append(cfg.Resources, r)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := deviceplugin.Run(ctx, &cfg, log); err != nil {
		log.Error(err, "device plugin terminated")
		return 1
	}
	return 0
}
