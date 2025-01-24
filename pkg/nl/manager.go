package nl

import (
	"fmt"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"log"
	"net"
)

const (
	vrfTableStart = 50
	vrfTableEnd   = 80

	bridgePrefix   = "br."
	vxlanPrefix    = "vx."
	layer2SVI      = "l2."
	vlanPrefix     = "vlan."
	loopbackPrefix = "lo."

	underlayInterfaceName = "dum.underlay"

	vxlanPort  = 4789
	defaultMtu = 9000
)

var macPrefix = []byte("\x02\x54")

type Manager struct {
	toolkit    ToolkitInterface
	baseConfig config.BaseConfig
}

func NewManager(toolkit ToolkitInterface, baseConfig config.BaseConfig) *Manager {
	return &Manager{toolkit: toolkit, baseConfig: baseConfig}
}

func (n *Manager) GetUnderlayIP() (net.IP, error) {
	_, ip, err := n.getUnderlayInterfaceAndIP()
	return ip, err
}

func (n *Manager) ReconcileNetlinkConfiguration(config NetlinkConfiguration) error {
	if err := n.reconcileLayer3(config); err != nil {
		return fmt.Errorf("error reconciling L3: %w", err)
	}
	if err := n.reconcileLayer2(config); err != nil {
		return fmt.Errorf("error reconciling L2: %w", err)
	}
	return nil
}

func (n *Manager) reconcileLayer2(config NetlinkConfiguration) error {
	existing, err := n.ListL2()
	if err != nil {
		return fmt.Errorf("error listing L2: %w", err)
	}

	var toCreate []Layer2Information
	var toDelete []Layer2Information

	for i := range config.Layer2s {
		alreadyExists := false
		for j := range existing {
			if existing[j].VlanID == config.Layer2s[i].VlanID {
				alreadyExists = true
				break
			}
		}
		if !alreadyExists {
			toCreate = append(toCreate, config.Layer2s[i])
		}
	}

	for i := range existing {
		needsDeletion := true
		for j := range config.Layer2s {
			if existing[i].VlanID == config.Layer2s[j].VlanID {
				needsDeletion = false
				break
			}
		}
		if needsDeletion {
			toDelete = append(toDelete, existing[i])
		} else {
			if err := n.ReconcileL2(&config.Layer2s[i], &existing[i]); err != nil {
				return fmt.Errorf("error reconciling L2 (VLAN: %d): %w", config.Layer2s[i].VlanID, err)
			}
		}
	}

	for i := range toDelete {
		if err := n.CleanupL2(&toDelete[i]); len(err) > 0 {
			return fmt.Errorf("error deleting L2 (VLAN: %d): %v", toDelete[i].VlanID, err)
		}
	}

	for i := range toCreate {
		if err := n.CreateL2(&toCreate[i]); err != nil {
			return fmt.Errorf("error creating L2 (VLAN: %d): %w", toCreate[i].VlanID, err)
		}
	}

	return nil
}

func (n *Manager) reconcileLayer3(config NetlinkConfiguration) error {
	existing, err := n.ListL3()
	if err != nil {
		return fmt.Errorf("error listing L3 VRF information: %w", err)
	}

	var toCreate []VRFInformation
	var toDelete []VRFInformation

	for i := range existing {
		needsDeletion := true
		for j := range config.VRFs {
			if config.VRFs[j].Name == existing[i].Name && config.VRFs[j].VNI == existing[i].VNI {
				needsDeletion = false
				break
			}
		}
		if needsDeletion || existing[i].markForDelete {
			toDelete = append(toDelete, existing[i])
		}
	}

	for i := range config.VRFs {
		alreadyExists := false
		for j := range existing {
			if existing[j].Name == config.VRFs[i].Name && existing[j].VNI == config.VRFs[i].VNI && !existing[j].markForDelete {
				alreadyExists = true
				break
			}
		}
		if !alreadyExists {
			toCreate = append(toCreate, config.VRFs[i])
		}
	}

	for i := range toDelete {
		errors := n.CleanupL3(toDelete[i].Name)
		if len(errors) > 0 {
			return fmt.Errorf("error cleaning up L3 (VRF: %s): %v", toDelete[i].Name, errors)
		}
	}

	for i := range toCreate {
		log.Println("Creating VRF", toCreate[i].Name)
		if err := n.CreateL3(toCreate[i]); err != nil {
			return fmt.Errorf("error creating L3 (VRF: %s): %w", toCreate[i].Name, err)
		}
		if err := n.UpL3(toCreate[i]); err != nil {
			return fmt.Errorf("error setting up L3 (VRF: %s): %w", existing[i].Name, err)
		}
	}

	return nil
}
