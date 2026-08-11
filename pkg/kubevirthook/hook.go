// Package kubevirthook implements the vhost-user KubeVirt network binding
// plugin: the onDefineDomain hook that turns a VM interface into an
// <interface type='vhostuser'> pointed at the socket our device plugin
// allocated.
//
// On the three things KubeVirt calls a "sidecar", because the choice is easy to
// get wrong:
//
//   - The legacy annotation hook (hooks.kubevirt.io/hookSidecars) is the
//     deprecated mechanism, and is NOT used here. It would also mean restating
//     on every VM something the device plugin already decided.
//   - A network binding plugin, which is what this is, is registered
//     cluster-wide in the KubeVirt CR under configuration.network.binding and
//     implemented by a sidecar image. Two properties of that registration are
//     load-bearing: KubeVirt then generates no <interface> element of its own
//     (its own bindings all terminate in a kernel netdev, which a vhost-user
//     socket is not), and downwardAPI: device-info projects the socket Multus
//     reported into this process, so the VM has to name only the binding.
//   - The Plugin CR (plugin.kubevirt.io/v1alpha1) added in KubeVirt v1.9 is a
//     newer, more general extension point. It is Alpha, gated, and newer than
//     the version this repo deploys, so it is future work rather than an
//     alternative today -- and it would not remove this code: its CEL hooks
//     cannot read the device-info file, so a case like this one stays a
//     sidecar hook there too, wrapped in a Plugin CR instead of a binding
//     registration.
package kubevirthook

import (
	"context"
	"encoding/json"
	"fmt"
)

// Hook converts the domain of a VMI whose interfaces use the vhost-user network
// binding plugin.
type Hook struct {
	// BindingName is the name the plugin is registered under in the KubeVirt
	// CR (configuration.network.binding.<name>). It selects which of the VMI's
	// interfaces this hook owns.
	//
	// It has to be configured rather than discovered: KubeVirt injects a
	// binding sidecar with no arguments and no environment naming the plugin
	// it was injected for, so the image is what has to state it.
	BindingName string

	// NetworkInfoPath is the device-info downward-API file.
	NetworkInfoPath string

	// Logf receives progress and diagnostics.
	Logf func(format string, args ...any)
}

func (h *Hook) logf(format string, args ...any) {
	if h.Logf != nil {
		h.Logf(format, args...)
	}
}

// OnDefineDomain returns the domain XML with every vhost-user interface of the
// VMI rendered into it.
//
// Every failure is returned, never swallowed: virt-launcher aborts the VM on a
// hook error, which is the outcome to want here. A domain that silently comes
// back unconverted is a VM that boots, looks healthy, and is attached to
// nothing.
func (h *Hook) OnDefineDomain(ctx context.Context, domainXML, vmiJSON []byte) ([]byte, error) {
	var vmi VMI
	if err := json.Unmarshal(vmiJSON, &vmi); err != nil {
		return nil, fmt.Errorf("parsing VMI: %w", err)
	}

	owned := h.ownedInterfaces(&vmi)
	if len(owned) == 0 {
		// Nothing to do, but say so: the sidecar is injected per binding
		// plugin, so being called with no matching interface means the plugin
		// is registered under a different name than this image was built for.
		h.logf("VMI %s/%s has no interface using binding plugin %q; returning the domain unchanged",
			vmi.Metadata.Namespace, vmi.Metadata.Name, h.BindingName)
		return domainXML, nil
	}

	names := make([]string, 0, len(owned))
	for _, iface := range owned {
		names = append(names, iface.Name)
	}
	h.logf("waiting for device-info for %v", names)

	info, err := WaitForNetworkInfo(ctx, h.NetworkInfoPath, names)
	if err != nil {
		return nil, err
	}

	attachments := make([]Attachment, 0, len(owned))
	for _, iface := range owned {
		dev := info.vhostUserAttachment(iface.Name)
		if err := validateMode(dev.Mode); err != nil {
			return nil, fmt.Errorf("interface %s: %w", iface.Name, err)
		}
		attachments = append(attachments, Attachment{
			Name:       iface.Name,
			SocketPath: dev.Path,
			Mode:       dev.Mode,
			MAC:        iface.MacAddress,
		})
		h.logf("interface %s: vhost-user socket %s mode=%s", iface.Name, dev.Path, dev.Mode)
	}

	return Convert(domainXML, attachments)
}

// ownedInterfaces returns the VMI interfaces bound to this hook's plugin.
func (h *Hook) ownedInterfaces(vmi *VMI) []VMIInterface {
	var owned []VMIInterface
	for _, iface := range vmi.Spec.Domain.Devices.Interfaces {
		if iface.Binding != nil && iface.Binding.Name == h.BindingName {
			owned = append(owned, iface)
		}
	}
	return owned
}

func validateMode(mode string) error {
	switch mode {
	case ModeClient, ModeServer:
		return nil
	case "":
		return fmt.Errorf("device-info states no socket mode")
	default:
		return fmt.Errorf("device-info states socket mode %q, want %q or %q",
			mode, ModeClient, ModeServer)
	}
}
