package kubevirthook

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

const (
	// DefaultNetworkInfoPath is where KubeVirt projects the device-info
	// downward API into a binding-plugin sidecar. Both the directory and the
	// file name are fixed by KubeVirt (downwardapi.MountPath and
	// downwardapi.NetworkInfoVolumePath).
	DefaultNetworkInfoPath = "/etc/podinfo/network-info"

	// networkInfoPollInterval is how often the file is re-read while waiting
	// for it to name every expected interface.
	networkInfoPollInterval = 200 * time.Millisecond
)

// readNetworkInfo parses the downward-API file. A missing or empty file is
// reported as a nil NetworkInfo rather than an error: both are the normal state
// before the projection has caught up.
func readNetworkInfo(path string) (*NetworkInfo, error) {
	data, err := os.ReadFile(path) //nolint:gosec // an operator-fixed path, not user input
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil //nolint:nilnil // "not there yet" is not a failure
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(data) == 0 {
		return nil, nil //nolint:nilnil // same, mid-projection
	}

	var info NetworkInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &info, nil
}

// vhostUserAttachment returns the vhost-user socket the device-info names for a
// VMI interface, or nil when the file carries no usable entry for it (yet).
//
// An entry with a device-info of another type is not an error either: a VMI can
// mix an SR-IOV interface, whose entry says "pci", with a vhost-user one.
func (n *NetworkInfo) vhostUserAttachment(name string) *VhostDevice {
	if n == nil {
		return nil
	}
	for i := range n.Interfaces {
		iface := &n.Interfaces[i]
		if iface.Network != name || iface.DeviceInfo == nil {
			continue
		}
		if iface.DeviceInfo.Type != DeviceInfoTypeVhostUser {
			continue
		}
		if iface.DeviceInfo.VhostUser == nil || iface.DeviceInfo.VhostUser.Path == "" {
			continue
		}
		return iface.DeviceInfo.VhostUser
	}
	return nil
}

// WaitForNetworkInfo polls the downward-API file until it names a vhost-user
// socket for every wanted interface, or the context is done.
//
// Polling is not defensive programming, it is required. KubeVirt writes the
// kubevirt.io/network-info annotation from the VMI controller only once it sees
// the virt-launcher pod Ready, and kubelet then refreshes the projected volume
// asynchronously; nothing sequences either against this hook being called. The
// alternative to waiting is booting a VM whose NIC is connected to nothing.
//
// The file is re-read (not just re-stat'ed) on every attempt because a
// projected volume is updated by swapping the directory symlink underneath.
func WaitForNetworkInfo(ctx context.Context, path string, wanted []string) (*NetworkInfo, error) {
	ticker := time.NewTicker(networkInfoPollInterval)
	defer ticker.Stop()

	var lastErr error
	for {
		info, err := readNetworkInfo(path)
		if err != nil {
			// Keep polling: a half-written projection parses as malformed.
			lastErr = err
		} else if missing := missingAttachments(info, wanted); len(missing) == 0 {
			return info, nil
		} else {
			lastErr = fmt.Errorf("%s names no vhost-user socket for %v", path, missing)
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for device-info: %w (last: %w)", ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func missingAttachments(info *NetworkInfo, wanted []string) []string {
	var missing []string
	for _, name := range wanted {
		if info.vhostUserAttachment(name) == nil {
			missing = append(missing, name)
		}
	}
	return missing
}
