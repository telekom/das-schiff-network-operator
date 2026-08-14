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

package deviceplugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

// deviceIDLen is the length of a device id in hex characters. 10 matches the
// 6WIND plugin, which keeps the derived directory names comparably short.
const deviceIDLen = 10

// deviceIDBytes is how many random bytes yield deviceIDLen hex characters.
const deviceIDBytes = (deviceIDLen + 1) / 2

// Plugin serves one resource (vhost-user or virtio-user) to kubelet.
//
// Device ids are generated ONCE at startup and are deliberately ephemeral: they
// are rendezvous handles, not hardware identities, and must never be persisted
// in a CR or cached across a restart. Restarting the plugin regenerates them,
// and kubelet re-Allocates for every pod that comes up afterwards.
type Plugin struct {
	pluginapi.UnimplementedDevicePluginServer

	cfg      *Config
	resource string
	// resourceName is the fully-qualified <domain>/<resource>.
	resourceName string
	// hostTree/podTree are the crossed socket trees for this resource.
	hostTree string
	podTree  string

	log logr.Logger

	devices []*pluginapi.Device
	// socketPath is this plugin's own gRPC endpoint in the kubelet directory.
	socketPath string

	// srvMu guards server, which Serve writes and Stop reads -- and Stop is
	// called both from Serve's own error paths and from another goroutine.
	srvMu  sync.Mutex
	server *grpc.Server

	// mu serialises Allocate.
	mu sync.Mutex
	// stop is closed on Stop and unblocks ListAndWatch. stopOnce guards the
	// close: Stop is reached from Serve's own error paths, from its context
	// cancellation and from the run loop, and two of them racing on a bare
	// "check then close" would close an already-closed channel and panic the
	// DaemonSet.
	stop     chan struct{}
	stopOnce sync.Once
}

// NewPlugin builds the plugin for one resource.
func NewPlugin(cfg *Config, resource string, log logr.Logger) *Plugin {
	hostTree, podTree := cfg.socketTrees(resource)
	p := &Plugin{
		cfg:          cfg,
		resource:     resource,
		resourceName: cfg.ResourceName(resource),
		hostTree:     hostTree,
		podTree:      podTree,
		log:          log.WithValues("resource", cfg.ResourceName(resource)),
		socketPath:   filepath.Join(cfg.KubeletSocketDir, "dsno-"+resource+".sock"),
		stop:         make(chan struct{}),
	}
	p.devices = make([]*pluginapi.Device, 0, cfg.DeviceCount)
	for range cfg.DeviceCount {
		p.devices = append(p.devices, &pluginapi.Device{ID: newDeviceID(), Health: pluginapi.Healthy})
	}
	return p
}

// newDeviceID returns a random device handle. crypto/rand is used not for
// secrecy but because a collision would silently make two pods share a socket
// directory, and it is the only non-seeded source available without pulling in
// global math/rand state.
//
// The ids are deliberately fresh on every start rather than derived from the
// device index, so a restarted plugin never hands out an id whose directory a
// still-running pod is using. The cost is that the previous generation's
// directories stay behind: they are empty of anything the fast path still
// references (the port is pruned when the pod goes away), but nothing deletes
// them, so a node that restarts the plugin many times accumulates empty
// directories under the socket tree. Deriving the ids from the index instead
// would fix that at the price of reusing a directory that may still hold a
// dead pod's socket, which is the worse failure: the new pod would find a
// socket that looks connectable and is attached to nothing.
func newDeviceID() string {
	b := make([]byte, deviceIDBytes)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand.Read never fails on the platforms we run on; falling back
		// to a time-derived id keeps the plugin serving rather than crashing a
		// node-critical DaemonSet.
		return strconv.FormatInt(time.Now().UnixNano(), 16)[:deviceIDLen]
	}
	return hex.EncodeToString(b)[:deviceIDLen]
}

// Serve starts the plugin's gRPC server and registers it with kubelet. It
// blocks until ctx is cancelled or the server stops.
func (p *Plugin) Serve(ctx context.Context) error {
	if err := os.MkdirAll(p.cfg.KubeletSocketDir, hostDirMode); err != nil {
		return fmt.Errorf("creating kubelet socket dir %s: %w", p.cfg.KubeletSocketDir, err)
	}
	// A leftover socket from a previous run makes Listen fail with EADDRINUSE
	// even though nothing is listening on it.
	if err := removeStaleSocket(p.socketPath); err != nil {
		return err
	}

	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "unix", p.socketPath)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", p.socketPath, err)
	}

	server := grpc.NewServer()
	pluginapi.RegisterDevicePluginServer(server, p)
	p.srvMu.Lock()
	p.server = server
	p.srvMu.Unlock()

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(lis) }()

	// kubelet refuses a registration whose socket it cannot dial, so wait for
	// our own server to accept a connection before registering.
	if err := p.waitReady(ctx); err != nil {
		p.Stop()
		return err
	}
	if err := p.register(ctx); err != nil {
		p.Stop()
		return err
	}
	p.log.Info("device plugin registered", "socket", p.socketPath,
		"devices", len(p.devices), "hostTree", p.hostTree, "podTree", p.podTree)

	select {
	case <-ctx.Done():
		p.Stop()
		return nil
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("device plugin server: %w", err)
		}
		return nil
	}
}

// Stop shuts the gRPC server down and removes the registration socket.
func (p *Plugin) Stop() {
	p.stopOnce.Do(func() { close(p.stop) })
	p.srvMu.Lock()
	server := p.server
	p.server = nil
	p.srvMu.Unlock()
	if server != nil {
		server.GracefulStop()
	}
	_ = os.Remove(p.socketPath)
}

// removeStaleSocket removes a leftover endpoint, refusing to unlink anything
// that is not a socket so a misconfigured path cannot delete real files.
func removeStaleSocket(path string) error {
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to remove %s: not a socket", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("removing stale socket %s: %w", path, err)
	}
	return nil
}

func (p *Plugin) waitReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, registerTimeout)
	defer cancel()
	conn, err := dial(ctx, p.socketPath)
	if err != nil {
		return err
	}
	return conn.Close() //nolint:wrapcheck // closing a just-dialled probe connection
}

// register tells kubelet about the endpoint and the resource it serves.
func (p *Plugin) register(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, registerTimeout)
	defer cancel()

	conn, err := dial(ctx, filepath.Join(p.cfg.KubeletSocketDir, kubeletSocketName))
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := pluginapi.NewRegistrationClient(conn).Register(ctx, &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     filepath.Base(p.socketPath),
		ResourceName: p.resourceName,
	}); err != nil {
		return fmt.Errorf("registering %s with kubelet: %w", p.resourceName, err)
	}
	return nil
}

func dial(ctx context.Context, socketPath string) (*grpc.ClientConn, error) {
	conn, err := grpc.NewClient("unix://"+socketPath, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dialing %s: %w", socketPath, err)
	}
	conn.Connect()
	for {
		state := conn.GetState()
		if state.String() == "READY" {
			return conn, nil
		}
		if !conn.WaitForStateChange(ctx, state) {
			conn.Close()
			return nil, fmt.Errorf("dialing %s: %w", socketPath, ctx.Err())
		}
	}
}

// GetDevicePluginOptions reports that we need neither PreStartContainer nor a
// preferred-allocation hook: the devices are interchangeable directories.
func (*Plugin) GetDevicePluginOptions(context.Context, *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	return &pluginapi.DevicePluginOptions{}, nil
}

// ListAndWatch streams the device list. The devices are directories we create
// on demand, so they never become unhealthy and the list is sent once and then
// held open until shutdown -- kubelet treats the stream ending as the plugin
// going away.
func (p *Plugin) ListAndWatch(_ *pluginapi.Empty, srv pluginapi.DevicePlugin_ListAndWatchServer) error {
	if err := srv.Send(&pluginapi.ListAndWatchResponse{Devices: p.devices}); err != nil {
		return fmt.Errorf("sending device list: %w", err)
	}
	select {
	case <-p.stop:
	case <-srv.Context().Done():
	}
	return nil
}

// Allocate creates one socket directory per requested device and returns the
// crossed bind mount, the device-id environment variable and the device-info
// file for each.
//
// The pod-side ordinal restarts at 0 for every Allocate call, which is what
// makes a pod requesting a single socket always see it at <podTree>/0/socket.
func (p *Plugin) Allocate(_ context.Context, req *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	resp := &pluginapi.AllocateResponse{}
	for _, creq := range req.GetContainerRequests() {
		cresp, err := p.allocateContainer(creq.GetDevicesIds())
		if err != nil {
			return nil, err
		}
		resp.ContainerResponses = append(resp.ContainerResponses, cresp)
	}
	return resp, nil
}

func (p *Plugin) allocateContainer(deviceIDs []string) (*pluginapi.ContainerAllocateResponse, error) {
	// The ordinal is per-container, so serialise only to keep the log and the
	// directory creation from interleaving between concurrent Allocates.
	p.mu.Lock()
	defer p.mu.Unlock()

	resp := &pluginapi.ContainerAllocateResponse{
		Envs:   map[string]string{},
		Mounts: make([]*pluginapi.Mount, 0, len(deviceIDs)),
	}

	for index, deviceID := range deviceIDs {
		if err := validateDeviceID(deviceID); err != nil {
			return nil, err
		}
		hostPath := filepath.Join(p.hostTree, deviceID)
		podPath := filepath.Join(p.podTree, strconv.Itoa(index))

		if err := p.makeSocketDir(hostPath); err != nil {
			return nil, err
		}

		resp.Mounts = append(resp.Mounts, &pluginapi.Mount{
			HostPath:      hostPath,
			ContainerPath: podPath,
			ReadOnly:      false,
		})

		// The device-info file carries the POD-side path: it is consumed by the
		// KubeVirt hook and ends up in the domain XML, which is resolved in the
		// guest launcher's mount namespace. Writing the host path here yields a
		// domain pointing at a socket the VM cannot open.
		if err := p.writeDeviceInfo(deviceID, filepath.Join(podPath, SocketFile)); err != nil {
			return nil, err
		}

		p.log.Info("allocated vhost-user socket directory",
			"deviceID", deviceID, "hostPath", hostPath, "podPath", podPath)
	}

	resp.Envs[deviceEnvVar(p.resource)] = strings.Join(deviceIDs, ",")
	return resp, nil
}

// validateDeviceID rejects anything that could escape the socket tree. kubelet
// only ever echoes ids we advertised, but the id is concatenated into a
// filesystem path, so it is checked rather than trusted.
func validateDeviceID(deviceID string) error {
	if deviceID == "" {
		return fmt.Errorf("empty device id")
	}
	if strings.ContainsAny(deviceID, "/\\") || strings.Contains(deviceID, "..") {
		return fmt.Errorf("invalid device id %q", deviceID)
	}
	return nil
}

// makeSocketDir creates the per-device directory and gives it to the socket
// owner. A workload that cannot traverse or write the directory cannot create
// its end of the socket, so ownership is part of the contract rather than a
// nicety.
func (p *Plugin) makeSocketDir(path string) error {
	if err := os.MkdirAll(path, dirMode); err != nil {
		return fmt.Errorf("creating socket dir %s: %w", path, err)
	}
	// MkdirAll honours the umask, so re-assert the mode explicitly.
	if err := os.Chmod(path, dirMode); err != nil {
		return fmt.Errorf("setting mode on %s: %w", path, err)
	}
	if err := os.Chown(path, p.cfg.SocketOwnerUID, p.cfg.SocketOwnerGID); err != nil {
		return fmt.Errorf("chowning %s to %d:%d: %w", path, p.cfg.SocketOwnerUID, p.cfg.SocketOwnerGID, err)
	}
	return nil
}

// deviceInfo is the k8snetworkplumbingwg device-info document.
type deviceInfo struct {
	Type      string         `json:"type"`
	Version   string         `json:"version"`
	VhostUser *vhostUserInfo `json:"vhost-user"`
}

type vhostUserInfo struct {
	Mode string `json:"mode"`
	Path string `json:"path"`
}

// writeDeviceInfo publishes the plugin's own statement of what it allocated.
// Multus copies this file to the CNI's device-info path before invoking the
// plugin, and the workload CNI treats it as authoritative for the pod-side path
// and the socket mode.
func (p *Plugin) writeDeviceInfo(deviceID, podSocketPath string) error {
	if err := os.MkdirAll(p.cfg.DeviceInfoDir, hostDirMode); err != nil {
		return fmt.Errorf("creating device-info dir %s: %w", p.cfg.DeviceInfoDir, err)
	}
	info := deviceInfo{
		Type:    "vhost-user",
		Version: "1.1.0",
		VhostUser: &vhostUserInfo{
			// The mode is stated from the WORKLOAD's perspective, which is what
			// the resource name already encodes: the holder of the vhost-user
			// resource owns (serves) the socket, the holder of virtio-user
			// connects to it. The CRA renderers invert it for the fast path.
			Mode: p.workloadSocketMode(),
			Path: podSocketPath,
		},
	}
	data, err := json.Marshal(&info)
	if err != nil {
		return fmt.Errorf("marshalling device info: %w", err)
	}
	path := filepath.Join(p.cfg.DeviceInfoDir, deviceInfoFileName(p.resourceName, deviceID))
	if err := os.WriteFile(path, data, deviceInfoMode); err != nil { //nolint:gosec // read by Multus as another user
		return fmt.Errorf("writing device info %s: %w", path, err)
	}
	return nil
}

// workloadSocketMode returns the socket mode from the workload's perspective.
func (p *Plugin) workloadSocketMode() string {
	if p.resource == ResourceVhostUser {
		return "server"
	}
	return "client"
}
