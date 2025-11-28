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

package cra

import (
	"fmt"
	"sort"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
)

const (
	maxVRFnameLen = 12
)

type Layer3 struct {
	nodeCfg *v1alpha1.NodeNetworkConfigSpec
	ns      *Namespace
	mgr     *Manager
	tableID map[string]int
	infos   map[string]InfoL3
}

type InfoL3 struct {
	name string
	tid  int
	vni  int
	mtu  int
}

func NewLayer3(
	nodeCfg *v1alpha1.NodeNetworkConfigSpec,
	ns *Namespace,
	mgr *Manager,
) *Layer3 {
	return &Layer3{
		nodeCfg: nodeCfg,
		mgr:     mgr,
		ns:      ns,
		tableID: map[string]int{},
		infos:   map[string]InfoL3{},
	}
}

func (l *Layer3) findFreeTableID() (int, error) {
	listTableID := make([]int, 0, len(l.tableID))

	for _, t := range l.tableID {
		listTableID = append(listTableID, t)
	}
	sort.Ints(listTableID)

	freeTableID := vrfTableStart
	for _, t := range listTableID {
		if t == freeTableID {
			freeTableID++
		}
	}

	if freeTableID >= vrfTableEnd {
		return -1, fmt.Errorf(
			"no more free tables available in range [%d-%d]",
			vrfTableStart, vrfTableEnd,
		)
	}

	return freeTableID, nil
}

func (l *Layer3) setupTableID() error {
	running := LookupNS(l.mgr.running, l.mgr.workNS)
	if running == nil {
		return fmt.Errorf("failed to found NetNS %s", l.mgr.workNS)
	}

	for _, vrf := range running.VRFs {
		l.tableID[vrf.Name] = vrf.TableID
	}

	for name := range l.tableID {
		if l.mgr.isReservedVRF(name) {
			continue
		}
		if _, ok := l.nodeCfg.FabricVRFs[name]; !ok {
			if _, ok := l.nodeCfg.LocalVRFs[name]; !ok {
				delete(l.tableID, name)
			}
		}
	}

	return nil
}

func (Layer3) makeInfo(name string, tid, vni int) InfoL3 {
	return InfoL3{
		name: name,
		tid:  tid,
		vni:  vni,
		mtu:  defaultMtu,
	}
}

func (l *Layer3) setupInformations() error {
	type FlattenVRF struct {
		name string
		vni  int
	}

	vrfs := []FlattenVRF{}
	for name := range l.nodeCfg.FabricVRFs {
		vrfs = append(vrfs, FlattenVRF{
			name: name,
			vni:  int(l.nodeCfg.FabricVRFs[name].VNI),
		})
	}
	for name := range l.nodeCfg.LocalVRFs {
		vrfs = append(vrfs, FlattenVRF{
			name: name,
			vni:  -1,
		})
	}
	sort.Slice(vrfs, func(i, j int) bool {
		return vrfs[i].name < vrfs[j].name
	})

	for _, vrf := range vrfs {
		if l.mgr.isReservedVRF(vrf.name) {
			continue
		}
		tid, ok := l.tableID[vrf.name]
		if !ok {
			var err error
			tid, err = l.findFreeTableID()
			if err != nil {
				return err
			}
			l.tableID[vrf.name] = tid
		}

		l.infos[vrf.name] = l.makeInfo(vrf.name, tid, vrf.vni)
	}

	{
		name := l.mgr.baseConfig.ClusterVRF.Name
		tid, ok := l.tableID[name]
		if !ok {
			return fmt.Errorf("cluster vrf not found in cra")
		}
		l.infos[name] = l.makeInfo(name, tid, -1)
	}

	{
		name := l.mgr.baseConfig.ManagementVRF.Name
		tid, ok := l.tableID[name]
		if !ok {
			return fmt.Errorf("management vrf not found in cra")
		}
		l.infos[name] = l.makeInfo(name, tid, -1)
	}

	return nil
}

func (l *Layer3) setupVRF(info InfoL3) error {
	if len(info.name) > maxVRFnameLen {
		return fmt.Errorf("VRF name too long (max 12): %s", info.name)
	}

	vrf := l.mgr.createVRF(info.name, info.tid, l.ns)

	if info.vni != -1 {
		br := l.mgr.createBridge(
			(bridgePrefix + info.name), "", vrf.Interfaces,
			info.mtu, true, false)

		l.mgr.createVXLAN(
			(vxlanPrefix + info.name), br, l.ns.Interfaces,
			info.vni, info.mtu, true, false)
	}

	return nil
}

func (l *Layer3) setup() error {
	if err := l.setupTableID(); err != nil {
		return err
	}

	if err := l.setupInformations(); err != nil {
		return err
	}

	for _, info := range l.infos {
		if err := l.setupVRF(info); err != nil {
			return err
		}
	}

	return nil
}
