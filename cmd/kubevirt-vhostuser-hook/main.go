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

// Command kubevirt-vhostuser-hook is the KubeVirt network binding plugin
// sidecar that attaches a VM to a CRA fast path over vhost-user.
//
// It is registered once, cluster-wide, on the KubeVirt CR:
//
//	spec:
//	  configuration:
//	    developerConfiguration:
//	      featureGates: ["NetworkBindingPlugins"]
//	    network:
//	      binding:
//	        vhostuser:
//	          sidecarImage: <this image>
//	          downwardAPI: device-info
//
// after which a VM attaches with nothing but
//
//	interfaces:
//	  - name: fastnet
//	    binding:
//	      name: vhostuser
//
// The plugin is deliberately registered with no domainAttachmentType: that is
// what makes KubeVirt skip the pod-link discovery a vhost-user socket can never
// satisfy, and it leaves this sidecar owning the whole <interface> element.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"google.golang.org/grpc"

	"github.com/telekom/das-schiff-network-operator/pkg/kubevirthook"
)

// hookSocketDir is the directory virt-launcher scans for hook sidecars. It is
// fixed by KubeVirt (hooks.HookSocketsSharedDirectory) and shared with the
// compute container through an emptyDir.
const hookSocketDir = "/var/run/kubevirt-hooks"

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "kubevirt-vhostuser-hook: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		bindingName     = flag.String("binding-name", kubevirthook.HookName, "name the binding plugin is registered under in the KubeVirt CR")
		networkInfoPath = flag.String("network-info", kubevirthook.DefaultNetworkInfoPath, "path of the device-info downward-API file")
		socketDir       = flag.String("socket-dir", hookSocketDir, "directory to serve the hook socket in")
	)
	flag.Parse()

	logf := func(format string, args ...any) {
		_, _ = fmt.Fprintf(os.Stderr, "kubevirt-vhostuser-hook: "+format+"\n", args...)
	}

	hook := &kubevirthook.Hook{
		BindingName:     *bindingName,
		NetworkInfoPath: *networkInfoPath,
		Logf:            logf,
	}

	// The socket name is arbitrary -- virt-launcher dials every entry of the
	// directory -- but it has to be unique, because the directory is shared
	// with any other sidecar the VMI uses.
	socketPath := filepath.Join(*socketDir, kubevirthook.HookName+".sock")
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing stale socket %s: %w", socketPath, err)
	}
	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "unix", socketPath)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", socketPath, err)
	}
	defer listener.Close()

	server := kubevirthook.NewServer(hook)
	grpcServer := grpc.NewServer()
	server.Register(grpcServer)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		select {
		case <-ctx.Done():
		case <-server.Stopped():
		}
		grpcServer.GracefulStop()
	}()

	logf("serving %s on %s (binding %q, device-info %s)",
		kubevirthook.HookName, socketPath, *bindingName, *networkInfoPath)
	if err := grpcServer.Serve(listener); err != nil {
		return fmt.Errorf("serving: %w", err)
	}
	return nil
}
