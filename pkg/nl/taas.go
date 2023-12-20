package nl

import "fmt"

type TaasInformation struct {
	Name  string
	Table int
}

func (n *NetlinkManager) CreateTaas(info TaasInformation) error {
	_, err := n.createVRF(info.Name, info.Table)
	if err != nil {
		return fmt.Errorf("error creating VRF for TaaS: %w", err)
	}

	err = n.setUp(info.Name)
	if err != nil {
		return fmt.Errorf("error set VRF up for TaaS: %w", err)
	}

	return nil
}

func (n *NetlinkManager) CleanupTaas(info TaasInformation) error {
	err := n.deleteLink(info.Name)
	if err != nil {
		return fmt.Errorf("error deleting VRF for TaaS: %w", err)
	}

	return nil
}
