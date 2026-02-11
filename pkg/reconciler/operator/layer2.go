package operator

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
	corev1 "k8s.io/api/core/v1"
)

func buildNodeLayer2(node *corev1.Node, revision *v1alpha1.NetworkConfigRevision, c *v1alpha1.NodeNetworkConfig) error {
	c.Spec.Layer2s = make(map[string]v1alpha1.Layer2)

	layer2 := revision.Spec.Layer2
	sort.SliceStable(layer2, func(i, j int) bool {
		return layer2[i].ID < layer2[j].ID
	})

	for i := range layer2 {
		l2 := &layer2[i]
		if !matchSelector(node, l2.NodeSelector) {
			continue
		}

		if _, ok := c.Spec.Layer2s[fmt.Sprintf("%d", l2.ID)]; ok {
			return fmt.Errorf("duplicate Layer2 ID found: %d", l2.ID)
		}

		nodeL2 := v1alpha1.Layer2{
			VNI:  uint32(l2.VNI), //nolint:gosec
			VLAN: uint16(l2.ID),  //nolint:gosec
			MTU:  uint16(l2.MTU), //nolint:gosec
		}
		if len(l2.AnycastGateways) > 0 {
			irb := v1alpha1.IRB{
				VRF:         l2.VRF,
				IPAddresses: l2.AnycastGateways,
				MACAddress:  l2.AnycastMac,
			}
			nodeL2.IRB = &irb
		}

		c.Spec.Layer2s[fmt.Sprintf("%d", l2.ID)] = nodeL2
	}
	return nil
}
func buildNetplanVLANs(node *corev1.Node, revision *v1alpha1.NetworkConfigRevision) (map[string]netplan.Device, error) {
	vlans := make(map[string]netplan.Device)

	layer2 := revision.Spec.Layer2
	sort.SliceStable(layer2, func(i, j int) bool {
		return layer2[i].ID < layer2[j].ID
	})
	for i := range layer2 {
		l2 := &layer2[i]
		if !matchSelector(node, l2.NodeSelector) {
			continue
		}

		vlan := map[string]interface{}{
			"id":   l2.ID,
			"link": "hbn",
			"mtu":  l2.MTU,
		}

		rawVlan, err := json.Marshal(vlan)
		if err != nil {
			return nil, fmt.Errorf("error marshaling vlan: %w", err)
		}

		vlans[fmt.Sprintf("vlan.%d", l2.ID)] = netplan.Device{
			Raw: rawVlan,
		}
	}
	return vlans, nil
}

func checkL2Duplicates(configs []v1alpha1.Layer2NetworkConfiguration) error {
	for i := range configs {
		for j := i + 1; j < len(configs); j++ {
			if configs[i].Spec.VNI == configs[j].Spec.VNI {
				return fmt.Errorf("dupliate Layer2 VNI found: %s %s", configs[i].ObjectMeta.Name, configs[j].ObjectMeta.Name)
			}
		}
	}
	return nil
}
