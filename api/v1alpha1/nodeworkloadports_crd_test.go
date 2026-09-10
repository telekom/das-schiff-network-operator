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

package v1alpha1

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The port-wiring CEL rule is exercised against the CRDs the suite loads into
// envtest, since the gRPC server's validateWiring is bypassed by anything that
// writes NodeWorkloadPorts directly.
var _ = Describe("NodeWorkloadPorts port wiring schema", func() {
	entry := func(wiring PortWiring) *NodeWorkloadPorts {
		return &NodeWorkloadPorts{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "wiring-"},
			Spec: NodeWorkloadPortsSpec{Ports: []WorkloadPortEntry{{
				PodNamespace: "ns",
				PodName:      "pod",
				ContainerID:  "cid-1",
				WorkloadPort: WorkloadPort{Interface: "cra0cid1", PortWiring: wiring},
			}}},
		}
	}

	cases := []struct {
		name        string
		wiring      PortWiring
		wantInvalid bool
	}{
		{name: "veth with no socket fields"},
		{name: "explicit veth", wiring: PortWiring{Transport: PortTransportVeth}},
		{name: "vhostuser with socket", wiring: PortWiring{
			Transport: PortTransportVhostUser, SocketPath: "/run/vsr-vhost-user/3f9a/socket", SocketMode: SocketModeClient,
		}},
		{name: "vhostuser without socketPath", wiring: PortWiring{
			Transport: PortTransportVhostUser, SocketMode: SocketModeClient,
		}, wantInvalid: true},
		{name: "veth with socketPath", wiring: PortWiring{
			SocketPath: "/run/vsr-vhost-user/3f9a/socket",
		}, wantInvalid: true},
		{name: "veth with socketMode", wiring: PortWiring{SocketMode: SocketModeServer}, wantInvalid: true},
		{name: "unknown socketMode", wiring: PortWiring{
			Transport: PortTransportVhostUser, SocketPath: "/run/vsr-vhost-user/3f9a/socket", SocketMode: "peer",
		}, wantInvalid: true},
	}
	for _, tc := range cases {
		tc := tc
		It("is enforced by the apiserver: "+tc.name, func() {
			obj := entry(tc.wiring)
			err := k8sClient.Create(ctx, obj)
			if tc.wantInvalid {
				Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected an Invalid error, got %v", err)
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Delete(ctx, obj)).To(Succeed())
		})
	}
})
