package netplan

import (
	"fmt"
	"strings"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/sirupsen/logrus"
	"github.com/telekom/das-schiff-network-operator/pkg/helpers/diff"
	"github.com/telekom/das-schiff-network-operator/pkg/helpers/maps"
	"github.com/telekom/das-schiff-network-operator/pkg/helpers/merge"
	"github.com/telekom/das-schiff-network-operator/pkg/network/net"
	"sigs.k8s.io/yaml"
)

type RawDevice []byte

// +kubebuilder:object:generate=true
// +kubebuilder:validation:XPreserveUnknownFields
type Device struct {
	Raw RawDevice `json:"-"`
}

// +kubebuilder:object:generate=true
type State struct {
	Network NetworkState `json:"network"`
}

// +kubebuilder:object:generate=true
type NetworkState struct {
	Version   int               `json:"version,omitempty"`
	Ethernets map[string]Device `json:"ethernets,omitempty"`
	Modems    map[string]Device `json:"modems,omitempty"`
	Wifis     map[string]Device `json:"wifis,omitempty"`
	Bridges   map[string]Device `json:"bridges,omitempty"`
	Bonds     map[string]Device `json:"bonds,omitempty"`
	Tunnels   map[string]Device `json:"tunnels,omitempty"`
	VLans     map[string]Device `json:"vlans,omitempty"`
	VRFs      map[string]Device `json:"vrfs,omitempty"`
}

func NewEmptyState() State {
	return State{
		Network: NetworkState{
			Ethernets: make(map[string]Device),
			Modems:    make(map[string]Device),
			Wifis:     make(map[string]Device),
			Bridges:   make(map[string]Device),
			Bonds:     make(map[string]Device),
			Tunnels:   make(map[string]Device),
			VLans:     make(map[string]Device),
			VRFs:      make(map[string]Device),
			Version:   2,
		},
	}
}
func GetInterfaceTypeStatePath(t net.InterfaceType) string {
	return fmt.Sprintf("network.%ss", t.String())
}

type StateDeviceIteratorItem struct {
	Name   string
	Device Device
	Type   net.InterfaceType
}
type StateDeviceIterator struct {
	items       []StateDeviceIteratorItem
	state       *State
	currentItem int
}

func (s State) IsEmpty() bool {
	return cmp.Equal(s, NewEmptyState(), cmpopts.EquateEmpty())
}
func (s State) DeviceIterator() StateDeviceIterator {
	items := make([]StateDeviceIteratorItem, 0)
	add := func(t net.InterfaceType, n string, d Device) {
		items = append(items, StateDeviceIteratorItem{Type: t, Name: n, Device: d})
	}

	maps.ForEach(s.Network.Ethernets, func(name string, device Device) { add(net.InterfaceTypeEthernet, name, device) })
	maps.ForEach(s.Network.Modems, func(name string, device Device) { add(net.InterfaceTypeModem, name, device) })
	maps.ForEach(s.Network.Wifis, func(name string, device Device) { add(net.InterfaceTypeWifi, name, device) })
	maps.ForEach(s.Network.Bridges, func(name string, device Device) { add(net.InterfaceTypeBridge, name, device) })
	maps.ForEach(s.Network.Bonds, func(name string, device Device) { add(net.InterfaceTypeBond, name, device) })
	maps.ForEach(s.Network.Tunnels, func(name string, device Device) { add(net.InterfaceTypeTunnel, name, device) })
	maps.ForEach(s.Network.VLans, func(name string, device Device) { add(net.InterfaceTypeVLan, name, device) })
	maps.ForEach(s.Network.VRFs, func(name string, device Device) { add(net.InterfaceTypeVRF, name, device) })

	return StateDeviceIterator{
		state:       &s,
		items:       items,
		currentItem: -1,
	}
}
func (di *StateDeviceIterator) HasNext() bool {
	return di.currentItem+1 < len(di.items)
}
func (di *StateDeviceIterator) Next() StateDeviceIteratorItem {
	di.currentItem++
	return di.items[di.currentItem]
}
func (di *StateDeviceIterator) Apply(i StateDeviceIteratorItem) {
	switch i.Type {
	case net.InterfaceTypeEthernet:
		di.state.Network.Ethernets[i.Name] = i.Device
	case net.InterfaceTypeModem:
		di.state.Network.Modems[i.Name] = i.Device
	case net.InterfaceTypeWifi:
		di.state.Network.Wifis[i.Name] = i.Device
	case net.InterfaceTypeBridge:
		di.state.Network.Bridges[i.Name] = i.Device
	case net.InterfaceTypeBond:
		di.state.Network.Bonds[i.Name] = i.Device
	case net.InterfaceTypeTunnel:
		di.state.Network.Tunnels[i.Name] = i.Device
	case net.InterfaceTypeVLan:
		di.state.Network.VLans[i.Name] = i.Device
	case net.InterfaceTypeVRF:
		di.state.Network.VRFs[i.Name] = i.Device

	}
}

func NewState(raw string) (State, error) {
	var state State
	err := yaml.Unmarshal([]byte(raw), &state)
	return state, err
}
func (s State) Clone() State {
	result, _ := NewState(s.YAML())
	return result
}
func (s State) YAML() string {
	result, _ := yaml.Marshal(s)
	return string(result)
}

// Simple stringer for State
func (s State) String() string {
	return s.YAML()
}
func (s State) ContainsVirtualInterfaces() bool {
	return len(s.Network.Bonds) > 0 || len(s.Network.Bridges) > 0
}

func (s NetworkState) Equals(target NetworkState) bool {
	return s.Version == target.Version &&
		maps.AreEqual(s.Ethernets, target.Ethernets) &&
		maps.AreEqual(s.VLans, target.VLans) &&
		maps.AreEqual(s.Bonds, target.Bonds) &&
		maps.AreEqual(s.Bridges, target.Bridges)
}

func (s State) Equals(target State) bool {
	return s.Network.Equals(target.Network)
}
func (d *Device) Clear() {
	d.Raw = nil
}

// This overrides the State type [1] so we can do a custom marshaling of
// netplan yaml without the need to have golang code representing the
// netplan schema

// [1] https://github.com/kubernetes/kube-openapi/tree/master/pkg/generators
func (Device) OpenAPISchemaType() []string { return []string{"object"} }

// We are using behind the scenes the golang encode/json so we need to return
// json here for golang to work well, the upper yaml parser will convert it
// to yaml making nmstate yaml transparent to kubernetes-nmstate
func (d Device) MarshalJSON() (output []byte, err error) {
	return yaml.YAMLToJSON([]byte(d.Raw))
}

// Bypass State parsing and directly store it as yaml string to later on
// pass it to namestatectl using it as transparet data at kubernetes-nmstate
func (d *Device) UnmarshalJSON(b []byte) error {
	output, err := yaml.JSONToYAML(b)
	if err != nil {
		return err
	}
	var outputMap map[string]interface{}
	if err := yaml.Unmarshal(output, &outputMap); err != nil {
		return err
	} else {
		// Hack: fix for https://github.com/canonical/netplan/pull/329 (not in all versions)
		// Deduplicate call is needed because of netplan duplicating items in string arrays
		if err := maps.Deduplicate(outputMap); err != nil {
			return err
		}
		if output, err = yaml.Marshal(outputMap); err != nil {
			return err
		} else {
			*d = Device{Raw: output}
		}
	}

	return nil
}

// Simple stringer for State
func (d Device) String() string {
	return string(d.Raw)
}

// EqualsIgnoringSorting compares 2 devices, not only by Raw string but also after unmarshal, ignoring string slice order. This is a hack to not detect differences in virtual devices where the interfaces may be in different order (netplan not always outputs the same order)
func (d Device) EqualsIgnoringSorting(target Device) bool {
	if d.String() == target.String() {
		return true
	}
	// Hack: fix for unpredictable order in netplan arrays
	var dMap, tMap interface{}
	yaml.Unmarshal(d.Raw, &dMap)
	yaml.Unmarshal(target.Raw, &tMap)
	return cmp.Equal(dMap, tMap, cmpopts.SortSlices(less))
}
func less(a, b interface{}) bool {
	switch aT := a.(type) {
	case string:
		bT := b.(string)
		return aT < bT
	}
	return false
}
func SanitizeDeviceName(name string) string {
	return strings.Replace(name, ".", "\\.", -1)
}
func GetChangedVirtualInterfaces(source, target State) ([]net.Interface, error) {
	log := logrus.WithField("name", "netplan")
	result := make([]net.Interface, 0)
	compare := func(t net.InterfaceType, sourceMap, targetMap map[string]Device) []net.Interface {
		compareResult := make([]net.Interface, 0)
		for sourceKey := range sourceMap {
			if _, targetExists := targetMap[sourceKey]; !targetExists {
				log.Infof("virtual interface %s was removed", sourceKey)
				i := net.Interface{
					Type: t,
					Name: sourceKey,
				}
				compareResult = append(compareResult, i)
			}
		}
		for targetKey, targetValue := range targetMap {
			if sourceValue, sourceExists := sourceMap[targetKey]; sourceExists {
				if !sourceValue.EqualsIgnoringSorting(targetValue) {
					log.Infof("virtual interface %s changed from %s to %s", targetKey, sourceValue, targetValue)
					i := net.Interface{
						Type: t,
						Name: targetKey,
					}
					result = append(result, i)
				}
			}
		}
		return compareResult
	}
	result = append(result, compare(net.InterfaceTypeVLan, source.Network.VLans, target.Network.VLans)...)
	result = append(result, compare(net.InterfaceTypeBond, source.Network.Bonds, target.Network.Bonds)...)
	result = append(result, compare(net.InterfaceTypeBridge, source.Network.Bridges, target.Network.Bridges)...)

	return result, nil
}

func (d1 *Device) merge(d2 Device) error {
	if newRaw, err := merge.YAML([][]byte{d1.Raw, d2.Raw}, true); err != nil {
		return err
	} else {
		d1.Raw = newRaw.Bytes()
	}
	return nil
}
func (s1 *State) Merge(s2 *State) error {
	mergeDevices := func(sourceMap, targetMap map[string]Device) error {
		for key, val := range targetMap {
			if sourceVal, exist := sourceMap[key]; exist {
				if err := sourceVal.merge(val); err != nil {
					return err
				} else {
					sourceMap[key] = sourceVal
				}
			} else {
				sourceMap[key] = val
			}
		}
		return nil
	}
	var err error
	err = mergeDevices(s1.Network.Ethernets, s2.Network.Ethernets)
	if err != nil {
		return err
	}
	err = mergeDevices(s1.Network.VLans, s2.Network.VLans)
	if err != nil {
		return err
	}
	err = mergeDevices(s1.Network.Bonds, s2.Network.Bonds)
	if err != nil {
		return err
	}
	err = mergeDevices(s1.Network.Bridges, s2.Network.Bridges)
	if err != nil {
		return err
	}
	return nil
}
func (s1 *State) FindOverrides(s2 *State) (string, error) {
	if result, err := diff.FindYAMLOverrides([]byte(s1.YAML()), []byte(s2.YAML())); err != nil {
		return "", err
	} else {
		return result.String(), nil
	}
}
