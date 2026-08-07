//go:build linux

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

package agent_cra_frr //nolint:revive

import (
	"testing"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
)

func workloadPort(iface string) v1alpha1.WorkloadPort {
	return v1alpha1.WorkloadPort{
		Interface:  iface,
		GatewayV4:  "169.254.1.1/32",
		GatewayV6:  "fe80::1/128",
		HostRoutes: []string{"10.201.0.10/32"},
	}
}

// TestConvertWorkloadPortsCoversEveryTable asserts that routed attachments are
// picked up from every place the workloadcni merge can put them - in particular
// GlobalWorkloadPorts, which carries the underlay (no-VRF) attachments that are
// the primary use case.
func TestConvertWorkloadPortsCoversEveryTable(t *testing.T) {
	applier := &CRAFRRConfigApplier{
		baseConfig: &config.BaseConfig{ClusterVRF: config.BaseVRF{Name: "cluster"}},
	}

	nodeCfg := &v1alpha1.NodeNetworkConfig{
		Spec: v1alpha1.NodeNetworkConfigSpec{
			GlobalWorkloadPorts: []v1alpha1.WorkloadPort{workloadPort("cra000001")},
			ClusterVRF:          &v1alpha1.VRF{WorkloadPorts: []v1alpha1.WorkloadPort{workloadPort("cra000002")}},
			FabricVRFs: map[string]v1alpha1.FabricVRF{
				"tenant-a": {VRF: v1alpha1.VRF{WorkloadPorts: []v1alpha1.WorkloadPort{workloadPort("cra000003")}}},
			},
			LocalVRFs: map[string]v1alpha1.VRF{
				"tenant-b": {WorkloadPorts: []v1alpha1.WorkloadPort{workloadPort("cra000004")}},
			},
		},
	}

	want := map[string]string{
		"cra000001": "",         // underlay / default table
		"cra000002": "cluster",  // cluster VRF, by its configured device name
		"cra000003": "tenant-a", // fabric VRF
		"cra000004": "tenant-b", // local VRF
	}

	ports := applier.convertWorkloadPorts(nodeCfg)
	if len(ports) != len(want) {
		t.Fatalf("expected %d workload ports, got %d (%+v)", len(want), len(ports), ports)
	}
	for i := range ports {
		wantVRF, ok := want[ports[i].Interface]
		if !ok {
			t.Fatalf("unexpected workload port %q", ports[i].Interface)
		}
		if ports[i].VRF != wantVRF {
			t.Errorf("workload port %q: expected VRF %q, got %q", ports[i].Interface, wantVRF, ports[i].VRF)
		}
		if len(ports[i].HostRoutes) != 1 {
			t.Errorf("workload port %q: host routes not carried over: %+v", ports[i].Interface, ports[i].HostRoutes)
		}
	}
}
